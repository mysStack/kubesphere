# OCI 应用仓库健康信息 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 Repo Status 中记录同步结果和 OCI 指标，并在 Console 的仓库列表显示简短状态摘要。

**Architecture:** 保持现有异步 `RepoReconciler`。Controller 记录开始、成功、失败的通用生命周期；OCI loader 每次调用聚合 tag、缓存、版本、警告和请求统计并返回 Controller；HTTPS 仅写通用统计。Console 复用 Repo 列表接口，不新增 REST API。

**Tech Stack:** Go、controller-runtime、Helm/ORAS、Kubernetes CRD、React、TypeScript、styled-components。

**Spec:** `docs/superpowers/specs/2026-09-20-oci-repository-health-design.md`

## Global Constraints

- 不新增任务 CRD、数据库、ConfigMap、REST 接口或同步入口。
- HTTPS 不进入 OCI 缓存、统计、重试和 Registry 请求路径；只写通用同步字段。
- `status.sync` 可选，旧 Repo 和旧 Console 响应保持兼容。
- Status、Event、日志和 Console 不得包含密码、Token、Authorization header 或 URL 用户信息。
- 保留 OCI 全量校验、普通增量同步和 `allowDeletion := len(indexWarnings) == 0` 的现有语义。
- 在当前分支开发；后端未提交 `.gitignore` 不得暂存或提交。
- 新增提交信息使用中文。

---

### Task 1: Repo Status 模型与通用同步生命周期

**Files:**

- Modify: `staging/src/kubesphere.io/api/application/v2/types.go`
- Modify: `config/ks-core/charts/ks-crds/crds/application.kubesphere.io_repos.yaml`
- Modify: `pkg/controller/application/helm_repo_controller.go`
- Test: `pkg/controller/application/helm_repo_controller_test.go`

**Interfaces:**

- Add `appv2.RepoSyncStatus` and `RepoStatus.Sync *RepoSyncStatus`.
- `RepoSyncStatus` has `StartedAt`, `CompletedAt`, `DurationSeconds`, `ValidChartVersionCount`, `RemoteTagCount`, `SkippedArtifactCount`, `FailedTagCount`, `RequestCount`, `CacheHitCount`, and `LastError`.
- `UpdateStatus` persists the whole `RepoStatus`; `failRepoSync` completes the status before returning the original error.

- [ ] **Step 1: Write failing tests**

Add controller tests: a successful HTTPS sync records start, completion, duration and valid versions; a failed sync records completion and an error summary without the `secret` in `https://user:secret@example.test/charts`.

- [ ] **Step 2: Verify RED**

```bash
go test ./pkg/controller/application -run 'TestRepoReconciler.*SyncStatus' -count=1
```

Expected: fail because `RepoStatus.Sync` does not exist.

- [ ] **Step 3: Implement minimal Status storage**

Create the API type and matching optional CRD OpenAPI fields. Add small Controller helpers to begin and complete a sync. Start clears stale completion/error values; completion calculates duration, preserves already-collected metrics, and sanitizes URLs before assigning an error summary. HTTPS uses the number of filtered index versions as `ValidChartVersionCount`.

- [ ] **Step 4: Verify and commit**

```bash
gofmt -w staging/src/kubesphere.io/api/application/v2/types.go pkg/controller/application/helm_repo_controller.go pkg/controller/application/helm_repo_controller_test.go
go test ./pkg/controller/application -run 'TestRepoReconciler.*SyncStatus' -count=1
git diff --check
git add staging/src/kubesphere.io/api/application/v2/types.go config/ks-core/charts/ks-crds/crds/application.kubesphere.io_repos.yaml pkg/controller/application/helm_repo_controller.go pkg/controller/application/helm_repo_controller_test.go
git commit -m "feat: 记录应用仓库同步状态"
```

### Task 2: OCI loader 指标与 Controller 接入

**Files:**

- Modify: `pkg/simple/client/application/oci.go`
- Modify: `pkg/simple/client/application/oci_provider.go`
- Modify: `pkg/simple/client/oci/registry.go`
- Modify: `pkg/controller/application/helm_repo_controller.go`
- Test: `pkg/simple/client/application/oci_test.go`
- Test: `pkg/controller/application/helm_repo_controller_test.go`

**Interfaces:**

- Add `application.OCIIndexStats` with OCI-only fields from `RepoSyncStatus`.
- Add `LoadOCIRepoIndexWithCacheAndStats(ctx, url, credential, cache, options...) (helmrepo.IndexFile, OCIIndexStats, []error, error)`.
- Keep `LoadOCIRepoIndexWithCache` as a compatibility wrapper with its existing signature.
- Pass a per-sync request counter through OCI discovery and Registry creation; never use a global counter.

- [ ] **Step 1: Write failing loader tests**

Extend an existing `httptest` fixture with two valid tags, one cached valid tag, one metadata artifact and one 401 tag. Assert: `remoteTagCount=4`, `validChartVersionCount=2`, `skippedArtifactCount=1`, `failedTagCount=1`, `cacheHitCount=1`, and `requestCount>0`.

- [ ] **Step 2: Verify RED**

```bash
go test ./pkg/simple/client/application -run 'TestLoadOCIRepoIndexWithCache.*Stats' -count=1
```

Expected: fail because the stats-returning loader does not exist.

- [ ] **Step 3: Implement per-call statistics**

Count raw tags before filtering. Count cache hits when a cached chart is reused, valid versions when a chart enters the index, skipped artifacts only for `ErrNotHelmOCIArtifact`, and failed tags/repositories for other warnings. Wrap the Registry HTTP client with a local counting client so discovery, tags, manifests, blobs and retry attempts contribute to one counter.

- [ ] **Step 4: Connect Controller and test partial results**

Use the extended loader only in the OCI branch and copy its result into `Repo.Status.Sync` before success and failure updates. Add controller tests that partial warnings retain valid/cached metrics and leave deletion protection unchanged.

- [ ] **Step 5: Verify and commit**

```bash
gofmt -w pkg/simple/client/application/oci.go pkg/simple/client/application/oci_provider.go pkg/simple/client/oci/registry.go pkg/simple/client/application/oci_test.go pkg/controller/application/helm_repo_controller.go pkg/controller/application/helm_repo_controller_test.go
go test ./pkg/simple/client/application ./pkg/simple/client/oci ./pkg/controller/application -count=1
git diff --check
git add pkg/simple/client/application/oci.go pkg/simple/client/application/oci_provider.go pkg/simple/client/oci/registry.go pkg/simple/client/application/oci_test.go pkg/controller/application/helm_repo_controller.go pkg/controller/application/helm_repo_controller_test.go
git commit -m "feat: 增加 OCI 仓库同步健康统计"
```

### Task 3: Console 仓库列表摘要

**Files:**

- Modify: `packages/shared/src/types/app.ts`
- Modify: `packages/shared/src/components/Apps/RepoManage/index.tsx`
- Create: `packages/shared/src/components/Apps/RepoManage/syncSummary.ts`
- Test: `packages/shared/src/components/Apps/RepoManage/syncSummary.test.ts`
- Modify: `locales/{zh,en,es,tc}/l10n-workspaces-appManagement-appRepositories-list.js`

**Interfaces:**

- Extend optional `RepoData.status.sync`.
- Add pure `getRepoSyncSummary(sync, state)` returning no value for old data, a start label while syncing, or duration and valid-version summary after completion.

- [ ] **Step 1: Write failing UI helper tests**

Test old Repo status returns no summary; syncing uses `startedAt`; completed HTTPS uses duration and valid versions; OCI has the same short summary but never exposes `lastError` in the table.

- [ ] **Step 2: Verify RED**

Bundle the TypeScript helper test with installed `esbuild` and run `node --test`; the test must initially fail due to a missing helper.

- [ ] **Step 3: Implement the smallest display**

Keep the existing status dot and state label. Add a muted second line within the status cell: start time while syncing, otherwise duration plus valid version count. Do not add a table column, details page, diagnostics UI, or credential display.

- [ ] **Step 4: Add translations, verify, commit**

```bash
./node_modules/.bin/eslint packages/shared/src/types/app.ts packages/shared/src/components/Apps/RepoManage
./node_modules/.bin/prettier --check packages/shared/src/types/app.ts packages/shared/src/components/Apps/RepoManage locales/zh/l10n-workspaces-appManagement-appRepositories-list.js locales/en/l10n-workspaces-appManagement-appRepositories-list.js locales/es/l10n-workspaces-appManagement-appRepositories-list.js locales/tc/l10n-workspaces-appManagement-appRepositories-list.js
./node_modules/.bin/tsc --noEmit -p packages/shared/tsconfig.json
git diff --check
git add packages/shared/src/types/app.ts packages/shared/src/components/Apps/RepoManage locales/zh/l10n-workspaces-appManagement-appRepositories-list.js locales/en/l10n-workspaces-appManagement-appRepositories-list.js locales/es/l10n-workspaces-appManagement-appRepositories-list.js locales/tc/l10n-workspaces-appManagement-appRepositories-list.js
git commit -m "feat: 展示仓库同步摘要"
```

### Task 4: 回归、测试环境和路线图

**Files:**

- Modify: `docs/PLAN.md`
- Modify: `docs/superpowers/plans/2026-09-22-oci-repository-health.md`

- [ ] **Step 1: Run focused regressions**

```bash
go test ./pkg/simple/client/application ./pkg/simple/client/oci ./pkg/controller/application ./pkg/kapis/application/v2 -count=1
```

- [ ] **Step 2: Build/deploy after approval**

Build personal backend and Console images from this branch, update only `ks-apiserver`, `ks-controller-manager` and `ks-console` in `kubesphere-system`, and retain previous image tags for rollback.

- [ ] **Step 3: Validate repository behavior**

Run OCI incremental sync and OCI full refresh: ensure `status.sync` has OCI counters and no sensitive values. Run HTTPS sync: ensure it has only common metrics. Verify the Console list summary with an authenticated user session.

- [ ] **Step 4: Record evidence and commit**

Mark the repository-health items complete only after the test-environment evidence exists. Record commits, image tags, and current limits: no realtime percentage, history, or diagnostics detail panel.

```bash
git add docs/PLAN.md docs/superpowers/plans/2026-09-22-oci-repository-health.md
git commit -m "docs: 记录仓库健康信息验证"
```
