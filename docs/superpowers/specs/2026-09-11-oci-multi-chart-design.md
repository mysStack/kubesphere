# OCI 多 Chart 应用仓库第一期技术设计

## 1. 目标与范围

第一期让一个 OCI 仓库路径能够像传统 Helm Repository 一样在 KubeSphere Store 中提供多个 Helm Chart。用户只填写一个 OCI URL，系统自动兼容单 Chart、Harbor Project 和通用 Distribution Registry 三种来源。

目标 URL：

```text
oci://registry.example.com/project/chart   # 单 Chart
oci://harbor.example.com/project           # Harbor Project
oci://registry.example.com/project         # 通用 Registry，尽力发现
```

第一期包含：

- repository 自动发现；
- Helm OCI artifact 识别；
- Chart metadata 读取；
- 多 Chart、多版本 Application/ApplicationVersion 同步；
- digest 增量判断；
- 单个 Chart 失败隔离；
- 保持 HTTP Helm Repo 和现有单 Chart OCI 兼容。

第一期不包含：

- 新增独立 OCI Catalog 微服务；
- 同步阶段下载完整 Chart tgz；
- 修改 Store 的 Application 数据模型；
- 支持所有厂商的专有 Registry API。

## 2. 当前代码链路

仓库创建和验证入口为 `pkg/kapis/application/v2/handler_repo.go`。HTTP 地址读取 `index.yaml`；OCI 地址调用 `ValidateOCIRepository`。创建的对象是现有 `Repo` CR。

`pkg/controller/application/helm_repo_controller.go` 负责 Repo reconcile：读取凭据、加载索引、删除源端不存在的 Application/ApplicationVersion，并调用 `CreateOrUpdateApp` 和 `CreateOrUpdateAppVersion` 写入 CR。OCI 与 HTTP 最终都转换为 `helmrepo.IndexFile`，因此上层 Application 模型可以保持不变。

`pkg/simple/client/application/oci.go` 负责 OCI tag 和 Chart 访问，`pkg/simple/client/application/store.go` 在部署阶段根据 `ApplicationVersion.spec.pullUrl` 调用 `HelmPullFromOci` 下载完整 Chart。

当前 OCI tag-only 实现没有读取 manifest，无法区分 Helm Chart 与普通 Docker Image，也不能填充 Chart 描述、图标和 maintainer。

## 3. 目标架构

```text
Repo Controller
    |
    +-- HTTP URL  ------> LoadRepoIndexFromHTTP (保持不变)
    |
    +-- OCI URL --------> LoadRepoIndexFromOCI
                              |
                              +-- SingleChartProvider
                              +-- HarborProjectProvider
                              +-- DistributionCatalogProvider
                                      |
                              OCI Artifact Inspector
                                      |
                              helmrepo.IndexFile
                                      |
                       Application / ApplicationVersion
```

Provider 只负责发现 repository；Artifact Inspector 负责列 tag、读取 manifest、判断 Helm artifact 和解析 metadata。Provider 不参与 Kubernetes CR 写入，Controller 不包含 Harbor API 细节。

建议接口：

```go
type OCIRepositoryProvider interface {
    DiscoverRepositories(ctx context.Context, source *url.URL, cred appv2.RepoCredential) ([]string, error)
}
```

底层 Registry Client 需要提供以下能力：

- 列 repository（支持分页）；
- 列 tags（支持分页）；
- 获取 manifest descriptor/digest；
- 获取 config blob；
- 下载完整 Chart（部署阶段已有能力）。

## 4. 自动识别规则

1. 先尝试将 URL path 作为直接 repository 查询。
2. 如果该 repository 存在且能列出 tags，则使用 `SingleChartProvider`。
3. 如果直接 repository 不存在：
   - 对 Harbor，验证 Project 后调用 Harbor API 分页列出 Project repositories；
   - 其他 Registry 尝试 `/v2/_catalog`，只保留 URL path 下的直接子 repository。
4. 没有发现 repository 时返回明确错误，不创建空 Repo。

该规则保持当前单 Chart OCI URL 的兼容性，同时允许 `oci://harbor/project` 自动发现多个 Chart。Provider 选择可先通过 Registry 能力探测；后续如自动探测不可靠，再增加显式配置字段。

## 5. Harbor Provider

Harbor Project 使用 Harbor API，而不是依赖 `/v2/_catalog`：

```text
GET  /api/v2.0/ping
HEAD /api/v2.0/projects?project_name=<project>
GET  /api/v2.0/projects/<project>/repositories?page=<n>&page_size=<n>
```

从 OCI URL 的第一个 path segment 提取 Project 名称，例如 `/helm` 对应 Project `helm`。返回的 repository 名称转换为完整 OCI repository path。

Harbor API 请求与 Registry 请求共用 `RepoCredential`、TLS 设置和代理配置，不在日志中输出用户名或密码。Robot Account 权限不足时将 Harbor API 错误返回为 Repo 同步诊断信息。

## 6. Helm Artifact 识别与 metadata

对每个候选 repository 的有效 tag：

1. 忽略 `*-metadata` 等已知辅助 tag；
2. 将 `_` 转为 `+` 后验证 SemVer；
3. 获取 manifest，不下载 chart layer；
4. 要求 manifest config 为 Helm config media type；
5. 要求至少包含 Helm chart layer 或 legacy Helm chart layer；
6. 获取 config blob 并解析 Helm Chart metadata；
7. 保存 manifest digest 作为该版本的 `ApplicationVersion.spec.digest`；
8. 保存原始 tag 到 `spec.pullUrl`，部署时使用原始 tag。

普通 Docker Image、仅有 metadata artifact、缺少 Helm layer 的对象被跳过。单个 tag 解析失败记录 repository、tag、digest（如果可得）和错误，但不影响其他 repository。

元数据映射：

```text
Chart.Metadata.Name         → Application/Version 名称
Description                 → ApplicationVersion annotation / abstraction
Home                        → spec.appHome
Icon                        → spec.icon
Maintainers                 → spec.maintainer
Version                     → spec.versionName
Manifest digest             → spec.digest
```

如果历史版本 metadata 尚未缓存，第一期可以使用该版本 config metadata；Chart tgz 只在部署时读取 values 和完整文件。

## 7. 增量同步与删除

现有 `ApplicationVersion.spec.digest` 作为持久化缓存，不新增 ConfigMap，也不直接操作 etcd。

每次同步：

```text
发现当前 repositories
  → 列出当前 tags
  → 删除源端已不存在的 Application/Version
  → 对新增或 digest 变化的 tag 读取 metadata
  → 对 digest 未变化的 tag 复用已有 CR metadata
```

manifest digest 是 OCI 的主要变化判断依据。ETag/Last-Modified 不是所有 Registry 都保证支持，第一期不将它们作为正确性依赖；可以作为 HTTP 客户端层的可选优化。

同步使用受限并发，默认每个 Repo 内按 repository 顺序处理，避免触发 Registry 限流。并发数应可配置或至少集中定义，不能由每个请求创建无限 goroutine。

## 8. 错误与状态

- 发现阶段失败：Repo 状态为 Failed，并保留可诊断错误。
- 某个 repository 失败：记录 Event 和日志，继续其他 repository。
- 某个 tag 不是 Helm artifact：静默跳过或记录 debug 日志。
- 有效 Chart 已同步但部分 Chart 失败：保留已同步结果，Repo 状态保持 Successful，并通过 Condition/Event 表示部分失败；如果当前 API 暂不支持 Conditions，则先使用事件和结构化日志，避免伪造新的状态值。
- 新 Repo 即使 `syncPeriod=0` 也必须完成一次初始同步；后续 `syncPeriod=0` 才表示禁用定时同步。

## 9. CRD、API 和前端

第一期优先不修改 `RepoSpec`，通过 URL 自动识别，以保持现有 API 和 CRD 向后兼容。只有在自动识别被实际 Registry 证明存在歧义时，才增加可选字段：

```yaml
spec:
  oci:
    mode: auto       # auto | single | project
    provider: auto   # auto | harbor | distribution
```

后端创建 Repo、Application 和 ApplicationVersion 的模型保持不变。Store 已按 Application 列表展示，因此多 Chart 主要是后端同步能力，基础 Store 页面不需要新数据模型。前端仓库表单只需确保 OCI URL、凭据和同步周期可正常提交；若后续增加 `oci.mode/provider`，再补充表单选项。

## 10. 改动边界

计划修改：

- `pkg/simple/client/application/oci.go`：Provider 选择、artifact 检查、metadata 和 digest 同步；
- `pkg/simple/client/oci/registry.go`：补充受控的 manifest/config 访问能力；
- `pkg/controller/application/helm_repo_controller.go`：消费增强后的 OCI index，处理部分失败结果；
- `pkg/kapis/application/v2/handler_repo.go`：保持 HTTP/OCI 验证分流，补充 OCI 错误信息；
- 对应单元测试、Controller 测试和 Harbor mock 测试。

第一期暂不修改：

- `Application`、`ApplicationVersion` 的核心 CRD 字段；
- HTTP Helm Repo 下载和解析逻辑；
- 部署阶段的 Helm Pull 流程；
- 独立 gRPC OCI Catalog 服务。

## 11. 测试与验收

单元测试：

- 单 Chart URL 保持原行为；
- Harbor Project API 分页发现多个 repository；
- Distribution `/v2/_catalog` 发现和 path 过滤；
- 普通 Docker Image 被过滤；
- `*-metadata` 和非法 SemVer tag 被过滤；
- Helm config、chart layer 和 metadata 正确解析；
- digest 未变化时不重复读取 config；
- 单个 Chart/Tag 失败不影响其他 Chart；
- HTTP Repo 回归测试；
- OCI 部署仍使用原始 tag 下载 tgz。

集成验收：

1. Harbor Project 中至少放置三个 Helm Chart；
2. KubeSphere 只创建一个 Repo；
3. Store 出现至少三个 Application；
4. 每个 Application 显示多个版本、描述、maintainer 和 icon；
5. 能成功读取 values 并完成部署；
6. 新增 Chart、版本后下次同步可见；
7. 删除版本行为与现有 HTTP Repo 一致；
8. 普通镜像不会进入 Store；
9. Harbor Robot Account 可以同步；
10. HTTP Repo 和单 Chart OCI 回归通过。

## 12. 风险与取舍

- `/v2/_catalog` 不是所有 Registry 都开放，因此 Harbor API 是 Harbor 的主路径，通用 catalog 只能兜底。
- 完整保证“普通镜像不进入 Store”需要读取 manifest；这比纯 tag 扫描请求更多，但不下载大体积 Chart layer。
- 每个历史版本都要求完整 metadata 时请求量仍可能较大；digest 缓存只避免重复读取，不能消除首次扫描成本。
- Provider 自动识别存在边界情况；第一期保留单 Chart URL 作为明确兜底，后续可增加显式 mode/provider。
- 前端源码不在当前 KubeSphere 后端仓库中，UI 兼容性需在对应 console 仓库单独验证。

