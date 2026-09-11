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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/emicklei/go-restful/v3"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/registry"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	appv2 "kubesphere.io/api/application/v2"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"kubesphere.io/kubesphere/pkg/constants"
)

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

func TestValidateOCIRepoReadsOnlyNewestChartMetadata(t *testing.T) {
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
		case "/v2/charts/demo/manifests/2.0.0":
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
