/*
 * Copyright 2024 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 * https://github.com/kubesphere/kubesphere/blob/master/LICENSE
 */

package v2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/emicklei/go-restful/v3"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/registry"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	appv2 "kubesphere.io/api/application/v2"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"kubesphere.io/kubesphere/pkg/constants"
)

type conflictOnceManualSyncPatchClient struct {
	runtimeclient.Client
	conflicted     atomic.Bool
	rewriteURL     string
	returnConflict bool
}

func (c *conflictOnceManualSyncPatchClient) Patch(ctx context.Context, obj runtimeclient.Object, patch runtimeclient.Patch, opts ...runtimeclient.PatchOption) error {
	if _, isRepo := obj.(*appv2.Repo); isRepo && c.conflicted.CompareAndSwap(false, true) {
		if c.rewriteURL != "" {
			current := &appv2.Repo{}
			if err := c.Client.Get(ctx, runtimeclient.ObjectKeyFromObject(obj), current); err != nil {
				return err
			}
			current.Spec.Url = c.rewriteURL
			if err := c.Client.Update(ctx, current); err != nil {
				return err
			}
		}
		if c.returnConflict {
			return apierrors.NewConflict(schema.GroupResource{Group: appv2.SchemeGroupVersion.Group, Resource: "repos"}, obj.GetName(), fmt.Errorf("injected conflict"))
		}
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

type failingManualSyncStatusClient struct{ runtimeclient.Client }

func (c *failingManualSyncStatusClient) Status() runtimeclient.SubResourceWriter {
	return failingManualSyncStatusWriter{SubResourceWriter: c.Client.Status()}
}

type failingManualSyncStatusWriter struct {
	runtimeclient.SubResourceWriter
}

func (failingManualSyncStatusWriter) Update(context.Context, runtimeclient.Object, ...runtimeclient.SubResourceUpdateOption) error {
	return fmt.Errorf("injected status update failure")
}

func TestCreateRepoDoesNotValidateIndex(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	h := &appHandler{client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	ws := new(restful.WebService)
	ws.Path("/").Consumes(restful.MIME_JSON).Produces(restful.MIME_JSON)
	ws.Route(ws.POST("/repos").To(h.CreateOrUpdateRepo))
	container := restful.NewContainer()
	container.Add(ws)

	body, err := json.Marshal(&appv2.Repo{
		ObjectMeta: metav1.ObjectMeta{Name: "unreachable-oci-repo"},
		Spec:       appv2.RepoSpec{Url: "oci://127.0.0.1:1/charts"},
	})
	if err != nil {
		t.Fatalf("marshal repo: %v", err)
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/repos", bytes.NewReader(body))
	req.Header.Set("Content-Type", restful.MIME_JSON)
	container.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create repo status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	repo := &appv2.Repo{}
	if err := h.client.Get(context.Background(), runtimeclient.ObjectKey{Name: "unreachable-oci-repo"}, repo); err != nil {
		t.Fatalf("get created repo: %v", err)
	}
}

func TestCreateOCIRepoStripsURLUserinfoBeforePersistence(t *testing.T) {
	const username = "fixture-user"
	const password = "fixture-pass"
	scheme := runtime.NewScheme()
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	h := &appHandler{client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	ws := new(restful.WebService)
	ws.Path("/").Consumes(restful.MIME_JSON).Produces(restful.MIME_JSON)
	ws.Route(ws.POST("/repos").To(h.CreateOrUpdateRepo))
	container := restful.NewContainer()
	container.Add(ws)

	body, err := json.Marshal(&appv2.Repo{
		ObjectMeta: metav1.ObjectMeta{Name: "userinfo-oci-repo"},
		Spec:       appv2.RepoSpec{Url: "oci://" + username + ":" + password + "@registry.example.invalid/charts/demo"},
	})
	if err != nil {
		t.Fatalf("marshal repo: %v", err)
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/repos", bytes.NewReader(body))
	req.Header.Set("Content-Type", restful.MIME_JSON)
	container.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create repo status = %d, want %d", recorder.Code, http.StatusOK)
	}

	repo := &appv2.Repo{}
	if err := h.client.Get(context.Background(), runtimeclient.ObjectKey{Name: "userinfo-oci-repo"}, repo); err != nil {
		t.Fatalf("get created repo: %v", err)
	}
	if repo.Spec.Url != "oci://registry.example.invalid/charts/demo" {
		t.Fatal("persisted OCI repository URL still contains userinfo")
	}
	if repo.Spec.Credential.Username != username || repo.Spec.Credential.Password != password {
		t.Fatal("persisted OCI repository credential does not match URL userinfo")
	}
}

func TestCreateHTTPRepoValidatesIndex(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	h := &appHandler{client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	ws := new(restful.WebService)
	ws.Path("/").Consumes(restful.MIME_JSON).Produces(restful.MIME_JSON)
	ws.Route(ws.POST("/repos").To(h.CreateOrUpdateRepo))
	container := restful.NewContainer()
	container.Add(ws)

	body, err := json.Marshal(&appv2.Repo{
		ObjectMeta: metav1.ObjectMeta{Name: "unreachable-http-repo"},
		Spec:       appv2.RepoSpec{Url: "http://127.0.0.1:1/charts"},
	})
	if err != nil {
		t.Fatalf("marshal repo: %v", err)
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/repos", bytes.NewReader(body))
	req.Header.Set("Content-Type", restful.MIME_JSON)
	container.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("create repo status = %d, want %d: %s", recorder.Code, http.StatusInternalServerError, recorder.Body.String())
	}

	repo := &appv2.Repo{}
	err = h.client.Get(context.Background(), runtimeclient.ObjectKey{Name: "unreachable-http-repo"}, repo)
	if err == nil {
		t.Fatal("HTTP repository was persisted without a successful index validation")
	}
}

func TestValidateHTTPRepoLoadsIndexWithoutOCIRequests(t *testing.T) {
	indexRequests := 0
	ociRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/charts/index.yaml":
			indexRequests++
			_, _ = w.Write([]byte("apiVersion: v1\nentries: {}\n"))
		default:
			if r.URL.Path == "/v2" || strings.HasPrefix(r.URL.Path, "/v2/") {
				ociRequests++
			}
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	scheme := runtime.NewScheme()
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	h := &appHandler{client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	ws := new(restful.WebService)
	ws.Path("/").Consumes(restful.MIME_JSON).Produces(restful.MIME_JSON)
	ws.Route(ws.POST("/repos").To(h.CreateOrUpdateRepo))
	container := restful.NewContainer()
	container.Add(ws)

	body, err := json.Marshal(&appv2.Repo{
		ObjectMeta: metav1.ObjectMeta{Name: "validated-http-repo"},
		Spec:       appv2.RepoSpec{Url: server.URL + "/charts"},
	})
	if err != nil {
		t.Fatalf("marshal repo: %v", err)
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/repos?validate=1", bytes.NewReader(body))
	req.Header.Set("Content-Type", restful.MIME_JSON)
	container.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("validate repo status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if indexRequests != 1 {
		t.Fatalf("index requests = %d, want 1", indexRequests)
	}
	if ociRequests != 0 {
		t.Fatalf("OCI requests = %d, want 0", ociRequests)
	}
}

func TestValidateOCIRepoReadsHelmMetadata(t *testing.T) {
	metadata, err := json.Marshal(chart.Metadata{APIVersion: "v2", Name: "demo", Version: "2.0.0"})
	if err != nil {
		t.Fatalf("marshal chart metadata: %v", err)
	}
	configDigest := digest.FromBytes(metadata)
	manifest, err := json.Marshal(ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    ocispec.Descriptor{MediaType: registry.ConfigMediaType, Digest: configDigest, Size: int64(len(metadata))},
		Layers:    []ocispec.Descriptor{{MediaType: registry.ChartLayerMediaType, Digest: digest.FromString("chart"), Size: 5}},
	})
	if err != nil {
		t.Fatalf("marshal OCI manifest: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/", "/v2":
			w.WriteHeader(http.StatusOK)
		case "/v2/charts/demo/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0", "2.0.0"}})
		case "/v2/charts/demo/manifests/1.0.0", "/v2/charts/demo/manifests/2.0.0":
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			_, _ = w.Write(manifest)
		case "/v2/charts/demo/blobs/" + configDigest.String():
			_, _ = w.Write(metadata)
		default:
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	scheme := runtime.NewScheme()
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	h := &appHandler{client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	ws := new(restful.WebService)
	ws.Path("/").Consumes(restful.MIME_JSON).Produces(restful.MIME_JSON)
	ws.Route(ws.POST("/repos").To(h.CreateOrUpdateRepo))
	container := restful.NewContainer()
	container.Add(ws)
	body, err := json.Marshal(&appv2.Repo{ObjectMeta: metav1.ObjectMeta{Name: "oci-repo"}, Spec: appv2.RepoSpec{Url: "oci://" + server.Listener.Addr().String() + "/charts/demo", Credential: appv2.RepoCredential{PlainHTTP: true}}})
	if err != nil {
		t.Fatalf("marshal repo: %v", err)
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/repos?validate=true", bytes.NewReader(body))
	req.Header.Set("Content-Type", restful.MIME_JSON)
	container.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("validate repo status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	repo := &appv2.Repo{}
	if err := h.client.Get(context.Background(), runtimeclient.ObjectKey{Name: "oci-repo"}, repo); err == nil {
		t.Fatal("OCI repository was persisted during validation")
	}
}

func TestValidateOCIRepoChecksAllTags(t *testing.T) {
	metadata, err := json.Marshal(chart.Metadata{APIVersion: "v2", Name: "demo", Version: "3.0.0"})
	if err != nil {
		t.Fatalf("marshal chart metadata: %v", err)
	}
	configDigest := digest.FromBytes(metadata)
	manifest, err := json.Marshal(ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    ocispec.Descriptor{MediaType: registry.ConfigMediaType, Digest: configDigest, Size: int64(len(metadata))},
		Layers:    []ocispec.Descriptor{{MediaType: registry.ChartLayerMediaType, Digest: digest.FromString("chart"), Size: 5}},
	})
	if err != nil {
		t.Fatalf("marshal OCI manifest: %v", err)
	}
	var manifestRequests atomic.Int32
	var configRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/", "/v2":
			w.WriteHeader(http.StatusOK)
		case "/v2/charts/demo/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0", "2.0.0", "3.0.0"}})
		case "/v2/charts/demo/manifests/1.0.0", "/v2/charts/demo/manifests/2.0.0", "/v2/charts/demo/manifests/3.0.0":
			manifestRequests.Add(1)
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			_, _ = w.Write(manifest)
		case "/v2/charts/demo/blobs/" + configDigest.String():
			configRequests.Add(1)
			_, _ = w.Write(metadata)
		default:
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	scheme := runtime.NewScheme()
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	h := &appHandler{client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	ws := new(restful.WebService)
	ws.Path("/").Consumes(restful.MIME_JSON).Produces(restful.MIME_JSON)
	ws.Route(ws.POST("/repos").To(h.CreateOrUpdateRepo))
	container := restful.NewContainer()
	container.Add(ws)
	body, err := json.Marshal(&appv2.Repo{ObjectMeta: metav1.ObjectMeta{Name: "fast-validated-oci-repo"}, Spec: appv2.RepoSpec{Url: "oci://" + server.Listener.Addr().String() + "/charts/demo", Credential: appv2.RepoCredential{PlainHTTP: true}}})
	if err != nil {
		t.Fatalf("marshal repo: %v", err)
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/repos?validate=true", bytes.NewReader(body))
	req.Header.Set("Content-Type", restful.MIME_JSON)
	container.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("validate repo status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	repo := &appv2.Repo{}
	if err := h.client.Get(context.Background(), runtimeclient.ObjectKey{Name: "fast-validated-oci-repo"}, repo); err == nil {
		t.Fatal("OCI repository was persisted during validation")
	}
	if manifestRequests.Load() != 3 || configRequests.Load() != 3 {
		t.Fatalf("got %d manifest and %d config requests, want 3 each", manifestRequests.Load(), configRequests.Load())
	}
}

func TestValidateOCIRepoRejectsDockerImage(t *testing.T) {
	manifest, err := json.Marshal(ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig, Digest: digest.FromString("image-config"), Size: 2},
		Layers:    []ocispec.Descriptor{{MediaType: ocispec.MediaTypeImageLayer, Digest: digest.FromString("image-layer"), Size: 5}},
	})
	if err != nil {
		t.Fatalf("marshal OCI image manifest: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/", "/v2":
			w.WriteHeader(http.StatusOK)
		case "/v2/charts/demo/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0", "2.0.0"}})
		case "/v2/charts/demo/manifests/1.0.0", "/v2/charts/demo/manifests/2.0.0":
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			_, _ = w.Write(manifest)
		default:
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	scheme := runtime.NewScheme()
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	h := &appHandler{client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	ws := new(restful.WebService)
	ws.Path("/").Consumes(restful.MIME_JSON).Produces(restful.MIME_JSON)
	ws.Route(ws.POST("/repos").To(h.CreateOrUpdateRepo))
	container := restful.NewContainer()
	container.Add(ws)
	body, err := json.Marshal(&appv2.Repo{ObjectMeta: metav1.ObjectMeta{Name: "image-repo"}, Spec: appv2.RepoSpec{Url: "oci://" + server.Listener.Addr().String() + "/charts/demo", Credential: appv2.RepoCredential{PlainHTTP: true}}})
	if err != nil {
		t.Fatalf("marshal repo: %v", err)
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/repos?validate=true", bytes.NewReader(body))
	req.Header.Set("Content-Type", restful.MIME_JSON)
	container.ServeHTTP(recorder, req)
	if recorder.Code == http.StatusOK {
		t.Fatalf("validate Docker image repo status = %d, want non-%d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	repo := &appv2.Repo{}
	if err := h.client.Get(context.Background(), runtimeclient.ObjectKey{Name: "image-repo"}, repo); err == nil {
		t.Fatal("Docker image repository was persisted during validation")
	}
}

func TestValidateOCIRepoFailureDoesNotExposeURLUserinfo(t *testing.T) {
	const username = "fixture-user"
	const password = "fixture-pass"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/charts/demo/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0"}})
		case "/v2/charts/demo/manifests/1.0.0":
			_ = json.NewEncoder(w).Encode(ocispec.Manifest{Config: ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	scheme := runtime.NewScheme()
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	h := &appHandler{client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	ws := new(restful.WebService)
	ws.Path("/").Consumes(restful.MIME_JSON).Produces(restful.MIME_JSON)
	ws.Route(ws.POST("/repos").To(h.CreateOrUpdateRepo))
	container := restful.NewContainer()
	container.Add(ws)
	body, err := json.Marshal(&appv2.Repo{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid-userinfo-oci-repo"},
		Spec: appv2.RepoSpec{
			Url:        "oci://" + username + ":" + password + "@" + server.Listener.Addr().String() + "/charts/demo",
			Credential: appv2.RepoCredential{PlainHTTP: true},
		},
	})
	if err != nil {
		t.Fatalf("marshal repo: %v", err)
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/repos?validate=true", bytes.NewReader(body))
	req.Header.Set("Content-Type", restful.MIME_JSON)
	container.ServeHTTP(recorder, req)
	if recorder.Code == http.StatusOK {
		t.Fatal("validation status = 200, want failure")
	}
	response := recorder.Body.String()
	if strings.Contains(response, username) || strings.Contains(response, password) {
		t.Fatal("validation response exposes OCI URL userinfo")
	}
}

func TestManualSyncTriggersRepoUpdateAndSetsStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	repo := &appv2.Repo{
		ObjectMeta: metav1.ObjectMeta{Name: "manual-sync-repo"},
		Status:     appv2.RepoStatus{State: appv2.StatusSuccessful},
	}
	h := &appHandler{client: fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(repo).WithObjects(repo).Build()}
	ws := new(restful.WebService)
	ws.Path("/").Consumes(restful.MIME_JSON).Produces(restful.MIME_JSON)
	ws.Route(ws.POST("/repos/{repo}/action").To(h.ManualSync))
	container := restful.NewContainer()
	container.Add(ws)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/repos/manual-sync-repo/action", nil)
	req.Header.Set("Content-Type", restful.MIME_JSON)
	container.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("manual sync status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	updated := &appv2.Repo{}
	if err := h.client.Get(context.Background(), runtimeclient.ObjectKey{Name: repo.Name}, updated); err != nil {
		t.Fatalf("get repo after manual sync: %v", err)
	}
	if updated.Status.State != appv2.StatusManualTrigger {
		t.Fatalf("repo status = %q, want %q", updated.Status.State, appv2.StatusManualTrigger)
	}
	if updated.Annotations[appv2.ManualSyncTriggerAnnotation] == "" {
		t.Fatal("manual sync did not update the controller trigger annotation")
	}
}

func TestManualSyncAcceptsRequestWhenStatusUpdateFails(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	repo := &appv2.Repo{ObjectMeta: metav1.ObjectMeta{Name: "manual-sync-status-failure"}, Status: appv2.RepoStatus{State: appv2.StatusSuccessful}}
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(repo).WithObjects(repo).Build()
	h := &appHandler{client: &failingManualSyncStatusClient{Client: base}}
	ws := new(restful.WebService)
	ws.Path("/").Consumes(restful.MIME_JSON).Produces(restful.MIME_JSON)
	ws.Route(ws.POST("/repos/{repo}/action").To(h.ManualSync))
	container := restful.NewContainer()
	container.Add(ws)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/repos/manual-sync-status-failure/action", nil)
	req.Header.Set("Content-Type", restful.MIME_JSON)
	container.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("manual sync status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	updated := &appv2.Repo{}
	if err := base.Get(context.Background(), runtimeclient.ObjectKey{Name: repo.Name}, updated); err != nil {
		t.Fatalf("get repo after manual sync: %v", err)
	}
	if updated.Annotations[appv2.ManualSyncTriggerAnnotation] == "" {
		t.Fatal("manual sync marker was not persisted")
	}
}

func TestManualSyncRejectsFullRefreshWhenRepoChangesToHTTPDuringPatchRetry(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	repo := &appv2.Repo{
		ObjectMeta: metav1.ObjectMeta{Name: "rewritten-manual-sync"},
		Spec:       appv2.RepoSpec{Url: "oci://registry.example.invalid/charts"},
		Status:     appv2.RepoStatus{State: appv2.StatusSuccessful},
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(repo).WithObjects(repo).Build()
	h := &appHandler{client: &conflictOnceManualSyncPatchClient{Client: base, rewriteURL: "https://charts.example.invalid"}}
	ws := new(restful.WebService)
	ws.Path("/").Consumes(restful.MIME_JSON).Produces(restful.MIME_JSON)
	ws.Route(ws.POST("/repos/{repo}/action").To(h.ManualSync))
	container := restful.NewContainer()
	container.Add(ws)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/repos/rewritten-manual-sync/action?mode=full", nil)
	req.Header.Set("Content-Type", restful.MIME_JSON)
	container.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("manual sync status = %d, want %d: %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	updated := &appv2.Repo{}
	if err := base.Get(context.Background(), runtimeclient.ObjectKey{Name: repo.Name}, updated); err != nil {
		t.Fatalf("get repo after manual sync: %v", err)
	}
	if updated.Annotations[appv2.ManualSyncTriggerAnnotation] != "" || updated.Annotations[appv2.FullRefreshTriggerAnnotation] != "" {
		t.Fatalf("repo annotations = %#v, want no sync markers", updated.Annotations)
	}
}

func TestManualSyncRetriesMetadataPatchConflict(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	repo := &appv2.Repo{
		ObjectMeta: metav1.ObjectMeta{Name: "conflicted-manual-sync"},
		Spec:       appv2.RepoSpec{Url: "oci://registry.example.invalid/charts"},
		Status:     appv2.RepoStatus{State: appv2.StatusSuccessful},
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(repo).WithObjects(repo).Build()
	h := &appHandler{client: &conflictOnceManualSyncPatchClient{Client: base, returnConflict: true}}
	ws := new(restful.WebService)
	ws.Path("/").Consumes(restful.MIME_JSON).Produces(restful.MIME_JSON)
	ws.Route(ws.POST("/repos/{repo}/action").To(h.ManualSync))
	container := restful.NewContainer()
	container.Add(ws)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/repos/conflicted-manual-sync/action?mode=full", nil)
	req.Header.Set("Content-Type", restful.MIME_JSON)
	container.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("manual sync status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	updated := &appv2.Repo{}
	if err := base.Get(context.Background(), runtimeclient.ObjectKey{Name: repo.Name}, updated); err != nil {
		t.Fatalf("get repo after manual sync: %v", err)
	}
	if updated.Annotations[appv2.ManualSyncTriggerAnnotation] == "" || updated.Annotations[appv2.FullRefreshTriggerAnnotation] == "" {
		t.Fatalf("full refresh triggers = %#v, want both markers", updated.Annotations)
	}
}

func TestManualSyncModes(t *testing.T) {
	tests := []struct {
		name          string
		url           string
		mode          string
		wantStatus    int
		wantFull      bool
		wantUnchanged bool
	}{
		{name: "default OCI", url: "oci://registry.example.invalid/charts", wantStatus: http.StatusOK},
		{name: "incremental OCI", url: "oci://registry.example.invalid/charts", mode: "incremental", wantStatus: http.StatusOK},
		{name: "full OCI", url: "oci://registry.example.invalid/charts", mode: "full", wantStatus: http.StatusOK, wantFull: true},
		{name: "full HTTPS", url: "https://charts.example.invalid", mode: "full", wantStatus: http.StatusBadRequest, wantUnchanged: true},
		{name: "unknown mode", url: "oci://registry.example.invalid/charts", mode: "reset", wantStatus: http.StatusBadRequest, wantUnchanged: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := appv2.AddToScheme(scheme); err != nil {
				t.Fatalf("AddToScheme() error = %v", err)
			}
			repo := &appv2.Repo{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "manual-sync-mode-repo",
					Annotations: map[string]string{"existing": "annotation"},
				},
				Spec:   appv2.RepoSpec{Url: tt.url},
				Status: appv2.RepoStatus{State: appv2.StatusSuccessful},
			}
			h := &appHandler{client: fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(repo).WithObjects(repo).Build()}
			ws := new(restful.WebService)
			ws.Path("/").Consumes(restful.MIME_JSON).Produces(restful.MIME_JSON)
			ws.Route(ws.POST("/repos/{repo}/action").To(h.ManualSync))
			container := restful.NewContainer()
			container.Add(ws)

			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/repos/manual-sync-mode-repo/action?mode="+tt.mode, nil)
			req.Header.Set("Content-Type", restful.MIME_JSON)
			container.ServeHTTP(recorder, req)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("manual sync status = %d, want %d: %s", recorder.Code, tt.wantStatus, recorder.Body.String())
			}

			updated := &appv2.Repo{}
			if err := h.client.Get(context.Background(), runtimeclient.ObjectKey{Name: repo.Name}, updated); err != nil {
				t.Fatalf("get repo after manual sync: %v", err)
			}
			if tt.wantUnchanged {
				if updated.Status.State != appv2.StatusSuccessful {
					t.Fatalf("repo status = %q, want unchanged %q", updated.Status.State, appv2.StatusSuccessful)
				}
				if len(updated.Annotations) != 1 || updated.Annotations["existing"] != "annotation" {
					t.Fatalf("repo annotations = %#v, want unchanged", updated.Annotations)
				}
				return
			}
			if updated.Status.State != appv2.StatusManualTrigger {
				t.Fatalf("repo status = %q, want %q", updated.Status.State, appv2.StatusManualTrigger)
			}
			if updated.Annotations[appv2.ManualSyncTriggerAnnotation] == "" {
				t.Fatal("manual sync trigger annotation is empty")
			}
			if got := updated.Annotations[appv2.FullRefreshTriggerAnnotation]; (got != "") != tt.wantFull {
				t.Fatalf("full refresh trigger annotation = %q, want full=%t", got, tt.wantFull)
			}
			if tt.wantFull && updated.Annotations[appv2.ManualSyncTriggerAnnotation] != updated.Annotations[appv2.FullRefreshTriggerAnnotation] {
				t.Fatalf("manual and full refresh triggers were not written atomically: %#v", updated.Annotations)
			}
		})
	}
}

func TestLoadRepoCredentialSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	insecure := false
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: constants.KubeSphereNamespace,
			Name:      "repo-cred",
		},
		Data: map[string][]byte{
			"username":              []byte("admin"),
			"password":              []byte("password"),
			"certFile":              []byte("/etc/certs/tls.crt"),
			"keyFile":               []byte("/etc/certs/tls.key"),
			"caFile":                []byte("/etc/certs/ca.crt"),
			"insecureSkipTLSVerify": []byte("true"),
			"plainHTTP":             []byte("true"),
		},
	}
	h := &appHandler{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()}
	credential := appv2.RepoCredential{
		Username:              "old-user",
		Password:              "old-password",
		InsecureSkipTLSVerify: &insecure,
	}

	err := h.loadRepoCredentialSecret(context.Background(), &corev1.SecretReference{Name: "repo-cred"}, &credential)
	if err != nil {
		t.Fatalf("loadRepoCredentialSecret() error = %v", err)
	}

	if credential.Username != "admin" || credential.Password != "password" {
		t.Fatalf("unexpected basic auth: %#v", credential)
	}
	if credential.CertFile != "/etc/certs/tls.crt" || credential.KeyFile != "/etc/certs/tls.key" || credential.CAFile != "/etc/certs/ca.crt" {
		t.Fatalf("unexpected tls config: %#v", credential)
	}
	if credential.InsecureSkipTLSVerify == nil || !*credential.InsecureSkipTLSVerify {
		t.Fatalf("expected insecureSkipTLSVerify=true, got %#v", credential.InsecureSkipTLSVerify)
	}
	if !credential.PlainHTTP {
		t.Fatal("expected plainHTTP=true")
	}
}

func TestLoadRepoCredentialSecretInvalidBool(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "repo-secrets",
			Name:      "repo-cred",
		},
		Data: map[string][]byte{"insecureSkipTLSVerify": []byte("sometimes")},
	}
	h := &appHandler{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()}

	err := h.loadRepoCredentialSecret(context.Background(), &corev1.SecretReference{Namespace: "repo-secrets", Name: "repo-cred"}, &appv2.RepoCredential{})
	if err == nil {
		t.Fatal("expected invalid boolean error")
	}
}
