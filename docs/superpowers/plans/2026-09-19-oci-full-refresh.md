# OCI 显式全量校验实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 OCI Repo 可以按需重新读取所有有效 tag 的 metadata，同时保持默认同步的缓存命中行为和 HTTPS 路径不变。

**Architecture:** Repo action Handler 接受 `mode=full`，在现有异步触发标记外写入一次性全量标记。Controller 读取并立即清理该标记；本次 OCI loader 收到 nil cache，后续同步恢复从 ApplicationVersion 构建缓存。Console 只在 OCI Repo 的操作菜单展示带确认的全量校验动作。

**Tech Stack:** Go、controller-runtime、Helm OCI client、React、TypeScript、react-query。

**Spec:** `docs/superpowers/specs/2026-09-19-oci-full-refresh-design.md`

## Global Constraints

- 仅 OCI Repo 接受 `mode=full`；HTTPS Repo 的 `index.yaml` 流程完全不变。
- 默认 action、定时同步、首次同步继续增量缓存。
- 全量校验沿用并发 4、30 秒超时、429/5xx/超时最多 3 次请求和 200ms、500ms 退避。
- 有 OCI warning 时禁止删除已有 Application 和 ApplicationVersion。
- 标记只影响一次 reconcile；不读取、输出或写入凭据；提交信息使用中文。

---

### Task 1: 定义并写入全量校验意图

**Files:**
- Modify: `staging/src/kubesphere.io/api/application/v2/constants.go`
- Modify: `pkg/kapis/application/v2/handler_repo.go:152-178`
- Test: `pkg/kapis/application/v2/handler_repo_test.go`

**Interfaces:**
- Add `FullRefreshTriggerAnnotation = "application.kubesphere.io/full-refresh-trigger"`.
- Retain `POST .../repos/{repo}/action`; supported `mode` values are empty, `incremental`, and `full`.

- [ ] **Step 1: Add a failing mode test**

Create `TestManualSyncModes` with fake status-subresource Repo objects. Cover default OCI, `incremental` OCI, `full` OCI, `full` HTTPS, and unknown `reset` mode. Success cases must assert `manualTrigger` and the existing manual-sync annotation; only full OCI asserts the new full-refresh annotation. Rejected cases must assert a 400 response with status and annotations unchanged.

- [ ] **Step 2: Prove red**

Run `go test ./pkg/kapis/application/v2 -run '^TestManualSyncModes$' -count=1`.

Expected: FAIL because the current handler ignores `mode`.

- [ ] **Step 3: Implement the smallest handler change**

Before `Status().Update`, validate mode exactly as follows:

```go
mode := req.QueryParameter("mode")
fullRefresh := mode == "full"
if mode != "" && mode != "incremental" && !fullRefresh {
	api.HandleBadRequest(resp, req, fmt.Errorf("unsupported sync mode %q", mode))
	return
}
if fullRefresh && !registry.IsOCI(repo.Spec.Url) {
	api.HandleBadRequest(resp, req, fmt.Errorf("full refresh is only supported for OCI repositories"))
	return
}
```

After the status update, use one Unix-nanosecond value for `ManualSyncTriggerAnnotation` and, only for full mode, `FullRefreshTriggerAnnotation`; keep metadata update after status update.

- [ ] **Step 4: Prove green and commit**

Run `go test ./pkg/kapis/application/v2 -count=1`.

Commit only the three Task 1 files using `git commit -m "feat: 支持 OCI 全量校验触发"`.

### Task 2: Controller 消费一次性标记并选择空缓存

**Files:**
- Modify: `pkg/controller/application/helm_repo_controller.go:218-252`
- Test: `pkg/controller/application/helm_repo_controller_test.go`
- Test: `pkg/simple/client/application/oci_test.go`

**Interfaces:**
- Add `func (r *RepoReconciler) consumeOCIFullRefresh(ctx context.Context, repo *appv2.Repo) (bool, error)`.
- When the annotation exists, remove only it with `r.Patch(ctx, repo, client.MergeFrom(before))` and return true.

- [ ] **Step 1: Add failing client and controller tests**

Extend the cached-tag httptest fixture. First call `LoadOCIRepoIndexWithCache` with populated cache and assert zero manifest/config calls for the cached semantic tag. Then call it with nil cache and assert one manifest and one config call. Keep a `*-metadata` tag and assert it remains at zero manifest calls.

Add controller tests for a Repo with the full-refresh annotation: `consumeOCIFullRefresh` returns true and removes the annotation from storage; a Repo without it returns false and retains unrelated annotations. Add a reconcile loader seam or fixture showing full OCI passes nil cache, normal OCI passes `BuildOCIChartVersionCache`, and HTTPS never calls OCI loading.

- [ ] **Step 2: Prove red**

Run `go test ./pkg/simple/client/application ./pkg/controller/application -run 'Test.*(FullRefresh|CachedTag|HTTPS)' -count=1`.

Expected: FAIL because the Controller has no one-shot cache selection.

- [ ] **Step 3: Implement one-shot selection**

Inside the OCI branch, consume the marker before building the index. If true, call `LoadOCIRepoIndexWithCache(ctx, repoURL, credential, nil)`; otherwise preserve the existing `BuildOCIChartVersionCache(appList.Items, appVersionList.Items)` call. Do not change tag filtering, worker-pool limits, retry policy, warnings, deletion protection, or the HTTPS `LoadRepoIndex` branch.

- [ ] **Step 4: Prove green and commit**

Run:

```bash
go test ./pkg/simple/client/application ./pkg/simple/client/oci ./pkg/controller/application ./pkg/kapis/application/v2 -count=1
gofmt -w pkg/controller/application/helm_repo_controller.go pkg/controller/application/helm_repo_controller_test.go pkg/kapis/application/v2/handler_repo.go pkg/kapis/application/v2/handler_repo_test.go staging/src/kubesphere.io/api/application/v2/constants.go
git diff --check
```

Commit Task 2 files using `git commit -m "feat: 全量校验绕过 OCI 版本缓存"`.

### Task 3: Console 增加确认后的全量校验入口

**Files:**
- Modify: `../console/packages/shared/src/stores/openpitrix/repo.ts:76-82`
- Modify: `../console/packages/shared/src/components/Apps/RepoManage/index.tsx:37-81`
- Modify: matching files under `../console/locales` only when translation keys are absent

**Interfaces:**
- `useRepoSyncMutation` accepts `{ repo_name: string; mode?: 'incremental' | 'full' }`.
- Export `getRepoSyncUrl(workspace, repo_name, mode)`; only full returns `?mode=full`.
- `isOCIRepo(record)` checks `record.spec.url.startsWith('oci://')`.

- [ ] **Step 1: Add a failing URL behavior check**

Use the existing Console test convention if it exists. If the shared package has no runner, add the pure `getRepoSyncUrl` helper and use the focused TypeScript build in Step 4 instead of adding a new test dependency. The check must establish that incremental has no query and full has exactly `?mode=full`.

- [ ] **Step 2: Update mutation and actions**

Change ordinary sync to `syncRepo({ repo_name: record.metadata.name })`. Add a second action immediately after it, only shown for OCI records. Use the project’s existing confirmation modal pattern. Its content must say that all OCI versions are rechecked and the operation may be slow or consume Registry quota. Confirm calls `syncRepo({ repo_name: record.metadata.name, mode: 'full' })`, displays the existing trigger-success notification, and refetches the table. HTTPS records must not render the action.

- [ ] **Step 3: Localize exactly the new UI**

Search for `FULL_REFRESH_REPOSITORY`; if absent, add Chinese and English menu, confirmation-title, and confirmation-content entries beside the existing `SYNC_REPOSITORY` keys. Do not render raw translation keys.

- [ ] **Step 4: Verify and commit Console changes**

From `../console`, run:

```bash
yarn prettier --check packages/shared/src/stores/openpitrix/repo.ts packages/shared/src/components/Apps/RepoManage/index.tsx
yarn eslint packages/shared/src/stores/openpitrix/repo.ts packages/shared/src/components/Apps/RepoManage/index.tsx
```

Commit Task 3 files with `git commit -m "feat: 增加 OCI 全量校验入口"`.

### Task 4: 构建、部署与行为验证

**Files:**
- Modify: `docs/PLAN.md`

- [ ] **Step 1: Build feature images**

Push both `feature/oci-performance-cache` branches. Trigger personal image workflows with `source_ref=feature/oci-performance-cache` and `oci-repo-full-refresh` tag prefix. Verify workflow head SHA equals the pushed commit.

- [ ] **Step 2: Deploy test components only**

Client-dry-run and update `kubesphere-system/ks-apiserver`, `ks-controller-manager`, and `ks-console`; wait for every rollout. Do not alter credentials, PVCs, or unrelated workloads.

- [ ] **Step 3: Validate the three OCI paths and HTTPS regression**

On an OCI Repo containing cached versions: default action must succeed with cached metadata requests skipped; `action?mode=full` must succeed and reread valid cached-tag metadata; the next default action must return to cache-hit behavior. On an HTTPS Repo, default action must succeed with no OCI Registry request. Confirm the full-refresh annotation is absent after the full reconcile and capture only non-sensitive Events/log evidence.

- [ ] **Step 4: Record evidence and commit**

After Step 3 succeeds, update `docs/PLAN.md`: mark explicit old-tag revalidation complete; record backend/Console image tags, commits, test date and the public-Registry cost limitation; leave metrics and parameter configuration unchecked. Run `git diff --check`, then commit with `git commit -m "docs: 记录 OCI 全量校验验证结果"`. Push the feature branches only; do not merge to master without user approval.
