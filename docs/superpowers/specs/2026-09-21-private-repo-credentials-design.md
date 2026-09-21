# 私有 Helm/OCI 应用仓库凭据设计

**日期：** 2026-09-21  
**状态：** 评审中  
**范围：** 阶段一 P0，私有 Helm 与 OCI 应用仓库的凭据创建、选择、绑定和安全复用。

## 目标与约束

当前 `Repo.spec.credentialSecretRef` 已能在仓库验证、Controller 同步和应用部署下载 Chart 时加载 Secret；Console 只有仓库 URL 表单，Workspace 用户无法安全地创建或选择凭据。

第一期目标是让用户在添加仓库时安全地使用私有 Harbor、Docker Registry、GHCR 或需要 Basic Auth 的 HTTPS Helm Repo，同时满足：

- 一个凭据只属于一个 Workspace，不提供跨 Workspace 共享。
- Kubernetes Secret 统一保存在 `kubesphere-system`，但必须带受后端校验的 Workspace 归属标签。
- Repo 只保存 `credentialSecretRef`，不保存用户名、密码或 Token。
- 凭据内容只在创建或替换时从浏览器发送到后端；任何查询接口都不返回 Secret data、密码或 Token。
- 仓库验证、定时同步、手动同步和部署下载走同一个 Secret 引用。
- 不复用 Console 的通用 Secret 查询接口；该接口会解码 Secret data，不适合仓库凭据选择。

本期不包含凭据跨工作空间共享、通用 Secret 导入、凭据编辑/轮换页面、TLS 客户端证书表单或凭据审计页面。这些能力可以在不改变 Repo 引用模型的后续迭代中补充。

## 前端交互设计

凭据属于仓库连接设置，但不应为所有公开仓库永久增加一个完整表单行。它放在现有 URL 标题行的右侧，作为紧凑的次级操作：

```
URL                                      私有仓库？配置访问凭据
[协议选择] [地址                                             ] [验证]
URL 需要通过验证才能添加或编辑应用仓库。
```

选中后，右侧操作收敛为带锁图标的 `凭据：harbor-readonly` 和“更换”，Token、用户名都不显示。点击该操作才打开小型选择面板：首项为“不使用凭据”，其后是当前 Workspace 的凭据名称，末项为“新建凭据”。这样默认表单高度、URL 输入宽度和“验证”按钮位置完全不变；只有需要认证时才进入第二层操作。

“新建凭据”打开一个小型独立弹窗，成功创建后自动回填并选中该凭据。独立创建而不是在 Repo 请求中内嵌 Token，有三个好处：创建后的凭据可被同 Workspace 的其他仓库复用；Repo API 永不接收需要持久化的明文凭据；取消仓库编辑只会留下一个可复用、无绑定的凭据，而不会造成半写入的 Repo。

创建弹窗第一期仅包含：

- 凭据名称（DNS label 规则）；
- 用户名（可选，令牌认证可按 Registry 约定填写）；
- 密码 / Access Token（密码输入框，创建后不可读取）。

Repo 编辑时可更换或清除已选凭据，但只能看到凭据名称。第一期不提供“查看 Token”“复制 Token”或通用 Secret 浏览入口；Token 轮换方式是创建新凭据并逐个切换关联仓库。后续若确有管理需求，再增加受引用检查保护的凭据管理页。

平台级应用仓库页面没有 URL 中的 Workspace 参数时，前端和后端都将其归属为 `system-workspace`；工作空间页面只列出当前 Workspace 的凭据。

## 后端存储与所有权

新建凭据写为 `kubesphere-system` 中的 `Opaque` Secret：

```yaml
metadata:
  name: harbor-readonly
  namespace: kubesphere-system
  labels:
    application.kubesphere.io/repo-credential: "true"
    kubesphere.io/workspace: team-a
type: Opaque
stringData:
  username: robot$helm
  password: <access-token>
```

`application.kubesphere.io/repo-credential=true` 防止用户将任意系统 Secret 当作仓库凭据选择。`kubesphere.io/workspace` 是唯一授权边界；请求路由中的空 Workspace 统一规范化为 `system-workspace`。

新增统一校验函数 `ValidateRepoCredentialSecretRef(ctx, workspace, ref)`，在下列路径调用：

1. 创建、编辑、验证 Repo；
2. Controller 同步 Repo；
3. 应用部署时从 Repo 下载 Chart。

校验要求：

- 引用允许为空；非空时 `name` 必填；
- namespace 只能为空或 `kubesphere-system`，持久化时规范化为该 namespace；
- Secret 必须存在、类型为 `Opaque`、带 `application.kubesphere.io/repo-credential=true`；
- Secret 的 Workspace 标签必须与 Repo 的 Workspace 标签完全一致；
- 不将 Secret 的 `data`、完整 URL 用户信息或认证 header 写入错误、Status、Event 或日志。

现有 loader `LoadRepoCredentialSecret` 保持为读取值的低层函数；新的校验在其之前执行。这样现有 OCI/HTTPS 下载逻辑无需复制认证行为。

## API 设计

在现有 application v2 Workspace 路由中增加：

| 方法 | 路径 | 行为 |
| --- | --- | --- |
| `GET` | `/workspaces/{workspace}/repo-credentials` | 返回该 Workspace 的凭据元数据列表；不返回 `data`。 |
| `POST` | `/workspaces/{workspace}/repo-credentials` | 创建 Opaque Secret；请求体仅在此接口携带用户名和 Token。 |
| `DELETE` | `/workspaces/{workspace}/repo-credentials/{name}` | 未被 Repo 引用才允许删除；被引用时返回冲突错误。 |

同一组也保留无 Workspace 前缀路由，并在后端归属到 `system-workspace`，与当前 Repo API 的全局仓库行为一致。

安全返回对象只包含 `apiVersion`、`kind`、`metadata.name`、`metadata.creationTimestamp` 和 Workspace 标签；不直接返回 Kubernetes `Secret`。创建请求示意：

```json
{
  "metadata": { "name": "harbor-readonly" },
  "credential": { "username": "robot$helm", "password": "<token>" }
}
```

Repo 请求只携带：

```json
{
  "spec": {
    "url": "oci://harbor.example.com/charts/private",
    "credentialSecretRef": { "name": "harbor-readonly", "namespace": "kubesphere-system" }
  }
}
```

List/Describe Repo 响应必须脱敏 `spec.credential`，只保留 `credentialSecretRef`。为兼容历史 API 中已经存在的 inline credential，第一期仍能读取和执行旧数据，但新 Console 不发送 inline credential；后端响应一律清空它，并将其视为待迁移的兼容路径。若请求同时提交 inline credential 和 `credentialSecretRef`，返回 400，避免凭据来源不明确。

删除凭据前，后端列出同 Workspace 的 Repo，若任何 Repo 引用该名称则返回 409，并提示先解绑或替换仓库凭据。API 不提供查询 Secret 内容、更新 Secret 内容或跨 Workspace 查找。

## Console 实现边界

- 在 `RepoManagementModal` 的 URL 组件后新增 `RepoCredentialSelect`；初始加载只调用新的安全元数据列表接口。
- 选择框值映射为 `spec.credentialSecretRef`，清除选择则删除该字段；绝不把凭据内容写入 `RepoData`、URL 或前端日志。
- “验证”沿用既有验证 API，但请求中同时传递当前 URL 和 `credentialSecretRef`；通过验证才可保存的现有交互保持不变。
- `CreateRepoCredentialModal` 用独立 mutation 调用创建接口，成功后刷新列表并自动选择新凭据；关闭时清空所有表单密码状态。
- 编辑已有仓库仅显示选择的凭据名。即使旧 Repo 中有 inline credential，也不展示其内容。
- 中文和英文文案覆盖“访问凭据（可选）”“新建凭据”“用户名”“密码 / Access Token”“该凭据正在被应用仓库使用，无法删除”等状态。

## 失败处理与兼容性

- Secret 不存在、标签错误、Workspace 不匹配、namespace 非法：Repo 保存/验证返回明确但不包含 Token 的 400/403 类错误。
- Registry 认证失败：验证或异步同步报告“认证失败或凭据无效”，不拼接认证头或 Secret 名称以外的内容。
- 旧 Repo 未使用 Secret 引用：维持当前行为；List/Describe 仅做输出脱敏，不改变运行中的同步。
- 已绑定凭据被外部删除：同步/部署报“仓库凭据不可用”；不会回退为匿名访问。
- 创建凭据后取消添加仓库：Secret 保留，允许后续仓库复用；这是可恢复的有意行为。

## 测试与验收

后端测试：

1. 创建凭据生成正确 namespace、类型和两个所有权标签；响应 JSON 不含密码或 Token。
2. List 仅返回当前 Workspace 的凭据元数据；序列化结果不包含 `data`、`password`。
3. Repo 验证拒绝跨 Workspace、未标记、错误 namespace 和不存在的 Secret 引用。
4. 有效引用在 HTTPS/OCI 验证、Controller 同步和下载 Chart 时能够成功加载；沿用并扩展现有 secret loader 测试。
5. Repo List/Describe 对历史 inline credential 脱敏；同时提交 inline credential 和引用被拒绝。
6. 被 Repo 引用的凭据删除返回冲突；解绑后可删除。

Console 测试：

1. 公开仓库不选凭据时请求不带 `credentialSecretRef`。
2. 选中凭据后验证和保存请求只携带 Secret 引用。
3. 创建凭据后密码不会出现在列表、Repo 详情或后续网络请求中。
4. 编辑仓库能显示并替换凭据名称，不能读取原 Token。

验收：在同一 Workspace 通过 Console 创建私有 OCI 与 HTTPS Repo，验证、立即同步和应用部署均成功；另一个 Workspace 无法列出、引用或删除该凭据；浏览器响应、Repo CR、Status、Event 和日志中均没有 Token。

## 后续演进

P1 可以在保持相同 Secret 引用和归属校验的前提下增加：凭据轮换/编辑、TLS CA 与客户端证书字段、引用计数、审计事件、管理员显式授权的跨 Workspace 共享。它们不属于本次 P0，避免先把简单的私有仓库接入做成通用凭据平台。
