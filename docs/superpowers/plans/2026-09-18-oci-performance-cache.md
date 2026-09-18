# OCI 性能、缓存和限流实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在不改变 HTTPS Repo 行为的前提下，让 OCI 同步复用已保存版本、限制 Registry 并发并对可恢复错误进行退避重试。

**Architecture:** Controller 从现有 Application/ApplicationVersion 构建 OCI metadata cache。OCI client 对已缓存 tag 直接复用版本；只对新增或缺少缓存的 tag 通过固定大小 worker pool 读取 manifest/config。Registry client 负责将可恢复 HTTP 错误重试为有限退避；聚合的 per-tag warning 继续沿用现有 Controller 事件和删除保护。

**Tech Stack:** Go、controller-runtime、oras-go、Helm repo index、httptest。

**Spec:** `docs/superpowers/specs/2026-09-18-oci-performance-cache-design.md`

## Global Constraints

- HTTPS Helm Repo 不得进入 OCI worker、缓存或重试路径。
- 不新增 ConfigMap、数据库或凭据存储；复用 ApplicationVersion 的 Chart metadata 和 digest。
- 默认最大并发为 4，最大重试次数为 2，退避为 200ms、500ms，单请求超时沿用 5 秒。
- 单个 tag 失败只写入 warning；存在 warning 时 Controller 不删除已有 Application/ApplicationVersion。
- 已缓存 tag 默认不重新读取 manifest；旧 tag digest 全量重检留给后续显式操作。
- 不读取、输出或写入 Registry 密码和 Token。
- 所有新增 commit message 使用中文。

---

### Task 1: Registry 响应分类与有限退避

**Files:**
- Modify: `pkg/simple/client/oci/registry.go`
- Test: `pkg/simple/client/oci/registry_test.go`

**Interfaces:**
- Add unexported `retry(ctx context.Context, attempts int, operation func() (*http.Response, error)) (*http.Response, error)` or an equivalent helper used by OCI GET operations.
- Add an error wrapper preserving HTTP status and parsed `Retry-After`, so 429/5xx are distinguishable from 401/404/TLS failures.

- [ ] **Step 1: Add failing status/retry tests**

  Use `httptest.Server` to serve 429, 503, 401 and 404. Assert 429 and 503 are called three times at most; assert 401 and 404 are called once; assert a `Retry-After: 0` response does not block the test.

- [ ] **Step 2: Confirm the tests fail before implementation**

  ```bash
  go test ./pkg/simple/client/oci -run 'Test.*Retry|Test.*RetryAfter' -count=1
  ```

  Expected: FAIL because no retry behavior exists.

- [ ] **Step 3: Implement retry classification**

  Wrap `FetchManifestDescriptor`, `FetchBlob`, catalog and tags requests through one helper. Retry only HTTP 429, HTTP 500–599 and timeout-like transport errors. Apply 200ms then 500ms delay, bounded by request context; immediately return 401, 404, TLS and decode errors.

- [ ] **Step 4: Confirm focused tests pass**

  ```bash
  go test ./pkg/simple/client/oci -run 'Test.*Retry|Test.*RetryAfter' -count=1
  ```

- [ ] **Step 5: Commit**

  ```bash
  git add pkg/simple/client/oci/registry.go pkg/simple/client/oci/registry_test.go
  git commit -m "fix: 增加 OCI 请求退避重试"
  ```

### Task 2: 缓存命中跳过 OCI metadata 请求

**Files:**
- Modify: `pkg/simple/client/application/oci.go`
- Test: `pkg/simple/client/application/oci_test.go`

**Interfaces:**
- Keep `BuildOCIChartVersionCache(apps, versions) OCIChartVersionCache` as the persistent-cache boundary.
- Add an internal helper that accepts `repository`, filtered tags and `OCIChartVersionCache`, then returns cached versions, newly inspected versions and `[]error` warnings in deterministic order.

- [ ] **Step 1: Add failing cache-hit tests**

  Extend an existing OCI httptest fixture with atomic manifest/config request counters. Assert cached tags produce zero manifest and zero config requests, a new tag produces one manifest plus one config request, and `*-metadata` produces neither.

- [ ] **Step 2: Confirm tests fail before implementation**

  ```bash
  go test ./pkg/simple/client/application -run 'Test.*OCI.*Cache|Test.*Cached.*Tag' -count=1
  ```

  Expected: FAIL because the current sync loop does not distinguish cached tags in every OCI repository mode.

- [ ] **Step 3: Implement cache-first index construction**

  Filter auxiliary/non-semver tags before scheduling. Add cached `ChartVersion` entries directly to the Helm index; only schedule missing entries for artifact inspection. Preserve tag-specific OCI pull URLs and stable version ordering.

- [ ] **Step 4: Confirm focused tests pass**

  ```bash
  go test ./pkg/simple/client/application -run 'Test.*OCI.*Cache|Test.*Cached.*Tag' -count=1
  ```

- [ ] **Step 5: Commit**

  ```bash
  git add pkg/simple/client/application/oci.go pkg/simple/client/application/oci_test.go
  git commit -m "fix: 复用 OCI 版本缓存"
  ```

### Task 3: 新 tag 有界并发检查和部分失败汇总

**Files:**
- Modify: `pkg/simple/client/application/oci.go`
- Test: `pkg/simple/client/application/oci_test.go`

**Interfaces:**
- Define unexported constants `ociMetadataConcurrency = 4` and `ociMetadataRetryAttempts = 2`.
- The new helper returns all successful versions plus `OCIIndexWarning` values for each failed tag without data races or goroutine leaks.

- [ ] **Step 1: Add failing 77-tag concurrency tests**

  Create 77 new semantic tags in an httptest registry. Track active manifest requests with `atomic.Int32`; assert the peak is at most four. Make one tag return permanent 401 and verify the other 76 tags still produce versions and one warning.

- [ ] **Step 2: Confirm tests fail before implementation**

  ```bash
  go test ./pkg/simple/client/application -run 'Test.*Concurrency|Test.*77.*Tag|Test.*Partial.*Failure' -count=1
  ```

  Expected: FAIL because the current loop is sequential and does not expose bounded worker behavior.

- [ ] **Step 3: Implement worker pool and aggregation**

  Send missing tags through a buffered jobs channel, start exactly four workers, collect one result per tag, close the result channel after `sync.WaitGroup.Wait`, and sort versions/warnings by tag before adding to the index. Stop queued work when the parent context is canceled.

- [ ] **Step 4: Confirm focused tests pass**

  ```bash
  go test ./pkg/simple/client/application -run 'Test.*Concurrency|Test.*77.*Tag|Test.*Partial.*Failure' -count=1
  ```

- [ ] **Step 5: Commit**

  ```bash
  git add pkg/simple/client/application/oci.go pkg/simple/client/application/oci_test.go
  git commit -m "feat: 限制 OCI metadata 同步并发"
  ```

### Task 4: Controller 与 HTTPS 回归保护

**Files:**
- Test: `pkg/controller/application/helm_repo_controller_test.go`
- Test: `pkg/kapis/application/v2/handler_repo_test.go`
- Modify: `docs/PLAN.md`

**Interfaces:**
- Retain `LoadOCIRepoIndexWithCache` return contract: `(helmrepo.IndexFile, []error, error)`.
- Retain `allowDeletion := len(indexWarnings) == 0` behavior in `helm_repo_controller.go`.

- [ ] **Step 1: Add Controller regression assertions**

  Verify a warning-producing OCI sync leaves existing ApplicationVersion objects intact. Verify a warning-free OCI index remains eligible for deletion reconciliation. Verify an HTTPS Repo uses `LoadRepoIndex` and does not invoke OCI helpers.

- [ ] **Step 2: Run Controller/API regression tests**

  ```bash
  go test ./pkg/controller/application ./pkg/kapis/application/v2 ./pkg/simple/client/application ./pkg/simple/client/oci -count=1
  ```

  Expected: PASS.

- [ ] **Step 3: Update the roadmap**

  Mark only finished phase-two items in `docs/PLAN.md`. Record the concurrency/retry defaults and the explicit limitation that default sync assumes immutable tags.

- [ ] **Step 4: Run formatting and diff validation**

  ```bash
  gofmt -w pkg/simple/client/application/oci.go pkg/simple/client/application/oci_test.go pkg/simple/client/oci/registry.go pkg/simple/client/oci/registry_test.go
  git diff --check
  ```

- [ ] **Step 5: Commit**

  ```bash
  git add pkg/controller/application/helm_repo_controller_test.go pkg/kapis/application/v2/handler_repo_test.go docs/PLAN.md
  git commit -m "test: 补充 OCI 阶段二回归验证"
  ```

### Task 5: 构建、部署和行为验证

**Files:**
- No source changes unless verification identifies a regression.

- [ ] **Step 1: Run all relevant backend tests**

  ```bash
  go test ./pkg/simple/client/application ./pkg/simple/client/oci ./pkg/controller/application ./pkg/kapis/application/v2 -count=1
  ```

  Expected: PASS.

- [ ] **Step 2: Trigger the personal-image workflow**

  Run `Build Personal Images` on `feature/oci-performance-cache` with `source_ref=feature/oci-performance-cache` and `tag=oci-repo-phase2`.

- [ ] **Step 3: Deploy backend images to the test environment**

  Update only `ks-apiserver` and `ks-controller-manager` in `kubesphere-system`, then wait for both rollouts to complete before testing repository synchronization.

- [ ] **Step 4: Validate repeated OCI sync**

  Use an OCI repository with many tags, run an initial sync and a repeated manual sync, then compare controller logs/events to confirm cached tags do not request manifest/config and that partial errors do not delete existing versions. Sync one HTTPS Repo as a regression check.

- [ ] **Step 5: Record final evidence**

  Update `docs/PLAN.md` only after test-environment evidence is available, then commit with:

  ```bash
  git commit -m "docs: 记录 OCI 阶段二测试结果"
  ```
