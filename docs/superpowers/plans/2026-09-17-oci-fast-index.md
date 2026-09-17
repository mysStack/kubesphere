# OCI 快速全量索引 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让直接 OCI Helm Chart 仓库首次和日常同步不再逐个探测全部历史 tag，避免 Docker Hub 配额耗尽。

**Architecture:** 直接 OCI repository 首次同步只严格检查最高语义化 tag，并用其 Helm metadata 创建全部历史版本。后续同步复用已有版本，只有新 tag 才检查 manifest/config；Harbor Project 与 catalog 多 Chart discovery 保持现有严格检查。

**Tech Stack:** Go、Helm v3 registry、ORAS remote registry、Kubernetes controller-runtime、Go `httptest`。

**Spec:** `docs/superpowers/specs/2026-09-16-oci-fast-index-design.md`

## Global Constraints

- 仅直接 OCI repository URL 使用快速索引；多 Chart discovery 和 HTTP Repo 行为不变。
- 首次快速同步必须严格验证最高 SemVer tag 是 Helm OCI Chart。
- 历史版本保存其原始 tag 的 `oci://…:<tag>` pull URL；部署时才下载 `.tgz`。
- 不新增 CRD 字段、ConfigMap、数据库或依赖；不泄漏任何凭据或 token。
- 不暂存或提交用户已有的 `.gitignore` 改动。

---

### Task 1: 实现直接 OCI 首次快速全量索引

**Files:**
- Modify: `pkg/simple/client/application/oci.go:173-239`
- Test: `pkg/simple/client/application/oci_test.go`

**Interfaces:**
- Consumes: `LoadOCIRepoIndexWithCache(ctx, u, cred, cached OCIChartVersionCache)`。
- Produces: `semanticOCITags(tags []string) []string`、`highestOCITag(tags []string) (string, bool)`、`cloneChartVersionForTag(source *helmrepo.ChartVersion, tag string) *helmrepo.ChartVersion`。

- [ ] **Step 1: 写失败测试**

添加 `TestLoadOCIRepoIndexWithCacheDirectRepoUsesLatestMetadataForAllTags`。registry server 返回 tags `1.0.0`、`1.2.0`、`2.0.0`、`2.0.0-metadata`、`not-a-version`，并且只允许 `2.0.0` 的 manifest/config 请求。断言 index 中 `demo` 有三个版本，URL 分别是各原始 tag，manifest/config 请求各为一次，只有最高 tag 有 digest。

```go
if manifestRequests != 1 || configRequests != 1 {
	t.Fatalf("got %d manifest and %d config requests, want 1 each", manifestRequests, configRequests)
}
```

- [ ] **Step 2: 验证测试先失败**

Run: `go test ./pkg/simple/client/application -run '^TestLoadOCIRepoIndexWithCacheDirectRepoUsesLatestMetadataForAllTags$' -count=1`

Expected: FAIL，因为旧逻辑会读取较低 tag 的 manifest。

- [ ] **Step 3: 写最小实现**

当 `DiscoverOCIRepositories` 返回一个直接 repository 且 cache 为空时：过滤辅助/非 SemVer tag，选择最高 tag 并调用一次 `inspectOCIChart`；克隆该 metadata 到所有 tag，按 tag 设置 pull URL；仅最高 tag 保留 digest。多 repository 继续当前循环和逐 artifact 严格检查。

- [ ] **Step 4: 验证测试通过并提交**

Run:
```bash
go test ./pkg/simple/client/application -run '^TestLoadOCIRepoIndexWithCacheDirectRepoUsesLatestMetadataForAllTags$' -count=1
go test ./pkg/simple/client/application -count=1
```

Commit:
```bash
git add pkg/simple/client/application/oci.go pkg/simple/client/application/oci_test.go
git commit -m "feat: index direct OCI charts without per-tag probes"
```

### Task 2: 增量同步只检查新增 OCI tag

**Files:**
- Modify: `pkg/simple/client/application/oci.go:96-148,173-239`
- Test: `pkg/simple/client/application/oci_test.go`
- Test: `pkg/controller/application/helm_repo_controller_test.go`

**Interfaces:**
- Consumes: `OCIChartVersionCache`（由 `BuildOCIChartVersionCache` 从现有 ApplicationVersion pull URL 创建）。
- Produces: 直接 repository 的缓存 tag 直接写入 index，只有缺失 tag 调用 `inspectOCIChart`。

- [ ] **Step 1: 写失败测试**

添加 `TestLoadOCIRepoIndexWithCacheDirectRepoInspectsOnlyNewTags`：cache 包含 `1.0.0`、`1.2.0`；registry 返回它们及新 tag `2.0.0`。server 只允许新 tag 的 manifest/config。断言三版本均在 index，旧 tag digest 保留，网络请求各一次。

添加 `TestRepoReconcilerDirectOCIReusesExistingVersionsWithoutManifestRequests`：第一次 reconcile 创建版本；第二次相同 tags reconcile，manifest/config 请求数不增加。

- [ ] **Step 2: 验证测试先失败**

Run:
```bash
go test ./pkg/simple/client/application -run '^TestLoadOCIRepoIndexWithCacheDirectRepoInspectsOnlyNewTags$' -count=1
go test ./pkg/controller/application -run '^TestRepoReconcilerDirectOCIReusesExistingVersionsWithoutManifestRequests$' -count=1
```

Expected: 至少一个 FAIL，因为旧 tag 仍被探测。

- [ ] **Step 3: 写最小实现**

直接 repository 的每个 tag 先查询 `cached[ociCacheKey(host, repository, tag)]`。存在时 clone cache 版本并不访问 registry；不存在时调用 `inspectOCIChart`。新 tag 的失败写 OCIIndexWarning，保留现有 Application/ApplicationVersion，不能触发删除。

- [ ] **Step 4: 验证通过并提交**

Run:
```bash
go test ./pkg/simple/client/application -run '^TestLoadOCIRepoIndexWithCacheDirectRepoInspectsOnlyNewTags$' -count=1
go test ./pkg/controller/application -run '^TestRepoReconcilerDirectOCIReusesExistingVersionsWithoutManifestRequests$' -count=1
go test ./pkg/simple/client/application ./pkg/controller/application -count=1
```

Commit:
```bash
git add pkg/simple/client/application/oci.go pkg/simple/client/application/oci_test.go pkg/controller/application/helm_repo_controller_test.go
git commit -m "feat: reuse OCI metadata for existing tags"
```

### Task 3: 快速 OCI 表单验证与兼容性回归

**Files:**
- Modify: `pkg/simple/client/application/oci.go:66-88`
- Test: `pkg/kapis/application/v2/handler_repo_test.go`

**Interfaces:**
- Consumes: `ValidateOCIRepository(u, cred)`。
- Produces: 直接 OCI URL 验证只读取最高候选 tag 的 manifest/config，且不创建 Repo。

- [ ] **Step 1: 写失败 API 测试**

添加 `TestValidateOCIRepoChecksOnlyHighestTag`。以 `POST /repos?validate=true` 传入有三个 SemVer tags 的直接 OCI URL；server 只接受最高 tag 的 manifest/config。断言 HTTP 200、Repo 未持久化、manifest/config 请求均为一次。

- [ ] **Step 2: 验证测试先失败**

Run: `go test ./pkg/kapis/application/v2 -run '^TestValidateOCIRepoChecksOnlyHighestTag$' -count=1`

Expected: FAIL，因为当前验证会读取较低 tag 的 manifest。

- [ ] **Step 3: 复用快速索引完成验证**

保持 `handler_repo.go` 的 HTTP/OCI 分流不变。令 `ValidateOCIRepository` 使用 Task 1 的直接 repository 快速索引；最高 tag 是 Docker image 时返回错误；多 Chart discovery 仍严格检查。

- [ ] **Step 4: 完整验证并提交**

Run:
```bash
go test ./pkg/kapis/application/v2 -run '^TestValidateOCIRepoChecksOnlyHighestTag$' -count=1
go test ./pkg/simple/client/oci ./pkg/simple/client/application ./pkg/controller/application ./pkg/kapis/application/v2 -count=1
git diff --check
```

Commit:
```bash
git add pkg/simple/client/application/oci.go pkg/simple/client/application/oci_test.go pkg/kapis/application/v2/handler_repo_test.go
git commit -m "feat: validate OCI repositories with one chart probe"
```

