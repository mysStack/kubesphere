# OCI 应用仓库显式全量校验设计

## 背景

阶段二的 OCI 同步以已保存的 `ApplicationVersion` 作为 metadata 缓存。默认同步会复用命中缓存的 tag，避免重复请求 manifest 和 config blob。但 OCI Registry 的 tag 列表不携带 digest，因此默认同步无法发现已存在 tag 被远端重写的情况。

HTTPS Helm Repo 不存在这个问题：`index.yaml` 已提供完整版本元数据。本设计只扩展 OCI Repo。

## 目标

1. 保持现有“立即同步”默认增量、异步且快速返回的行为。
2. 提供显式 OCI 全量校验动作，按需重新读取每个有效 tag 的 manifest 和 metadata。
3. 全量动作继续复用现有并发上限、超时、429/5xx 退避、artifact 校验和部分失败保护。
4. 确保全量动作只执行一次，不影响后续定时或手动增量同步。
5. 在 Console 明确提示全量校验的请求成本；HTTPS Repo 不展示该入口。

## 非目标

- 不为 HTTPS Repo 增加新的同步模式。
- 不新增 Job、ConfigMap、数据库、CRD 字段或凭据存储。
- 不调整现有 OCI worker 并发、超时和重试默认值。
- 不在本阶段增加缓存命中数、请求数或耗时等可观测指标。

## API 与触发模型

复用现有 Workspace Repo 动作 API：

```text
POST /kapis/application.kubesphere.io/v2/workspaces/{workspace}/repos/{repo}/action
POST /kapis/application.kubesphere.io/v2/workspaces/{workspace}/repos/{repo}/action?mode=full
```

- 未提供 `mode` 或 `mode=incremental`：写入 `manualTrigger`，执行当前增量同步。
- `mode=full`：仅 OCI Repo 接受。Handler 写入 `manualTrigger`，并同时写入一次性全量校验注解和手动同步触发注解；API 仍快速返回，不等待 Registry 同步完成。
- 其他 `mode` 值或 HTTPS Repo 的 `mode=full` 返回 400，不产生任何同步动作。

使用注解而非 `RepoSpec` 字段，原因是该请求仅代表一次操作，不应改变持久同步策略或使 CRD schema 扩张。

## Controller 数据流

```text
Console 全量校验
  -> POST action?mode=full
  -> Repo.status=manualTrigger + full-refresh 注解 + trigger 注解
  -> Repo Controller 入队
  -> 读取并清理 full-refresh 注解（本次 reconcile 内保存 fullRefresh=true）
  -> OCI: LoadOCIRepoIndexWithCache(..., nil)
  -> worker pool 校验所有有效 tag
  -> 更新 Application / ApplicationVersion
  -> successful 或 failed
```

全量模式下向 OCI index loader 传入空缓存，因此每个通过 tag 初步过滤的候选版本都会读取 manifest，并仅对有效 Helm Chart artifact 读取 config blob。辅助 `*-metadata` tag 及不符合过滤规则的 tag 仍不发起 manifest 请求。

Controller 在开始同步前移除全量校验注解并保存本地布尔值。即使后续状态更新或 controller 重启，新的定时 reconcile 也只会走默认增量模式；正在运行的本次 reconcile 不受注解移除影响。

## 删除与失败语义

- 全量校验无 warning 时，沿用当前 Controller 删除策略：Registry 列表中已消失的 Application/ApplicationVersion 可被清理。
- 任何 tag 产生 warning 时，保留已有 Application/ApplicationVersion，避免网络、限流或单个无效 artifact 导致破坏性删除。
- Registry 整体失败仍将 Repo 状态设为 `failed`；API 动作请求本身已经成功受理，不将异步同步失败伪装为 API 超时。

## Console 交互

- OCI Repo 的操作菜单保留“立即同步”，调用默认 action。
- 同一菜单新增“重新校验全部版本”。用户确认后调用 `action?mode=full`。
- 确认文案说明：该动作会重新访问全部 OCI tag，可能较慢并消耗 Registry 配额；执行后用户通过现有 Repo 状态和事件查看结果。
- HTTPS Repo 不渲染全量校验菜单项。

## 测试策略

- Handler：默认和 `incremental` 写入普通手动触发；`full` 仅对 OCI 写入全量注解；无效模式和 HTTPS 全量请求返回 400。
- Controller：读取并清理一次性全量注解；全量模式调用 loader 时不传入缓存；普通 OCI 和 HTTPS 行为保持不变。
- OCI client：使用已有 httptest Registry，证明缓存 tag 在默认模式零 manifest/config 请求、全量模式重新请求 metadata；辅助 artifact 仍被跳过。
- Console：Repo store 请求正确附带 `mode=full`，操作菜单只对 OCI Repo 展示该动作。
- 回归：运行 application client、application controller、application API handler 与 Console 相关测试；测试环境执行一次增量同步和一次全量校验，确认后续同步恢复增量。

## 回滚

删除或回滚本阶段镜像即可恢复只有增量同步的阶段二行为。一次性注解即使残留也仅影响下一次 reconcile；回滚版本会忽略它。已有 `Application`、`ApplicationVersion`、Secret 和 Repo 不需要迁移或删除。
