# OCI 应用仓库性能、缓存和限流设计

## 背景

阶段一已经完成 HTTPS Helm Repo 与 OCI Repo 的分流、OCI Helm artifact 校验、直接 Chart 仓库和子仓库发现，以及异步手动同步。当前同步流程仍可能在单次同步中对大量 tag 顺序执行 manifest/config 请求；公共 Registry（尤其 Docker Hub）容易因此触发 429，重复同步也会重复读取已经保存的 Chart 元数据。

## 目标

1. 保留 OCI 仓库的全量版本能力。
2. 复用已同步的 `ApplicationVersion` 作为持久缓存，不新增 ConfigMap 或独立缓存数据库。
3. 对 manifest/config 请求设置并发上限、超时和有限退避重试。
4. 单个 tag 失败只产生警告，不阻塞其他有效版本。
5. 不改变 HTTPS Repo 的同步逻辑，也不改变部署时通过 OCI URL 拉取 Chart 的行为。

## 非目标

- 本阶段不增加 Console 进度条、缓存命中数等展示字段。
- 本阶段不删除 Registry 上已不存在的版本；当同步存在警告时禁止破坏性删除。
- 不伪造或修改 OCI manifest，不把 Worker 变成 Registry 代理。
- 不针对 Docker Hub 编写专用分支；认证、429 和重试使用通用 Registry 行为。

## 方案

### 缓存模型

`BuildOCIChartVersionCache` 从当前 Repo 对应的 `Application` 和 `ApplicationVersion` 构建 `host/repository:tag -> ChartVersion` 映射。默认增量同步以 tag 是否存在于该映射为依据：

- tag 已存在且缓存 metadata 完整：直接复用，不请求 manifest 或 config blob。
- tag 新增或缓存缺少 metadata：读取 manifest，校验 Helm media type，再读取 config blob，并保存 digest 到新的 ApplicationVersion。
- tag 被删除：本阶段保留已有 ApplicationVersion，避免暂时的 Registry 分页或权限问题导致应用消失。

OCI Registry 的 tag API 不返回每个 tag 的 digest，因此无法在不请求 manifest 的情况下确认远端 tag 是否被重写；而对每个已知 tag 请求 manifest 会重新引入 429 风险。本阶段按 OCI tag 通常不可变的约定优化默认同步：已知 tag 不重新探测 digest。后续再增加显式“全量校验”动作，用于接受较高请求成本的 digest 重检。

### 请求调度

对新增或缺少缓存的 tag 使用固定大小 worker pool，默认并发为 4。每个 tag 的工作包含：

1. 获取 manifest 和 digest。
2. 校验 Helm config media type 与 chart layer。
3. 获取 config blob 并解码 Chart metadata。
4. 保存 Registry 返回的 digest，供后续显式全量校验使用。

通过 context deadline 继承单次同步超时；worker 数量和重试策略集中在 OCI application client 内部，HTTPS Repo 不调用这些路径。

### 重试和退避

仅对 `429`、`5xx` 和网络超时进行最多 2 次重试，使用 200ms、500ms 的指数退避并尊重 `Retry-After`（不超过单请求截止时间）。认证错误、TLS 错误、404、无效 Helm artifact 不重试。重试耗尽后生成包含 repository、tag 和 digest 的 `OCIIndexWarning`。

### 结果和删除策略

有效版本继续写入 Helm index。任何 warning 都保留在现有事件/日志链路中，并禁止删除旧 Application/ApplicationVersion；只有完整无 warning 的同步才执行删除检查。worker 结果按 tag 汇总后排序，保证 index 输出稳定。

## 配置默认值

第一期使用安全默认值，不新增 CRD 字段：

- 最大并发：4
- 单次 Registry 请求超时：沿用现有 5 秒
- 最大重试次数：2（即最多 3 次请求）
- 退避：200ms、500ms

后续根据测试环境数据再把这些参数提升为 Controller 配置。

## 测试策略

- 单元测试验证已缓存 tag 不读取 manifest/config、新 tag 读取并写入 digest、worker 并发不超过上限、429/5xx/超时退避后成功、认证/404 不重试。
- 使用 httptest Registry 模拟 77 个版本、metadata artifact、重复 digest、慢响应和部分失败。
- 保留 HTTPS Repo 现有测试，确认其不经过 OCI worker/retry 逻辑。
- 测试环境验证 Docker Hub 代理下重复手动同步的请求量和最终状态。

## 回滚

阶段二只修改 OCI client 和相关单元测试；回滚到阶段一镜像即可恢复顺序同步。Application/ApplicationVersion、Secret 和 PVC 不删除、不迁移。
