# 私有应用仓库凭据实施计划

> 设计依据：[私有 Helm/OCI 应用仓库凭据设计](../specs/2026-09-21-private-repo-credentials-design.md)

**目标：** 为私有 Helm/OCI Repo 提供按 Workspace 隔离的 Secret 凭据，同时让现有验证、同步和部署路径安全复用。

## 1. 后端安全模型与 API 类型

**文件：**
- 修改 `staging/src/kubesphere.io/api/application/v2/types.go`
- 新增 `pkg/kapis/application/v2/repo_credential.go`
- 修改 `pkg/kapis/application/v2/register.go`
- 新增 `pkg/kapis/application/v2/repo_credential_test.go`

1. 先编写 handler 测试：创建仅生成标记为仓库凭据的 Opaque Secret；列表结果和创建结果均不含 Secret data；跨 Workspace、未标记、错误 namespace 的引用被拒绝；被 Repo 引用时不能删除。
2. 定义安全请求/响应 DTO，不复用 Kubernetes `Secret` 作为 HTTP 返回对象。
3. 实现 Workspace 规范化（空路径为 `system-workspace`）、Safe list/create/delete handler 和路由。
4. 运行聚焦 API 测试。

## 2. Repo 引用校验与脱敏

**文件：**
- 修改 `pkg/simple/client/application/repo_credential.go`
- 修改 `pkg/kapis/application/v2/handler_repo.go`
- 修改 `pkg/controller/application/helm_repo_controller.go`
- 修改 `pkg/simple/client/application/store.go`
- 修改或新增对应测试：`handler_repo_test.go`、`store_test.go`、Controller 测试。

1. 先增加失败测试：Repo 不得引用其他 Workspace 或任意系统 Secret；Repo List/Describe 不得返回 legacy inline credential；Secret 缺失不能静默降级为匿名。
2. 实现统一 `ValidateRepoCredentialSecretRef`，要求 namespace、类型、标签和 Workspace 全部匹配。
3. 在保存/验证、同步和部署下载的凭据加载之前调用校验函数。
4. Repo API 同时提交 inline credential 与 Secret 引用时返回 400；历史 inline 数据继续运行但在响应中脱敏。
5. 运行 Helm Repo、Store 和 Controller 聚焦测试。

## 3. Console 凭据数据层

**文件：**
- 修改 `console/packages/shared/src/types/app.ts`
- 修改 `console/packages/shared/src/stores/openpitrix/repo.ts`
- 新增 `console/packages/shared/src/components/Modals/RepoManagementModal/RepoCredentialModal.tsx`
- 新增相关样式和测试（按 Console 现有测试框架落位）。

1. 添加只含元数据的 credential 类型以及 list/create mutation；禁止从通用 Secret store 获取数据。
2. 编写组件测试：选择只向 Repo 请求写入 `credentialSecretRef`；创建后的密码不保留在 Repo data 或列表数据中。
3. 实现创建凭据的独立小弹窗，关闭时清空密码状态，成功后返回安全元数据。

## 4. Console 紧凑布局与 Repo 绑定

**文件：**
- 修改 `console/packages/shared/src/components/UrlInput/index.tsx`
- 修改 `console/packages/shared/src/components/UrlInput/styles.ts`
- 修改 `console/packages/shared/src/components/Modals/RepoManagementModal/index.tsx`
- 添加中英文国际化文案。

1. 保持 URL 输入行和验证按钮原样；在 URL 标签行右侧增加文本型“配置访问凭据”操作，不新设常驻 FormItem。
2. 点击后展示紧凑选择面板：不使用凭据、当前 Workspace 凭据名称、新建凭据。
3. 已选择时展示锁图标、凭据名称和更换操作；不展示用户名或 Token。
4. 将选择结果绑定到 `spec.credentialSecretRef`；URL 或凭据改变时重置验证状态，验证调用继续使用完整 Repo payload。
5. 更新交互测试并执行 typecheck/build。

## 5. 回归与人工验证

1. 后端 Go 聚焦测试、Console typecheck/build 均通过。
2. 部署测试镜像后，分别用私有 OCI 与私有 HTTPS Repo 完成“创建凭据 → 选择 → 验证 → 保存 → 立即同步 → 部署”。
3. 用另一个 Workspace 确认无法列出、引用和删除凭据。
4. 检查 API 响应、Repo CR、Status、Event 和 Controller 日志不含 Token。
5. 记录镜像 tag、Action、测试证据；不合并 `master`，等待用户验收。
