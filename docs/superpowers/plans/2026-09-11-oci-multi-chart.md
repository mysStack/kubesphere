# OCI 多 Chart 应用仓库第一期 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让一个 OCI URL 能自动发现多个 Helm Chart，创建带真实元数据和 digest 的 Application/ApplicationVersion，同时保持 HTTP Repo 与单 Chart OCI 兼容。

**Architecture:** `LoadRepoIndexFromOci` 仍向现有 Repo Controller 返回 `helmrepo.IndexFile`。它先通过单 Chart、Harbor Project 或 Distribution catalog provider 发现 repository，再由统一 artifact inspector 检查 Helm OCI manifest/config 并生成 ChartVersion。完整 chart tgz 仅在已有的部署流程中下载。

**Tech Stack:** Go、controller-runtime、Helm v3 registry、ORAS、OCI Distribution API、Harbor API v2、`httptest`。

**Spec:** `docs/superpowers/specs/2026-09-11-oci-multi-chart-design.md`

## Global Constraints

- 不修改 HTTP Helm Repository 的 `index.yaml` 下载与解析路径。
- 保留 `oci://registry/project/chart` 单 Chart URL 的兼容行为。
- 不引入独立 OCI Catalog 服务、Rust 或新的运行时组件。
- 同步时不下载完整 chart tgz；部署继续由 `HelmPullFromOci` 下载。
- 所有 Registry 和 Harbor 请求复用 `RepoCredential`、TLS、代理和 `CredentialSecretRef`。
- 不能在日志、事件或错误中输出用户名和密码。
- 不合并到 `master`；只提交当前 `fix/oci-repo-sync-status` 分支。
- 不暂存用户已有的 `.gitignore`、`.claude/` 改动。

---

## File structure

- Modify: `pkg/simple/client/application/oci.go` — OCI provider 选择、artifact inspection、metadata/digest 到 `helmrepo.IndexFile` 的转换。
- Create: `pkg/simple/client/application/oci_provider.go` — provider 接口、单 Chart、Harbor Project、Distribution catalog 的发现实现。
- Modify: `pkg/simple/client/oci/registry.go` — 返回 manifest digest 的受限 manifest 查询及可复用认证 HTTP 请求能力。
- Modify: `pkg/controller/application/helm_repo_controller.go` — 使用完整 OCI index，并将单 Chart 局部错误记录为 Event 后继续同步其他 Chart。
- Modify: `pkg/kapis/application/v2/handler_repo.go` — OCI 验证调用增强后的索引加载器，但 HTTP 确认逻辑保持不变。
- Modify: `pkg/simple/client/application/oci_test.go` — provider、artifact、metadata、digest、过滤与部分失败测试。
- Modify: `pkg/controller/application/helm_repo_controller_test.go` — 多 Chart 创建、删除及部分失败 controller 测试。
- Modify: `pkg/kapis/application/v2/handler_repo_test.go` — OCI 验证的 Helm artifact 与多 Chart 行为测试。

## Task 1: Registry manifest descriptor support

**Files:**
- Modify: `pkg/simple/client/oci/registry.go:218-252`
- Test: `pkg/simple/client/oci/registry_test.go`

**Interfaces:**
- Produces: `func (r *Registry) FetchManifestDescriptor(ctx context.Context, repository, tag string) (ocispec.Manifest, string, error)`.
- Consumes: existing `Registry.do`, `buildScheme`, `ParseErrorResponse`.

- [ ] **Step 1: Write failing tests for manifest body and digest header**

Create tests using `httptest.NewServer` that return a valid OCI manifest with:

```go
w.Header().Set("Docker-Content-Digest", "sha256:manifest-digest")
w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
_ = json.NewEncoder(w).Encode(manifest)
```

Assert that `FetchManifestDescriptor` returns the decoded manifest and `sha256:manifest-digest`. Add a non-200 case asserting the method returns an error.

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
go test ./pkg/simple/client/oci -run '^(TestFetchManifestDescriptorReturnsDigest|TestFetchManifestDescriptorReturnsRegistryError)$' -count=1
```

Expected: FAIL because `FetchManifestDescriptor` does not exist.

- [ ] **Step 3: Implement the minimal descriptor method**

Add a method beside `FetchManifest`. It must issue the same `GET /v2/<repository>/manifests/<tag>` request and accept `ocispec.MediaTypeImageManifest`. Decode the body with `limitReader`; return `resp.Header.Get("Docker-Content-Digest")` without logging request credentials or response body.

- [ ] **Step 4: Run focused tests**

Run the command from Step 2. Expected: PASS.

- [ ] **Step 5: Commit the isolated registry change**

```bash
git add pkg/simple/client/oci/registry.go pkg/simple/client/oci/registry_test.go
git commit -m "feat: expose OCI manifest digest"
```

## Task 2: Repository discovery providers

**Files:**
- Create: `pkg/simple/client/application/oci_provider.go`
- Test: `pkg/simple/client/application/oci_test.go`

**Interfaces:**
- Produces:

```go
type OCIRepositoryProvider interface {
    Discover(ctx context.Context, source *url.URL, cred appv2.RepoCredential) ([]string, error)
}

func DiscoverOCIRepositories(ctx context.Context, source *url.URL, cred appv2.RepoCredential) ([]string, error)
```

- Consumes: `newOCIRegistry`, `getOCITags`, `isOCIRepositoryNotFound`, and `oci.Registry.Repositories`.

- [ ] **Step 1: Write failing direct repository and catalog fallback tests**

Keep the existing direct and catalog tests, but change them to call `DiscoverOCIRepositories`. Add assertions that a direct `charts/demo` URL does not request `/v2/_catalog`, while `oci://host/charts` with `NAME_UNKNOWN` returns only `charts/<direct-child>` repositories.

- [ ] **Step 2: Run the discovery tests to verify failure**

Run:

```bash
go test ./pkg/simple/client/application -run '^(TestDiscoverOCIRepositoriesDirectChart|TestDiscoverOCIRepositoriesFallsBackToCatalog)$' -count=1
```

Expected: FAIL because `DiscoverOCIRepositories` does not exist.

- [ ] **Step 3: Implement `SingleChartProvider` and `DistributionCatalogProvider`**

In `oci_provider.go`:

```go
type singleChartProvider struct{}
type distributionCatalogProvider struct{}
```

`DiscoverOCIRepositories` must first call `getOCITags` for the URL path. A successful non-empty tag result returns exactly that path. Only a repository-not-found response may fall through to catalog discovery. Catalog discovery must retain only direct children of the requested path, sort and deduplicate results.

- [ ] **Step 4: Write failing Harbor API discovery tests**

Use an `httptest` server to return:

```text
GET /api/v2.0/ping                                      → Pong
HEAD /api/v2.0/projects?project_name=helm               → 200
GET /api/v2.0/projects/helm/repositories?page=1&page_size=100 → [{"name":"helm/traefik"}]
GET /api/v2.0/projects/helm/repositories?page=2&page_size=100 → []
```

Assert `DiscoverOCIRepositories` returns `helm/traefik` and sends Basic auth when credentials are supplied.

- [ ] **Step 5: Implement `HarborProjectProvider`**

Implement an unexported provider which:

1. uses the same TLS/proxy-aware `oci.Registry` client transport;
2. probes `/api/v2.0/ping`;
3. extracts the first URL path segment as the Harbor project;
4. validates the project with `HEAD /api/v2.0/projects?project_name=<escaped-project>`;
5. lists `GET /api/v2.0/projects/<project>/repositories?page=<n>&page_size=100` until an empty page;
6. returns repository `name` values only under the requested project.

Provider selection order is: direct chart, Harbor Project when Harbor validation succeeds, Distribution catalog fallback. A Harbor 404 must allow catalog fallback; an authenticated Harbor 401/403 must be returned rather than hidden.

- [ ] **Step 6: Run discovery test suite**

Run:

```bash
go test ./pkg/simple/client/application -run '^(TestDiscoverOCIRepositories|TestGetRepoChartsFromOci)' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit discovery providers**

```bash
git add pkg/simple/client/application/oci_provider.go pkg/simple/client/application/oci.go pkg/simple/client/application/oci_test.go
git commit -m "feat: discover OCI charts from Harbor projects"
```

## Task 3: Helm OCI artifact inspection and index construction

**Files:**
- Modify: `pkg/simple/client/application/oci.go:57-135`
- Test: `pkg/simple/client/application/oci_test.go`

**Interfaces:**
- Produces:

```go
func LoadRepoIndexFromOci(u string, cred appv2.RepoCredential) (helmrepo.IndexFile, error)
func inspectOCIChart(ctx context.Context, reg *oci.Registry, repository, tag string) (*helmrepo.ChartVersion, error)
```

- Consumes: `DiscoverOCIRepositories`, `Registry.FetchManifestDescriptor`, `Registry.FetchBlob`, Helm `registry.ConfigMediaType`, `registry.ChartLayerMediaType`, `registry.LegacyChartLayerMediaType`.

- [ ] **Step 1: Replace tag-only assumptions with failing artifact tests**

Add an HTTP fixture serving one project with:

- `charts/traefik:1.0.0` whose manifest has Helm config and chart layer;
- `charts/redis:2.0.0` whose manifest has Helm config and chart layer;
- `images/nginx:1.0.0` whose config media type is OCI image config.

Provide config blobs containing distinct `chart.Metadata`. Assert the loaded index contains only `traefik` and `redis`, with description, icon, maintainer, original OCI pull URL and manifest digest.

- [ ] **Step 2: Run the artifact test to verify failure**

Run:

```bash
go test ./pkg/simple/client/application -run '^TestLoadRepoIndexFromOciBuildsMetadataAndFiltersImages$' -count=1
```

Expected: FAIL because the current tag-only index includes the image and has empty metadata/digest.

- [ ] **Step 3: Implement `inspectOCIChart`**

The inspector must:

```go
manifest, digest, err := reg.FetchManifestDescriptor(ctx, repository, tag)
```

Then reject artifacts unless `manifest.Config.MediaType == registry.ConfigMediaType` and a layer uses `registry.ChartLayerMediaType` or `registry.LegacyChartLayerMediaType`. Fetch only `manifest.Config`, decode JSON into `chart.Metadata`, normalize tag `_` to `+` for `Metadata.Version`, preserve original tag in pull URL, and return `ChartVersion{Metadata, URLs, Digest}`. Define one sentinel error for non-Helm artifacts so callers can skip it without treating it as a repository failure.

- [ ] **Step 4: Implement full OCI index generation**

Replace `LoadRepoIndexFromOciTags` internals with provider discovery plus inspection. Keep `LoadRepoIndexFromOciTags` as a compatibility wrapper only if callers/tests still use it; it must now return the full inspected index. Ignore `*-metadata` and invalid SemVer tags before manifest requests. Sort entries before return.

- [ ] **Step 5: Add failure-isolation and auxiliary tag tests**

Create one fixture where `charts/broken:1.0.0` returns malformed metadata while `charts/good:1.0.0` is valid. Assert the index contains `good`, not `broken`, and exposes a typed partial-result error/report for the controller. Add a test proving `1.0.0-metadata` never triggers a manifest request.

- [ ] **Step 6: Run all OCI client tests**

Run:

```bash
go test ./pkg/simple/client/application ./pkg/simple/client/oci -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit artifact inspection**

```bash
git add pkg/simple/client/application/oci.go pkg/simple/client/application/oci_test.go pkg/simple/client/oci/registry.go pkg/simple/client/oci/registry_test.go
git commit -m "feat: inspect Helm OCI artifacts during sync"
```

## Task 4: Digest-aware reconciliation and partial sync reporting

**Files:**
- Modify: `pkg/controller/application/helm_repo_controller.go:231-298`
- Modify: `pkg/controller/application/helm_repo_controller_test.go`

**Interfaces:**
- Consumes: inspected `helmrepo.ChartVersion.Digest` and an OCI loader result that includes per-repository warnings.
- Produces: unchanged `ApplicationVersion.spec.digest`; Event warnings for skipped OCI repositories.

- [ ] **Step 1: Write failing multi-Chart controller test**

Create a fake OCI server exposing two valid Helm repositories under one project. Reconcile a single `Repo` whose status is `manualTrigger`. Assert:

```text
one Repo
two Applications, each labelled with that Repo name
all valid ApplicationVersions with non-empty digest
metadata copied to ApplicationVersion and Application
```

- [ ] **Step 2: Run the controller test to verify failure**

Run:

```bash
go test ./pkg/controller/application -run '^TestRepoReconcilerCreatesApplicationsForMultipleOCICharts$' -count=1
```

Expected: FAIL before Task 3/4 wiring is complete.

- [ ] **Step 3: Preserve digest cache behavior**

Keep the existing `repoParseRequest` rule: do not rewrite an existing ApplicationVersion only when the remote digest equals its stored digest. With a non-empty OCI digest, changed manifests must generate an update request; unchanged manifests must not recreate the version.

Add a test using an existing ApplicationVersion whose digest matches the fixture manifest and assert its `Created` value is unchanged after reconciliation.

- [ ] **Step 4: Isolate repository-level failures**

Make the OCI loader return usable index entries plus warnings. In `Reconcile`, emit one `Warning` Event per warning and continue creating valid apps. If no valid Helm chart is found, preserve the existing failed Repo behavior. If at least one chart is synchronized, set Repo status to `successful` and do not emit credentials in events/logs.

- [ ] **Step 5: Add deletion regression test**

Seed two applications/versions under one Repo, serve only one Chart/version from OCI, reconcile, and assert the absent Application/Version are removed using the existing controller ownership and label semantics.

- [ ] **Step 6: Run controller tests**

Run:

```bash
go test ./pkg/controller/application -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit controller integration**

```bash
git add pkg/controller/application/helm_repo_controller.go pkg/controller/application/helm_repo_controller_test.go
git commit -m "feat: sync multiple OCI Helm charts"
```

## Task 5: Repo validation and deployment compatibility

**Files:**
- Modify: `pkg/kapis/application/v2/handler_repo.go:60-86`
- Modify: `pkg/kapis/application/v2/handler_repo_test.go`
- Test: `pkg/simple/client/application/store_test.go`

**Interfaces:**
- Consumes: `LoadRepoIndexFromOci` and `ValidateOCIRepository`.
- Produces: validation success only if at least one valid Helm OCI Chart is discoverable.

- [ ] **Step 1: Write a failing validation test for a Docker-only OCI URL**

Serve a repository with semver tags but OCI image config media type. Send `POST /repos?validate=true` and assert a non-200 response. Keep the valid Helm manifest test and assert it still returns 200.

- [ ] **Step 2: Run validation tests to verify failure**

Run:

```bash
go test ./pkg/kapis/application/v2 -run '^(TestValidateOCIRepoReadsHelmMetadata|TestValidateOCIRepoRejectsDockerImage)$' -count=1
```

Expected: FAIL until OCI validation invokes the inspected loader.

- [ ] **Step 3: Implement validation behavior**

`ValidateOCIRepository` must call the inspected OCI loader and succeed only if it has at least one valid `IndexFile.Entries` value. It must not persist a Repo during validation. `CreateOrUpdateRepo` must preserve the existing policy: OCI confirmation does not perform a second expensive validation; HTTP confirmation continues to load `index.yaml`.

- [ ] **Step 4: Verify deployment keeps original OCI tag and credentials**

Extend `store_test.go` with a version created from `1.1.0_build.1`, then assert `DownLoadChart` receives `oci://host/charts/demo:1.1.0_build.1` and uses `CredentialSecretRef`. This protects the required `_` → `+` display-only conversion.

- [ ] **Step 5: Run API and store tests**

Run:

```bash
go test ./pkg/kapis/application/v2 ./pkg/simple/client/application -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit validation and pull compatibility**

```bash
git add pkg/kapis/application/v2/handler_repo.go pkg/kapis/application/v2/handler_repo_test.go pkg/simple/client/application/store_test.go
git commit -m "fix: validate OCI Helm artifacts"
```

## Task 6: Regression verification and test-cluster acceptance

**Files:**
- Modify only if test findings require a focused correction.

**Interfaces:**
- Consumes: all prior tasks.
- Produces: evidence that HTTP, OCI single Chart, Harbor Project discovery and deployment still work.

- [ ] **Step 1: Run formatting and static checks**

Run:

```bash
gofmt -w pkg/simple/client/application/oci.go pkg/simple/client/application/oci_provider.go pkg/simple/client/application/oci_test.go pkg/simple/client/oci/registry.go pkg/simple/client/oci/registry_test.go pkg/controller/application/helm_repo_controller.go pkg/controller/application/helm_repo_controller_test.go pkg/kapis/application/v2/handler_repo.go pkg/kapis/application/v2/handler_repo_test.go
git diff --check
```

Expected: no formatting changes left and no whitespace errors.

- [ ] **Step 2: Run targeted unit and controller suites**

Run:

```bash
go test ./pkg/simple/client/oci ./pkg/simple/client/application ./pkg/controller/application ./pkg/kapis/application/v2 -count=1
```

Expected: PASS.

- [ ] **Step 3: Run the wider suite and classify environment-only failures**

Run:

```bash
go test ./... -count=1
```

Expected: PASS where the local environment supplies required dependencies. If the known missing kubebuilder etcd, external registry timeout, or e2e endpoint failure recurs, record it separately from code failures; do not change unrelated code to hide it.

- [ ] **Step 4: Build and deploy the controller-manager image only after tests pass**

Use the existing manual GitHub Action workflow with a branch-specific source ref and a unique OCI multi-chart image tag. Do not push/merge `master`. Update only `ks-controller-manager` in the test cluster because this change is controller-side.

- [ ] **Step 5: Perform three acceptance checks in the test cluster**

1. Existing HTTPS Helm Repo continues to list and deploy.
2. Existing single OCI Chart Repo lists all valid versions and deploys with credentials.
3. A Harbor Project Repo with at least three Helm repositories creates one Repo, at least three Applications, correct metadata, multiple versions, and no Docker-image Application.

Trigger a follow-up sync after adding a version or use fixtures to confirm changed digest updates metadata without duplicating versions.

- [ ] **Step 6: Commit only implementation corrections from verification**

```bash
git add <only-the-files-fixed-by-verification>
git commit -m "fix: complete OCI multi-chart sync verification"
```

Do not create this commit if verification requires no source changes.

## Plan self-review

Coverage: Task 1 provides digest support; Task 2 covers discovery, Harbor and catalog fallback; Task 3 provides Helm artifact recognition/metadata; Task 4 maps results to current CR reconciliation and failures/deletions; Task 5 covers API validation and deployment compatibility; Task 6 covers formatting, regressions and cluster acceptance.

The plan intentionally does not modify CRDs or frontend in the first iteration because automatic URL detection is the approved first-phase scope. If a real registry demonstrates ambiguous auto-detection, stop after Task 2 and create a follow-up spec for optional `spec.oci.mode/provider` rather than silently changing the API.
