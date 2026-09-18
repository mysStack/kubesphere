# OCI 快速全量索引技术设计

## 1. 背景与问题

当前 OCI 同步对每个语义化 tag 都获取 manifest，并对有效 Helm artifact 获取 config blob。以 Bitnami Redis 的约 190 个版本为例，首次同步至少需要约 380 次 Registry 请求；Docker Hub 认证账户的低配额度仍可能不足，匿名或未正确透传认证时更容易返回 429。

测试环境中的 `redis-oci` 已证明问题：仓库最终同步出 190 个 active 版本，但同步过程记录了大量 Docker Hub 429。当前 `ApplicationVersion.spec.digest` 缓存只避免重复获取 config blob，仍会在每次同步对全部 tag 请求 manifest，无法从根本上降低请求量。

## 2. 目标与非目标

目标：

- 单 Chart OCI 仓库首次同步可以生成全部历史语义化版本，且 Registry 请求量为常数级；
- 日常同步只请求 tags 列表，并只严格检查新 tag；
- 保持现有 `Application`、`ApplicationVersion`、`pullUrl` 和部署时 Helm OCI 拉取的模型不变；
- HTTP Helm Repository 的处理不变；
- 明确支持 Docker Hub 用户名和 Access Token 通过 Repo 凭据或 Secret 提供给 Controller。

非目标：

- 不新增数据库、ConfigMap 或独立 OCI 索引服务；
- 不在同步期间下载 chart `.tgz`；
- 不承诺识别同一 OCI repository 内所有混合的非 Helm OCI artifact；
- 不自动触发全量严格校验。

## 3. 核心决策

默认模式改为“快速索引”：将一个直接 OCI repository 视为单一 Helm Chart 的版本集合。

1. 获取 tags 列表并只保留有效 SemVer tag，忽略 `*-metadata`；
2. 选择最高 SemVer tag，获取它的 manifest 与 config，严格确认 Helm config media type、chart layer，并解析一次 Chart metadata；
3. 用这份已验证 metadata 为全部候选 tag 生成 `helmrepo.ChartVersion`；每个版本保留其原始 tag 的 `oci://host/repository:tag` pull URL；
4. 最新被验证版本保留 manifest digest；未逐个探测的历史版本 digest 留空；
5. Controller 继续写入现有 Application/ApplicationVersion CR，部署阶段仍由 `HelmPullFromOci` 拉取用户选择的精确 tag。

这将首次同步的网络成本从 O(tags) manifest/config 请求降为 O(1)；tags 列表若有分页仍按 Registry 分页读取，但 Redis 等常见单页仓库只需要一次 tags 请求。

## 4. 日常增量同步

同步时从已有 ApplicationVersion 的 `pullUrl` 建立已知 tag 集合。

```text
tags/list
  -> 过滤辅助 tag 与非 SemVer tag
  -> 已存在 tag：不请求 manifest，也不改动版本 metadata/digest
  -> 新 tag：请求 manifest + config 严格验证
       -> 是 Helm Chart：以新 tag 的 metadata 创建版本
       -> 非 Helm Chart / 请求失败：记录 warning，不创建版本
  -> tags 列表已经不存在的版本：按现有删除规则清理
```

首次同步没有已存在 tag 时，使用第 3 节的快速全量索引。后续一次通常只新增一个 Chart 版本，因此最多额外请求一个 manifest 和一个 config blob。

Helm Chart version tag 应视为不可变版本。若 registry 允许覆盖同名 tag，日常同步不会发现其 manifest digest 变化；管理员需要显式执行严格校验，或删除并重新添加该应用仓库。

## 5. 仓库验证与认证

添加页面的“验证”也使用快速验证：列 tags，然后严格校验最高 SemVer tag。它不扫描全部历史 tag，验证成功表示该 OCI repository 可作为单 Chart Helm OCI 仓库使用。

Controller 只会使用 `Repo.spec.credential` 或 `credentialSecretRef` 中的凭据；开发机的 `docker login` 不会传入 Controller。对 Docker Hub 需要创建 KubeSphere 应用仓库凭据，使用 Docker Hub 用户名与 Access Token，并确保 `docker.mystack.dpdns.org` 代理把认证请求正确转发或缓存。

## 6. 严格全量校验

本期不将严格全量校验加入自动同步。它保留为后续管理员显式动作：逐 tag 读取 manifest/config 并刷新 digest/metadata。

严格校验必须：

- 使用每个 registry host + credential 的共享限速器；
- 串行执行，收到 429 时遵守 `Retry-After` 或停止并报告需要稍后重试；
- 不触发 Controller 的快速重试循环；
- 不删除已有版本，除非完整 tags 列表成功返回。

该动作不属于本期实现，避免在缺少 UI、进度和配额策略时引入一个仍会超过 Docker Hub 配额的入口。

## 7. 兼容性与失败处理

- 多 Chart project / Harbor / catalog URL 继续沿用当前严格索引路径，因为不能安全假设所有 repository/tag 属于同一 Chart；
- 只有直接 repository URL 使用快速全量索引；
- 首次快速索引的最高 tag 无法严格验证时，Repo 同步失败，不创建不可信的应用版本；
- 增量新 tag 验证失败时，保留现有 Application/ApplicationVersion 并通过 warning/event 记录；
- `syncPeriod: 0` 仍表示初始同步后不进行定时同步；手动同步继续可用。

## 8. 改动边界与测试

修改范围：

- `pkg/simple/client/application/oci.go`：拆分首次快速全量索引、增量 tag 校验和现有严格索引；
- `pkg/simple/client/application/oci_test.go`：验证首次大量 tags 仅请求最高 tag 的 manifest/config，增量仅检查新增 tag，且无效新 tag 不删除历史版本；
- `pkg/kapis/application/v2/handler_repo_test.go`：验证 OCI 表单校验只检查一个候选 tag；
- `pkg/controller/application/helm_repo_controller_test.go`：验证已有 OCI 版本不会在下一轮同步重新探测 manifest，新增 tag 会被创建。

验收条件：

1. 190 个 tags 的直接 Helm OCI repository 首次同步只产生 tags/list、最高 tag manifest、最高 tag config 三类请求；
2. 同步后所有 SemVer tag 都有对应 ApplicationVersion，且 pull URL 指向其原始 tag；
3. 第二次无新增 tag 的同步不请求任何 manifest/config；
4. 新增一个 tag 时只请求该 tag 的 manifest/config；
5. HTTP repo、OCI chart 拉取和现有多 Chart project 测试保持通过；
6. Docker Hub 凭据仍可通过 credentialSecretRef 传入 Registry client，日志不输出秘密。
