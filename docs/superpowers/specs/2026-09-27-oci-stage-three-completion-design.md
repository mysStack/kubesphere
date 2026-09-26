# OCI 应用仓库阶段三收尾设计

**日期：** 2026-09-27  
**范围：** `upgrade/v4.1.4-oci` 上 OCI 应用仓库阶段一至三的未完成项。  
**不包含：** Gateway、Ingress、扩展生命周期和正式 `v4.1.4` 发布。

## 目标

在不新增任务 CRD、数据库或 Redis 的前提下，让 Repo 同步具有可解释、可重复触发且安全的体验：用户能看到同步生命周期、关键统计、失败原因和 Kubernetes Events；重复点击同步不会不断创建新的触发器；私有仓库凭据的所有权、脱敏和全链路复用有自动化及环境证据。

## 现有边界

- Controller 已以同一个 `RepoReconciler.Reconcile` 处理创建、定时和手动触发；不另建 Job。
- `Repo.status.sync` 已提供开始/完成时间、耗时、有效版本、OCI 请求/缓存统计和已脱敏的 `lastError`。
- `POST .../repos/{repo}/action` 已异步写入 annotation，Controller 消费后完成同步；`mode=full` 仅适用 OCI。
- Repo 表单已有连接验证，验证请求携带 URL 与 `credentialSecretRef`，不持久化密码。

## 状态模型

不改变 CRD 内的持久化 `status.state` 枚举，避免与旧 Controller、已有 Repo 发生兼容问题。Console 根据现有字段生成统一的展示状态：

| 展示状态 | 判定 |
| --- | --- |
| `Syncing` | `manualTrigger` 或 `syncing` |
| `Failed` | `failed`，显示已脱敏的 `status.sync.lastError` |
| `Stale` | 上次成功完成时间超过同步周期两倍，且周期大于 0；仅为 UI 告警，不改写后端状态 |
| `Ready` | `successful` 或其他已完成且未过期状态 |

`Stale` 以 `completedAt` 优先、`lastUpdateTime` 兜底。未设置周期或旧 Repo 缺少时间字段时不显示过期，保持当前兼容行为。

## 手动同步 API

保留现有 URL 和权限。接口返回一个不含凭据的响应体：

```json
{
  "accepted": true,
  "alreadyRunning": false,
  "mode": "incremental",
  "repo": { "metadata": { "name": "redis" }, "status": { "state": "manualTrigger" } }
}
```

- 同步尚未被 Controller 消费，或 Repo 已处于 `manualTrigger`/`syncing` 时，接口不覆盖现有 annotation，返回 `alreadyRunning: true`。
- 正常增量同步和 OCI 全量校验仅写一次触发 annotation，返回 `accepted: true`。
- `full` 仍拒绝 HTTPS；未知模式仍返回 400。
- 这不是新任务系统：响应中的 Repo 是当前任务状态快照，Console 继续仅轮询被触发条目，遇到终态停止。

## Console 诊断与操作

仓库列表继续保留紧凑布局，不增加列。状态第二行根据展示状态显示：同步开始时间、成功/失败后的耗时和有效版本数，或过期提示。

详情页默认增加“同步诊断”页签，展示存在的字段：开始/完成时间、耗时、有效版本、远端 tag、跳过 Artifact、失败 tag、请求数、缓存命中和脱敏错误。Events 保持独立页签。无 `status.sync` 的旧 Repo 显示兼容空态，而不是假造 `0`。

操作含义固定为：

1. **增量同步**：当前默认动作；读取 tag 并尽量复用缓存，只处理新增或变化内容。
2. **全量校验**：仅 OCI；重新读取已有 tag metadata，带确认提示。
3. **验证连接**：表单 URL 区域现有动作；使用当前 URL 与选中的凭据验证，不保存也不触发后台同步。

完成同步后，列表轮询收敛；应用商店的版本选择沿用其已有请求，因此重新打开或刷新时读取已更新的 ApplicationVersion。此期不新增跨页面 WebSocket/SSE 推送。

## 凭据验收

继续使用专用 repo credential Secret：`kubesphere-system` 命名空间、repo-credential 标签和严格 Workspace 标签。验证下列链路：

- 同 Workspace 的有效 Secret 可用于 HTTPS/OCI 验证、Controller 同步和 OCI Chart 下载。
- 跨 Workspace、未标记、错误 namespace、不存在引用均被拒绝。
- List/Describe、Status、Event、日志和浏览器响应不回传密码、Token 或 URL userinfo。

## 测试与回滚

- Go handler 测试覆盖首次受理、重复同步幂等、full/HTTPS 拒绝和响应脱敏。
- Console 单元测试覆盖状态映射、过期判定、诊断字段与 API URL/返回数据处理。
- Controller 现有同步状态与凭据测试继续作为回归集。
- 测试环境验证 HTTPS、OCI、私有 OCI：一次增量同步、一次全量校验、一次失败诊断；通过 API、`kubectl` 和浏览器确认。

本设计只扩展可选响应和 Console 展示；回滚镜像后 Repo CR、Secret、Application、ApplicationVersion 不需要迁移或删除。
