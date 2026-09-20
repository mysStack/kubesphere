# OCI 应用仓库健康信息设计

## 目标

让应用仓库每次同步都留下可查询的结果，使用户能区分“正在同步、同步成功但跳过部分 Artifact、同步失败”，并能在 Console 看到同步规模、耗时和最近错误。

本期覆盖 OCI 仓库的完整指标；HTTPS 仓库复用同一状态结构，但只填充通用的开始/结束时间、耗时、有效 Chart 版本数和最近错误。

## 非目标

- 不新增同步任务 CRD，不改变现有异步 Controller、定时同步和“立即同步”接口。
- 不在本期实现实时百分比进度、历史同步记录或 Prometheus 指标。
- 不将 Registry 用户名、密码、Token 或含用户信息的 URL 写入 Status、Event 或 Console。

## 状态模型

在 `application.kubesphere.io/v2 Repo.status` 中保留现有 `state`、`lastUpdateTime`，增加可选 `sync` 字段：

```yaml
status:
  state: successful
  lastUpdateTime: "2026-09-20T16:30:12Z"
  sync:
    startedAt: "2026-09-20T16:30:01Z"
    completedAt: "2026-09-20T16:30:12Z"
    durationSeconds: 11
    remoteTagCount: 77
    validChartVersionCount: 76
    skippedArtifactCount: 1
    failedTagCount: 0
    requestCount: 80
    cacheHitCount: 70
    lastError: ""
```

字段含义：

- `remoteTagCount`：OCI tag 列表中发现的原始 tag 数，不包含 HTTPS。
- `validChartVersionCount`：本次可写入索引的有效 Helm Chart 版本数。
- `skippedArtifactCount`：已识别为非 Helm Chart 的 OCI Artifact 数。
- `failedTagCount`：tag 获取、manifest 或 metadata 读取失败的数量。
- `requestCount`：Controller 本次 OCI 索引过程实际发出的 Registry HTTP 请求总数，包含重试和发现请求。
- `cacheHitCount`：从已有 `ApplicationVersion` metadata 缓存直接复用、未读取 manifest/config 的版本数。
- `lastError`：本轮失败时写入的脱敏错误摘要；成功或仅有可恢复 warning 时清空。

使用 `omitempty` 以兼容已有 Repo 和 HTTPS Repo。Console 对未提供的 OCI 专属字段显示 `-`，而不是错误地显示 `0`。

## 后端数据流

```text
手动/定时触发
  → RepoReconciler 写入 syncing + sync.startedAt
  → OCI 发现、tag 列表、缓存命中、manifest/config 读取
  → OCIIndexStats 聚合请求、tag、缓存、跳过和失败数量
  → 创建/更新 ApplicationVersion
  → Repo.status.sync 写入最终统计 + successful/failed
```

为兼容已有调用，保留 `LoadOCIRepoIndexWithCache` 的现有签名；新增返回 `OCIIndexStats` 的内部/扩展 loader 供 `RepoReconciler` 使用。统计对象只在一次同步内存在，不使用全局变量。

Registry 层使用一个同步安全的请求计数器：所有 discovery、tag list、manifest、blob/config 以及 retry 的实际 HTTP attempt 都计入同一个本次同步计数器。Controller 创建计数器并将其传给 OCI discovery 与 index loader，避免遗漏 discovery 请求。

`OCIIndexWarning` 按根因归类：`ErrNotHelmOCIArtifact` 计入 skipped；其他 tag 或 repository warning 计入 failed。完全无法加载索引时，Controller 标记失败并保留已经得到的统计；部分 warning 的同步保持现有“保留旧版本、不删除”的策略。

开始同步时记录 `startedAt`；成功或失败均记录 `completedAt`、`durationSeconds`。失败路径通过统一的错误摘要函数写入 `lastError`，该函数只保留协议/HTTP 状态/普通错误文本，并对 OCI URL 去除用户信息。

## API 与 CRD

修改 `staging/src/kubesphere.io/api/application/v2/types.go`，新增 `RepoSyncStatus` 并将其挂到 `RepoStatus.Sync`。同时更新 `config/ks-core/charts/ks-crds/crds/application.kubesphere.io_repos.yaml` 的 OpenAPI schema，使 API Server 保留新 Status 字段。

现有 `/repos` 列表、详情和 `action` 接口直接返回 Repo 对象，因此不新增 REST endpoint，也不改变权限模型。

## Console

更新 shared `RepoData` 类型以包含 `status.sync`。

仓库列表保持现有状态点和文本；状态列增加简短第二行：同步中显示开始时间，成功或失败显示最近耗时与有效版本数。详情页增加“同步信息”区域，展示所有存在的统计字段和最近错误。错误用普通文本呈现，不渲染 HTML；没有指标的旧仓库不改变现有列表体验。

## 测试与验收

- OCI loader 的本地 Registry Mock：验证原始 tag、缓存命中、有效 Helm Chart、metadata Artifact、失败 tag 与真实 HTTP attempt 的计数。
- Controller fake client：验证同步开始、成功、部分 warning、失败时的 `Repo.status.sync` 写入；错误摘要不包含凭据。
- CRD/配置：渲染 `ks-core` Chart，确认 `status.sync` schema 存在。
- Console：类型检查与组件测试/渲染测试，确认缺失 `sync` 时兼容，存在统计时显示摘要与错误。
- 测试环境：对一个已有 OCI Repo 运行增量同步和一次全量校验，确认状态、Console 和 Event 均可读取，且无凭据泄漏。

## 回滚

该变更只增加可选 Status 字段。回滚镜像后旧 Controller 会忽略 `status.sync`；无需迁移或删除任何应用、Repo、Secret 或 PVC。
