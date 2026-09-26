# OCI 应用仓库阶段三收尾 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 完成 OCI 应用仓库的同步幂等、可读状态/诊断和私有凭据验收，并在测试环境验证。

**Architecture:** 后端保留现有异步 Controller，以 `Repo.status.sync` 为唯一同步结果来源；手动接口只返回状态快照并防止重复触发。Console 从已有状态推导展示状态，在详情页消费同一份 `RepoData`，不新建轮询或任务服务。

**Tech Stack:** Go、controller-runtime、go-restful、React、TypeScript、React Query、Kubernetes、Helm OCI。

**Spec:** `docs/superpowers/specs/2026-09-27-oci-stage-three-completion-design.md`

## Global Constraints

- 在 `upgrade/v4.1.4-oci` 当前分支完成，不合并 `master`。
- Repo、Status、Event、API 和浏览器不得输出密码、Token 或 URL userinfo。
- 不新增任务 CRD、Redis、数据库或同步 Worker。
- 所有提交使用中文；每个生产改动先写失败测试并确认失败。

---

### Task 1: 手动同步幂等响应

**Files:**
- Modify: `pkg/kapis/application/v2/handler_repo.go`
- Modify: `pkg/kapis/application/v2/handler_repo_test.go`
- Modify: `packages/shared/src/stores/openpitrix/repo.ts`（Console 仓库）
- Test: `packages/shared/src/stores/openpitrix/repo.test.ts`（Console 仓库）

**Interfaces:**
- Produces: `ManualSyncResponse{accepted, alreadyRunning, mode, repo}` JSON。
- Consumes: `Repo.status.state` 与 `ManualSyncTriggerAnnotation`。

- [ ] **Step 1: 写失败的 Go 测试**

```go
func TestManualSyncReturnsExistingRunWithoutReplacingTrigger(t *testing.T) {
  // Repo is syncing and already owns manual-sync-trigger="first".
  // POST action must return 200, alreadyRunning=true, and preserve "first".
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/kapis/application/v2 -run TestManualSyncReturnsExistingRunWithoutReplacingTrigger -count=1`

- [ ] **Step 3: 最小实现**

在 `ManualSync` 中检测现有触发器或执行中状态；重复请求直接写回状态快照。首次请求完成 metadata/status 写入后返回 `accepted=true`、`alreadyRunning=false` 和增量/full 模式。

- [ ] **Step 4: 验证 Go 测试通过**

Run: `go test ./pkg/kapis/application/v2 -run 'TestManualSync' -count=1`

- [ ] **Step 5: 写失败的 Console URL/响应测试**

```ts
test('keeps incremental as the default sync mode and accepts the async response', () => {
  assert.equal(getRepoSyncUrl('system-workspace', 'redis'), '.../action');
});
```

- [ ] **Step 6: 更新 store 类型与 mutation，运行测试**

Run: `yarn test packages/shared/src/stores/openpitrix/repo.test.ts`

### Task 2: 统一状态与诊断详情

**Files:**
- Modify: `packages/shared/src/components/Apps/RepoManage/syncSummary.ts`（Console 仓库）
- Modify: `packages/shared/src/components/Apps/RepoManage/syncSummary.test.ts`（Console 仓库）
- Create: `packages/console/src/pages/workspaces/containers/Repos/RepoDetail/Overview/index.tsx`（Console 仓库）
- Create: `packages/console/src/pages/workspaces/containers/Repos/RepoDetail/Overview/syncDiagnostics.ts`（Console 仓库）
- Create: `packages/console/src/pages/workspaces/containers/Repos/RepoDetail/Overview/syncDiagnostics.test.ts`（Console 仓库）
- Modify: `packages/console/src/pages/workspaces/containers/Repos/RepoDetail/routes.tsx`（Console 仓库）
- Modify: `packages/console/src/pages/workspaces/containers/Repos/RepoDetail/index.tsx`（Console 仓库）
- Modify: `locales/{zh,en}/l10n-workspaces-appManagement-appRepositories-list.js`（Console 仓库）

**Interfaces:**
- Produces: `getRepoPresentationState(status, syncPeriod, now)`，返回 `ready|syncing|failed|stale`。
- Produces: `getSyncDiagnosticItems(sync)`，只返回实际存在的非敏感指标。

- [ ] **Step 1: 写失败的状态测试**

```ts
test('marks a successful repo stale after two sync periods', () => {
  assert.equal(getRepoPresentationState('successful', 60, '2026-09-27T00:00:00Z', now), 'stale');
});
```

- [ ] **Step 2: 运行测试确认失败**

Run: `yarn test packages/shared/src/components/Apps/RepoManage/syncSummary.test.ts`

- [ ] **Step 3: 最小实现状态映射与文案**

仅在成功且周期大于零、完成时间超过两倍周期时展示 `stale`；不回写后端状态。保留原 `manualTrigger → syncing` 行为。

- [ ] **Step 4: 写失败的诊断项测试**

```ts
test('omits absent metrics and returns a text-only sanitized error item', () => {
  assert.deepEqual(getSyncDiagnosticItems({ lastError: '401 unauthorized' }), [/* expected */]);
});
```

- [ ] **Step 5: 实现 Overview 页签并运行测试**

新增默认 Overview 页签，读取 `useRepoDetail` 的 `status.sync`，用 Card/Description 样式显示字段；Events 页签保持不变。

Run: `yarn test packages/shared/src/components/Apps/RepoManage/syncSummary.test.ts packages/console/src/pages/workspaces/containers/Repos/RepoDetail/Overview/syncDiagnostics.test.ts`

### Task 3: 凭据和失败链路回归

**Files:**
- Modify: `pkg/kapis/application/v2/handler_repo_test.go`
- Modify: `pkg/controller/application/helm_repo_controller_test.go`
- Modify: `pkg/simple/client/application/store_test.go`
- Modify: `docs/PLAN.md`

**Interfaces:**
- Consumes: `credentialSecretRef`、`ValidateRepoCredentialSecretRef`、`RepoSyncStatus.LastError`。
- Produces: HTTPS/OCI 验证、同步、下载的无敏感信息回归证据。

- [ ] **Step 1: 写失败的隔离/脱敏测试**

```go
func TestManualSyncResponseDoesNotExposeInlineCredential(t *testing.T) {
  // POST action response must not contain the repository's legacy username or password.
}
```

- [ ] **Step 2: 运行并确认失败后最小修复**

Run: `go test ./pkg/kapis/application/v2 -run 'TestManualSync.*Credential' -count=1`

- [ ] **Step 3: 运行相关后端回归集**

Run: `go test ./pkg/kapis/application/v2 ./pkg/controller/application ./pkg/simple/client/application -count=1`

- [ ] **Step 4: 更新路线图证据**

仅在测试与环境证据完整后，将阶段一 P0、同步状态、错误展示及阶段三完成项标记为已完成；保留未做的实时百分比、历史任务和 SSE/WebSocket 说明。

### Task 4: 构建与测试环境验收

**Files:**
- Modify: `docs/PLAN.md`

- [ ] **Step 1: Console 静态验证**

Run: `yarn prettier --check <changed-files> && yarn tsc --noEmit`

- [ ] **Step 2: Console 生产构建**

Run: `yarn build`

- [ ] **Step 3: 推送前后端中文提交并触发 Action**

记录两个 commit SHA、Action run、镜像 tag；不创建正式版本标签。

- [ ] **Step 4: 部署到 192.168.2.131 测试环境**

等待 `ks-apiserver`、`ks-controller-manager`、`ks-console` 变为 Ready，确认实际镜像 tag。

- [ ] **Step 5: API、Kubernetes、浏览器回归**

验证一个 HTTPS Repo、一个 OCI Repo 和一个带 Secret 引用的私有 OCI Repo：增量同步、OCI 全量校验、重复点击幂等、失败诊断、Event 和详情页面。使用浏览器确认列表状态、诊断页、凭据下拉与应用版本可见；检查 API、Repo CR、Event、Controller 日志不含密码/Token。

- [ ] **Step 6: 更新验收记录并提交**

在 `docs/PLAN.md` 记录镜像 tag、commit、测试命令和已知限制；分别在前后端提交并 push。不要合并 `master`。
