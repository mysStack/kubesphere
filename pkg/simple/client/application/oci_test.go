/*
 * Copyright 2024 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 * https://github.com/kubesphere/kubesphere/blob/master/LICENSE
 */

package application

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/registry"
	helmrepo "helm.sh/helm/v3/pkg/repo"
	"k8s.io/utils/ptr"
	appv2 "kubesphere.io/api/application/v2"
)

func writeTestCertificate(t *testing.T, filename string, der []byte) string {
	t.Helper()
	path := t.TempDir() + "/" + filename
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatalf("write test certificate: %v", err)
	}
	return path
}

func newTestClientCertificate(t *testing.T) (*x509.CertPool, string, string) {
	t.Helper()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test client CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	clientKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	clientTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "registry client"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, ca, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create client certificate: %v", err)
	}
	certPath := writeTestCertificate(t, "client.crt", clientDER)
	keyPath := t.TempDir() + "/client.key"
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(clientKey)}), 0600); err != nil {
		t.Fatalf("write client key: %v", err)
	}
	clientCAs := x509.NewCertPool()
	clientCAs.AddCert(ca)
	return clientCAs, certPath, keyPath
}

func TestDiscoverOCIRepositoriesCatalog(t *testing.T) {
	testRepos := []string{"helmcharts/nginx", "helmcharts/test-api", "helmcharts/test-ui", "helmcharts/demo-app"}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && (r.URL.Path == "/v2" || r.URL.Path == "/v2/") {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v2/_catalog" {
			result := struct {
				Repositories []string `json:"repositories"`
			}{
				Repositories: testRepos,
			}
			if err := json.NewEncoder(w).Encode(result); err != nil {
				t.Errorf("failed to write response: %v", err)
			}
			return
		}
		t.Logf("unexpected access: %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	serverURL, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("invalid test http server: %v", err)
	}

	cred := appv2.RepoCredential{PlainHTTP: true}
	repos, err := DiscoverOCIRepositories(context.Background(), serverURL, cred)
	if err != nil {
		t.Fatalf("DiscoverOCIRepositories() error: %s", err)
	}
	if len(repos) != len(testRepos) {
		t.Fatalf("expected %d repos, got %d", len(testRepos), len(repos))
	}
}

func TestOCIRegistryAllowsSlowRegistryResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/" && r.URL.Path != "/v2" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		time.Sleep(6 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	registry, err := newOCIRegistry("oci://"+server.Listener.Addr().String(), appv2.RepoCredential{PlainHTTP: true})
	if err != nil {
		t.Fatalf("newOCIRegistry() error = %v", err)
	}
	if err := registry.Ping(context.Background()); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
}

func TestDiscoverOCIRepositoriesDirectChart(t *testing.T) {
	testRepo := "charts/demo"
	testTags := []string{"1.0.0", "1.2.0", "1.0.3"}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && (r.URL.Path == "/v2" || r.URL.Path == "/v2/") {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v2/_catalog" {
			t.Errorf("unexpected catalog access: %s", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/v2/%s/tags/list", testRepo) {
			result := struct {
				Tags []string `json:"tags"`
			}{
				Tags: testTags,
			}
			if err := json.NewEncoder(w).Encode(result); err != nil {
				t.Errorf("failed to write response: %v", err)
			}
			return
		}
		t.Logf("unexpected access: %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	serverURL, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("invalid test http server: %v", err)
	}

	ociURL := fmt.Sprintf("oci://%s/%s", serverURL.Host, testRepo)
	parsedURL, err := url.Parse(ociURL)
	if err != nil {
		t.Fatalf("invalid OCI URL: %v", err)
	}

	cred := appv2.RepoCredential{PlainHTTP: true}
	repos, err := DiscoverOCIRepositories(context.Background(), parsedURL, cred)
	if err != nil {
		t.Fatalf("DiscoverOCIRepositories() error: %s", err)
	}
	if len(repos) != 1 || repos[0] != testRepo {
		t.Fatalf("expected repos [%s], got %v", testRepo, repos)
	}
}

func TestDiscoverOCIRepositoriesFallsBackToCatalog(t *testing.T) {
	testRepos := []string{"charts/demo", "charts/demo", "charts/nested/child", "charts/traefik", "other/demo"}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && (r.URL.Path == "/v2" || r.URL.Path == "/v2/") {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v2/charts/tags/list" {
			w.WriteHeader(http.StatusNotFound)
			result := struct {
				Errors []struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"errors"`
			}{
				Errors: []struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				}{
					{Code: "NAME_UNKNOWN", Message: "repository name not known to registry"},
				},
			}
			if err := json.NewEncoder(w).Encode(result); err != nil {
				t.Errorf("failed to write response: %v", err)
			}
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v2/_catalog" {
			result := struct {
				Repositories []string `json:"repositories"`
			}{
				Repositories: testRepos,
			}
			if err := json.NewEncoder(w).Encode(result); err != nil {
				t.Errorf("failed to write response: %v", err)
			}
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/ping" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		t.Logf("unexpected access: %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	serverURL, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("invalid test http server: %v", err)
	}

	ociURL := fmt.Sprintf("oci://%s/charts", serverURL.Host)
	parsedURL, err := url.Parse(ociURL)
	if err != nil {
		t.Fatalf("invalid OCI URL: %v", err)
	}

	repos, err := DiscoverOCIRepositories(context.Background(), parsedURL, appv2.RepoCredential{PlainHTTP: true})
	if err != nil {
		t.Fatalf("DiscoverOCIRepositories() error: %s", err)
	}
	if want := []string{"charts/demo", "charts/traefik"}; !reflect.DeepEqual(repos, want) {
		t.Fatalf("repositories = %v, want %v", repos, want)
	}
}

func TestDiscoverOCIRepositoriesHarborProject(t *testing.T) {
	const project = "helm"
	const repository = "helm/traefik"
	const username = "robot"
	const password = "secret"
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/helm/tags/list":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"code": "NAME_UNKNOWN", "message": "repository name not known to registry"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/ping":
			if gotUser, gotPassword, ok := r.BasicAuth(); !ok || gotUser != username || gotPassword != password {
				t.Errorf("Harbor ping did not include the expected basic authentication")
			}
			_, _ = w.Write([]byte("Pong"))
		case r.Method == http.MethodHead && r.URL.Path == "/api/v2.0/projects":
			if got := r.URL.Query().Get("project_name"); got != project {
				t.Errorf("project_name = %q, want %q", got, project)
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/projects/helm/repositories":
			if got := r.URL.Query().Get("page_size"); got != "100" {
				t.Errorf("page_size = %q, want 100", got)
			}
			switch r.URL.Query().Get("page") {
			case "1":
				pages++
				_ = json.NewEncoder(w).Encode([]map[string]string{{"name": repository}})
			case "2":
				pages++
				_ = json.NewEncoder(w).Encode([]map[string]string{})
			default:
				t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
				w.WriteHeader(http.StatusBadRequest)
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	source, err := url.Parse(fmt.Sprintf("oci://%s/%s", server.Listener.Addr(), project))
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	repositories, err := DiscoverOCIRepositories(context.Background(), source, appv2.RepoCredential{
		Username:  username,
		Password:  password,
		PlainHTTP: true,
	})
	if err != nil {
		t.Fatalf("DiscoverOCIRepositories() error: %v", err)
	}
	if want := []string{repository}; !reflect.DeepEqual(repositories, want) {
		t.Fatalf("repositories = %v, want %v", repositories, want)
	}
	if pages != 2 {
		t.Fatalf("repository pages = %d, want 2", pages)
	}
}

func TestDiscoverOCIRepositoriesEmptyDirectTagsFallsBackToHarborProject(t *testing.T) {
	const project = "helm"
	const repository = "helm/traefik"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/helm/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/ping":
			_, _ = w.Write([]byte("Pong"))
		case r.Method == http.MethodHead && r.URL.Path == "/api/v2.0/projects":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/projects/helm/repositories":
			if r.URL.Query().Get("page") == "1" {
				_ = json.NewEncoder(w).Encode([]map[string]string{{"name": repository}})
			} else {
				_ = json.NewEncoder(w).Encode([]map[string]string{})
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	source, err := url.Parse(fmt.Sprintf("oci://%s/%s", server.Listener.Addr(), project))
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	repositories, err := DiscoverOCIRepositories(context.Background(), source, appv2.RepoCredential{PlainHTTP: true})
	if err != nil {
		t.Fatalf("DiscoverOCIRepositories() error: %v", err)
	}
	if want := []string{repository}; !reflect.DeepEqual(repositories, want) {
		t.Fatalf("repositories = %v, want %v", repositories, want)
	}
}

func TestDiscoverOCIRepositoriesUsesCustomCAForRegistryAndHarbor(t *testing.T) {
	const repository = "helm/demo"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/helm/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {}})
		case r.URL.Path == "/api/v2.0/ping":
			_, _ = w.Write([]byte("Pong"))
		case r.Method == http.MethodHead && r.URL.Path == "/api/v2.0/projects":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/v2.0/projects/helm/repositories":
			if r.URL.Query().Get("page") == "1" {
				_ = json.NewEncoder(w).Encode([]map[string]string{{"name": repository}})
			} else {
				_ = json.NewEncoder(w).Encode([]map[string]string{})
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	caFile := writeTestCertificate(t, "registry-ca.crt", server.Certificate().Raw)
	source, err := url.Parse("oci://" + server.Listener.Addr().String() + "/helm")
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	repositories, err := DiscoverOCIRepositories(context.Background(), source, appv2.RepoCredential{CAFile: caFile, InsecureSkipTLSVerify: ptr.To(false)})
	if err != nil {
		t.Fatalf("DiscoverOCIRepositories() error: %v", err)
	}
	if want := []string{repository}; !reflect.DeepEqual(repositories, want) {
		t.Fatalf("repositories = %v, want %v", repositories, want)
	}
}

func TestHelmPullFromOCIUsesClientCertificate(t *testing.T) {
	clientCAs, certFile, keyFile := newTestClientCertificate(t)
	config := []byte(`{"apiVersion":"v2","name":"demo","version":"1.0.0"}`)
	chartData := []byte("chart archive")
	configDigest := digest.FromBytes(config)
	chartDigest := digest.FromBytes(chartData)
	manifest := ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		Config:    ocispec.Descriptor{MediaType: registry.ConfigMediaType, Digest: configDigest, Size: int64(len(config))},
		Layers:    []ocispec.Descriptor{{MediaType: registry.ChartLayerMediaType, Digest: chartDigest, Size: int64(len(chartData))}},
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	manifestDigest := digest.FromBytes(manifestData)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data []byte
		switch r.URL.Path {
		case "/v2/charts/demo/manifests/1.0.0":
			data = manifestData
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			w.Header().Set("Docker-Content-Digest", manifestDigest.String())
		case "/v2/charts/demo/blobs/" + configDigest.String():
			data = config
		case "/v2/charts/demo/blobs/" + chartDigest.String():
			data = chartData
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		if r.Method != http.MethodHead {
			_, _ = w.Write(data)
		}
	}))
	server.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientCAs}
	server.StartTLS()
	defer server.Close()

	got, err := HelmPullFromOci("oci://"+server.Listener.Addr().String()+"/charts/demo:1.0.0", appv2.RepoCredential{
		CertFile: certFile, KeyFile: keyFile, InsecureSkipTLSVerify: ptr.To(true),
	})
	if err != nil {
		t.Fatalf("HelmPullFromOci() error: %v", err)
	}
	if !reflect.DeepEqual(got, chartData) {
		t.Fatalf("chart data = %q, want %q", got, chartData)
	}
}

func TestHelmPullFromOCIUsesInsecureTLSWithBasicAuth(t *testing.T) {
	const username, password = "fixture-user", "fixture-password"
	config := []byte(`{"apiVersion":"v2","name":"demo","version":"1.0.0"}`)
	chartData := []byte("chart archive")
	configDigest := digest.FromBytes(config)
	chartDigest := digest.FromBytes(chartData)
	manifest := ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		Config:    ocispec.Descriptor{MediaType: registry.ConfigMediaType, Digest: configDigest, Size: int64(len(config))},
		Layers:    []ocispec.Descriptor{{MediaType: registry.ChartLayerMediaType, Digest: chartDigest, Size: int64(len(chartData))}},
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" || r.URL.Path == "/v2" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if gotUser, gotPassword, ok := r.BasicAuth(); !ok || gotUser != username || gotPassword != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var data []byte
		switch r.URL.Path {
		case "/v2/charts/demo/manifests/1.0.0":
			data = manifestData
			w.Header().Set("Docker-Content-Digest", digest.FromBytes(manifestData).String())
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
		case "/v2/charts/demo/blobs/" + configDigest.String():
			data = config
			w.Header().Set("Content-Type", registry.ConfigMediaType)
		case "/v2/charts/demo/blobs/" + chartDigest.String():
			data = chartData
			w.Header().Set("Content-Type", registry.ChartLayerMediaType)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		if r.Method != http.MethodHead {
			_, _ = w.Write(data)
		}
	}))
	defer server.Close()

	got, err := HelmPullFromOci("oci://"+server.Listener.Addr().String()+"/charts/demo:1.0.0", appv2.RepoCredential{
		Username: username, Password: password, InsecureSkipTLSVerify: ptr.To(true),
	})
	if err != nil {
		t.Fatalf("HelmPullFromOci() error: %v", err)
	}
	if !reflect.DeepEqual(got, chartData) {
		t.Fatalf("chart data = %q, want %q", got, chartData)
	}
}

func TestOCIClientsRejectInvalidTLSFilesBeforeNetworking(t *testing.T) {
	const username = "fixture-user"
	const password = "fixture-password"
	tests := []struct {
		name string
		cred appv2.RepoCredential
	}{
		{name: "CA", cred: appv2.RepoCredential{CAFile: "/missing/ca.pem", Username: username, Password: password}},
		{name: "client key pair", cred: appv2.RepoCredential{CertFile: "/missing/client.crt", KeyFile: "/missing/client.key", Username: username, Password: password}},
	}
	constructors := []struct {
		name string
		new  func(appv2.RepoCredential) error
	}{
		{name: "registry", new: func(cred appv2.RepoCredential) error {
			_, err := newOCIRegistry("oci://127.0.0.1:1/charts", cred)
			return err
		}},
		{name: "Helm pull", new: func(cred appv2.RepoCredential) error {
			_, err := newOCIRegistryClient("oci://127.0.0.1:1/charts", cred)
			return err
		}},
	}
	for _, test := range tests {
		for _, constructor := range constructors {
			t.Run(test.name+"/"+constructor.name, func(t *testing.T) {
				err := constructor.new(test.cred)
				if err == nil {
					t.Fatal("client construction error = nil")
				}
				if strings.Contains(err.Error(), username) || strings.Contains(err.Error(), password) {
					t.Fatalf("error leaks credentials: %v", err)
				}
			})
		}
	}
}

func TestOCIHelperErrorsDoNotExposeURLUserinfo(t *testing.T) {
	const username = "fixture-user"
	const password = "fixture-pass"
	rawURL := "oci://" + username + ":" + password + "@%zz/charts/demo"
	tests := []struct {
		name string
		call func() error
	}{
		{name: "registry", call: func() error {
			_, err := newOCIRegistry(rawURL, appv2.RepoCredential{})
			return err
		}},
		{name: "registry client", call: func() error {
			_, err := newOCIRegistryClient(rawURL, appv2.RepoCredential{})
			return err
		}},
		{name: "Helm pull", call: func() error {
			_, err := HelmPullFromOci(rawURL, appv2.RepoCredential{})
			return err
		}},
		{name: "index", call: func() error {
			_, _, err := LoadOCIRepoIndex(context.Background(), rawURL, appv2.RepoCredential{})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if err == nil {
				t.Fatal("helper error = nil, want malformed URL error")
			}
			if strings.Contains(err.Error(), username) || strings.Contains(err.Error(), password) {
				t.Fatal("helper error exposes OCI URL userinfo")
			}
		})
	}
}

func TestLoadRepoIndexFromOciBuildsMetadataAndFiltersImages(t *testing.T) {
	manifests := map[string]ocispec.Manifest{
		"charts/traefik:1.0.0_build.1": {
			Config: ocispec.Descriptor{MediaType: registry.ConfigMediaType, Digest: "sha256:traefik-config"},
			Layers: []ocispec.Descriptor{{MediaType: registry.ChartLayerMediaType, Digest: "sha256:traefik-layer"}},
		},
		"charts/traefik:0.9.0": {
			Config: ocispec.Descriptor{MediaType: registry.ConfigMediaType, Digest: "sha256:traefik-config"},
			Layers: []ocispec.Descriptor{{MediaType: registry.ChartLayerMediaType, Digest: "sha256:traefik-layer"}},
		},
		"charts/redis:2.0.0": {
			Config: ocispec.Descriptor{MediaType: registry.ConfigMediaType, Digest: "sha256:redis-config"},
			Layers: []ocispec.Descriptor{{MediaType: registry.LegacyChartLayerMediaType, Digest: "sha256:redis-layer"}},
		},
		"images/nginx:1.0.0": {
			Config: ocispec.Descriptor{MediaType: "application/vnd.oci.image.config.v1+json", Digest: "sha256:nginx-config"},
		},
		"charts/no-layer:3.0.0": {
			Config: ocispec.Descriptor{MediaType: registry.ConfigMediaType, Digest: "sha256:no-layer-config"},
			Layers: []ocispec.Descriptor{{MediaType: "application/vnd.example.provenance", Digest: "sha256:provenance"}},
		},
	}
	blobs := map[string][]byte{
		"sha256:traefik-config": []byte(`{"apiVersion":"v2","name":"traefik","version":"9.9.9","description":"Ingress controller","icon":"https://example.test/traefik.svg","maintainers":[{"name":"Alice"}]}`),
		"sha256:redis-config":   []byte(`{"apiVersion":"v2","name":"redis","version":"8.8.8","description":"Redis database","icon":"https://example.test/redis.svg","maintainers":[{"name":"Bob"}]}`),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/" || r.URL.Path == "/v2":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v2/_catalog":
			_ = json.NewEncoder(w).Encode(map[string][]string{"repositories": {"charts/traefik", "charts/redis", "images/nginx", "charts/no-layer"}})
		case r.URL.Path == "/v2/charts/traefik/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"0.9.0", "1.0.0_build.1"}})
		case r.URL.Path == "/v2/charts/redis/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"2.0.0"}})
		case r.URL.Path == "/v2/images/nginx/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0"}})
		case r.URL.Path == "/v2/charts/no-layer/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"3.0.0"}})
		default:
			for ref, manifest := range manifests {
				repository, tag, _ := strings.Cut(ref, ":")
				if r.URL.Path == "/v2/"+repository+"/manifests/"+tag {
					w.Header().Set("Docker-Content-Digest", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
					_ = json.NewEncoder(w).Encode(manifest)
					return
				}
			}
			for digest, blob := range blobs {
				if r.URL.Path == "/v2/charts/traefik/blobs/"+digest || r.URL.Path == "/v2/charts/redis/blobs/"+digest {
					_, _ = w.Write(blob)
					return
				}
			}
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	index, err := LoadRepoIndexFromOci(
		fmt.Sprintf("oci://%s", server.Listener.Addr()),
		appv2.RepoCredential{PlainHTTP: true},
	)
	if err != nil {
		t.Fatalf("LoadRepoIndexFromOci() error = %v", err)
	}
	if got := len(index.Entries); got != 2 {
		t.Fatalf("chart entries = %d, want 2", got)
	}
	traefik := index.Entries["traefik"][0]
	if got := len(index.Entries["traefik"]); got != 2 {
		t.Fatalf("traefik versions = %d, want 2", got)
	}
	if traefik.Description != "Ingress controller" || traefik.Icon != "https://example.test/traefik.svg" || traefik.Maintainers[0].Name != "Alice" {
		t.Fatalf("traefik metadata = %#v", traefik.Metadata)
	}
	if got := traefik.URLs[0]; got != fmt.Sprintf("oci://%s/charts/traefik:1.0.0_build.1", server.Listener.Addr()) {
		t.Fatalf("chart pull URL = %q", got)
	}
	if got := traefik.Digest; got != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("manifest digest = %q", got)
	}
	if got := traefik.Version; got != "1.0.0+build.1" {
		t.Fatalf("chart version = %q, want normalized tag", got)
	}
	redis := index.Entries["redis"][0]
	if redis.Description != "Redis database" || redis.Icon != "https://example.test/redis.svg" || redis.Maintainers[0].Name != "Bob" {
		t.Fatalf("redis metadata = %#v", redis.Metadata)
	}
}

func TestLoadOCIRepoIndexWarnsWhenHistoricalCatalogChartBecomesNonHelm(t *testing.T) {
	const (
		goodRepository       = "charts/good"
		historicalRepository = "charts/historical"
		imageRepository      = "images/unrelated"
		unrelatedImageTag    = "2.0.0"
	)
	var historicalIsChart atomic.Bool
	historicalIsChart.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/_catalog":
			_ = json.NewEncoder(w).Encode(map[string][]string{"repositories": {goodRepository, historicalRepository, imageRepository}})
		case "/v2/" + goodRepository + "/tags/list", "/v2/" + imageRepository + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0"}})
		case "/v2/" + historicalRepository + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0", unrelatedImageTag}})
		case "/v2/" + goodRepository + "/manifests/1.0.0":
			_ = json.NewEncoder(w).Encode(helmOCIManifest("sha256:good-config"))
		case "/v2/" + historicalRepository + "/manifests/1.0.0":
			if historicalIsChart.Load() {
				_ = json.NewEncoder(w).Encode(helmOCIManifest("sha256:historical-config"))
				return
			}
			_ = json.NewEncoder(w).Encode(ocispec.Manifest{Config: ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig}})
		case "/v2/" + historicalRepository + "/manifests/" + unrelatedImageTag:
			_ = json.NewEncoder(w).Encode(ocispec.Manifest{Config: ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig}})
		case "/v2/" + imageRepository + "/manifests/1.0.0":
			_ = json.NewEncoder(w).Encode(ocispec.Manifest{Config: ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig}})
		case "/v2/" + goodRepository + "/blobs/sha256:good-config":
			_, _ = w.Write([]byte(`{"apiVersion":"v2","name":"good","version":"1.0.0"}`))
		case "/v2/" + historicalRepository + "/blobs/sha256:historical-config":
			_, _ = w.Write([]byte(`{"apiVersion":"v2","name":"historical","version":"1.0.0"}`))
		default:
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	host := server.Listener.Addr().String()
	u := fmt.Sprintf("oci://%s", host)
	first, warnings, err := LoadOCIRepoIndexWithCache(context.Background(), u, appv2.RepoCredential{PlainHTTP: true}, nil)
	if err != nil || len(warnings) != 0 || len(first.Entries) != 2 {
		t.Fatalf("initial catalog load = entries %v, warnings %v, error %v", first.Entries, warnings, err)
	}
	history := OCIChartVersionCache{
		ociCacheKey(host, goodRepository, "1.0.0"):       nil,
		ociCacheKey(host, historicalRepository, "1.0.0"): nil,
	}
	historicalIsChart.Store(false)
	second, warnings, err := LoadOCIRepoIndexWithCache(context.Background(), u, appv2.RepoCredential{PlainHTTP: true}, history)
	if err != nil {
		t.Fatalf("changed catalog load error = %v", err)
	}
	if len(second.Entries["good"]) != 1 || len(second.Entries["historical"]) != 0 {
		t.Fatalf("changed catalog entries = %v, want only good", second.Entries)
	}
	if len(warnings) != 1 {
		t.Fatalf("changed catalog warnings = %v, want only historical chart warning", warnings)
	}
	var warning *OCIIndexWarning
	if !errors.As(warnings[0], &warning) || warning.Repository != historicalRepository || !errors.Is(warning, ErrNotHelmOCIArtifact) {
		t.Fatalf("warning = %#v, want historical non-Helm warning", warnings[0])
	}
	if warning.Tag != "1.0.0" {
		t.Fatalf("warning tag = %q, want historical chart tag 1.0.0", warning.Tag)
	}
}

func TestLoadOCIRepoIndexKeepsValidChartsAndReportsArtifactFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/_catalog":
			_ = json.NewEncoder(w).Encode(map[string][]string{"repositories": {"charts/broken", "charts/good"}})
		case "/v2/charts/broken/tags/list", "/v2/charts/good/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0"}})
		case "/v2/charts/broken/manifests/1.0.0":
			w.Header().Set("Docker-Content-Digest", "sha256:broken-manifest")
			_ = json.NewEncoder(w).Encode(helmOCIManifest("sha256:broken-config"))
		case "/v2/charts/good/manifests/1.0.0":
			w.Header().Set("Docker-Content-Digest", "sha256:good-manifest")
			_ = json.NewEncoder(w).Encode(helmOCIManifest("sha256:good-config"))
		case "/v2/charts/broken/blobs/sha256:broken-config":
			_, _ = w.Write([]byte(`{"name":`))
		case "/v2/charts/good/blobs/sha256:good-config":
			_, _ = w.Write([]byte(`{"apiVersion":"v2","name":"good","version":"9.9.9"}`))
		default:
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	index, warnings, err := LoadOCIRepoIndex(context.Background(), fmt.Sprintf("oci://%s", server.Listener.Addr()), appv2.RepoCredential{PlainHTTP: true})
	if err != nil {
		t.Fatalf("LoadOCIRepoIndex() error = %v", err)
	}
	if len(index.Entries["good"]) != 1 || len(index.Entries["broken"]) != 0 {
		t.Fatalf("index entries = %v, want only good", index.Entries)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one", warnings)
	}
	var warning *OCIIndexWarning
	if !errors.As(warnings[0], &warning) {
		t.Fatalf("warning type = %T, want *OCIIndexWarning", warnings[0])
	}
	if warning.Repository != "charts/broken" || warning.Tag != "1.0.0" {
		t.Fatalf("warning = %#v", warning)
	}
	if warning.Digest == "" || !strings.Contains(warning.Error(), warning.Digest) {
		t.Fatalf("warning digest = %q, error = %q", warning.Digest, warning.Error())
	}
	publicIndex, err := LoadRepoIndexFromOci(fmt.Sprintf("oci://%s", server.Listener.Addr()), appv2.RepoCredential{PlainHTTP: true})
	if err != nil || len(publicIndex.Entries["good"]) != 1 {
		t.Fatalf("LoadRepoIndexFromOci() = entries %v, error %v; want usable partial index", publicIndex.Entries, err)
	}
}

func TestLoadOCIRepoIndexWarningIncludesDigestForMalformedManifest(t *testing.T) {
	const repository = "charts/broken"
	manifest := []byte(`{"schemaVersion":`)
	manifestDigest := digest.FromBytes(manifest).String()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/" + repository + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0"}})
		case "/v2/" + repository + "/manifests/1.0.0":
			_, _ = w.Write(manifest)
		default:
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	index, warnings, err := LoadOCIRepoIndex(context.Background(), fmt.Sprintf("oci://%s/%s", server.Listener.Addr(), repository), appv2.RepoCredential{PlainHTTP: true})
	if err != nil || len(index.Entries) != 0 {
		t.Fatalf("LoadOCIRepoIndex() = entries %v, error %v", index.Entries, err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one", warnings)
	}
	var warning *OCIIndexWarning
	if !errors.As(warnings[0], &warning) {
		t.Fatalf("warning type = %T", warnings[0])
	}
	if warning.Digest != manifestDigest {
		t.Fatalf("warning digest = %q, want %q", warning.Digest, manifestDigest)
	}
}

func TestLoadOCIRepoIndexSkipsAuxiliaryAndInvalidTagsBeforeManifestRequests(t *testing.T) {
	const repository = "charts/good"
	manifestRequests := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/" + repository + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0", "v1.0.0", "1.0.0-metadata", "1.2", "latest"}})
		case "/v2/" + repository + "/manifests/1.0.0":
			manifestRequests["1.0.0"]++
			w.Header().Set("Docker-Content-Digest", "sha256:good-manifest")
			_ = json.NewEncoder(w).Encode(helmOCIManifest("sha256:good-config"))
		case "/v2/" + repository + "/blobs/sha256:good-config":
			_, _ = w.Write([]byte(`{"apiVersion":"v2","name":"good","version":"1.0.0"}`))
		default:
			if strings.Contains(r.URL.Path, "/manifests/") {
				manifestRequests[path.Base(r.URL.Path)]++
			}
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	index, warnings, err := LoadOCIRepoIndex(context.Background(), fmt.Sprintf("oci://%s/%s", server.Listener.Addr(), repository), appv2.RepoCredential{PlainHTTP: true})
	if err != nil || len(warnings) != 0 || len(index.Entries["good"]) != 1 {
		t.Fatalf("LoadOCIRepoIndex() = entries %v, warnings %v, error %v", index.Entries, warnings, err)
	}
	if !reflect.DeepEqual(manifestRequests, map[string]int{"1.0.0": 1}) {
		t.Fatalf("manifest requests = %v", manifestRequests)
	}
}

func TestLoadOCIRepoIndexWithCacheSkipsCachedTagMetadataRequests(t *testing.T) {
	const repository = "charts/demo"
	const cachedTag = "1.0.0"
	const newTag = "2.0.0"
	var cachedManifestRequests atomic.Int32
	var cachedConfigRequests atomic.Int32
	var newManifestRequests atomic.Int32
	var newConfigRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/_catalog":
			_ = json.NewEncoder(w).Encode(map[string][]string{"repositories": {repository}})
		case "/v2/" + repository + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {cachedTag, newTag, "2.0.0-metadata"}})
		case "/v2/" + repository + "/manifests/" + cachedTag:
			cachedManifestRequests.Add(1)
			_ = json.NewEncoder(w).Encode(helmOCIManifest("sha256:cached-config"))
		case "/v2/" + repository + "/blobs/sha256:cached-config":
			cachedConfigRequests.Add(1)
			_, _ = w.Write([]byte(`{"apiVersion":"v2","name":"demo","version":"1.0.0"}`))
		case "/v2/" + repository + "/manifests/" + newTag:
			newManifestRequests.Add(1)
			w.Header().Set("Docker-Content-Digest", "sha256:2222222222222222222222222222222222222222222222222222222222222222")
			_ = json.NewEncoder(w).Encode(helmOCIManifest("sha256:new-config"))
		case "/v2/" + repository + "/blobs/sha256:new-config":
			newConfigRequests.Add(1)
			_, _ = w.Write([]byte(`{"apiVersion":"v2","name":"demo","version":"2.0.0"}`))
		default:
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	host := server.Listener.Addr().String()
	cache := OCIChartVersionCache{
		ociCacheKey(host, repository, cachedTag): {
			Metadata: &chart.Metadata{Name: "demo", Version: cachedTag},
			URLs:     []string{fmt.Sprintf("oci://%s/%s:%s", host, repository, cachedTag)},
			Digest:   "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		},
	}
	index, warnings, err := LoadOCIRepoIndexWithCache(context.Background(), fmt.Sprintf("oci://%s", host), appv2.RepoCredential{PlainHTTP: true}, cache)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("LoadOCIRepoIndexWithCache() = entries %v, warnings %v, error %v", index.Entries, warnings, err)
	}
	if got := len(index.Entries["demo"]); got != 2 {
		t.Fatalf("demo versions = %d, want 2", got)
	}
	if got := cachedManifestRequests.Load(); got != 0 {
		t.Fatalf("cached manifest requests = %d, want 0", got)
	}
	if got := cachedConfigRequests.Load(); got != 0 {
		t.Fatalf("cached config requests = %d, want 0", got)
	}
	if got := newManifestRequests.Load(); got != 1 {
		t.Fatalf("new manifest requests = %d, want 1", got)
	}
	if got := newConfigRequests.Load(); got != 1 {
		t.Fatalf("new config requests = %d, want 1", got)
	}
}

func TestLoadOCIRepoIndexWithCacheDirectRepoReusesCachedVersionWithoutManifestRequests(t *testing.T) {
	const repository = "charts/demo"
	const tag = "1.0.0"
	firstDigest := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	configRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/" + repository + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {tag}})
		case "/v2/" + repository + "/manifests/" + tag:
			w.Header().Set("Docker-Content-Digest", firstDigest)
			_ = json.NewEncoder(w).Encode(helmOCIManifest("sha256:config"))
		case "/v2/" + repository + "/blobs/sha256:config":
			configRequests++
			_, _ = w.Write([]byte(`{"apiVersion":"v2","name":"demo","version":"1.0.0","description":"old"}`))
		default:
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	u := fmt.Sprintf("oci://%s/%s", server.Listener.Addr(), repository)
	cred := appv2.RepoCredential{PlainHTTP: true}
	first, warnings, err := LoadOCIRepoIndexWithCache(context.Background(), u, cred, nil)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("initial load = entries %v, warnings %v, error %v", first.Entries, warnings, err)
	}
	cache := OCIChartVersionCache{ociCacheKey(server.Listener.Addr().String(), repository, tag): first.Entries["demo"][0]}
	second, warnings, err := LoadOCIRepoIndexWithCache(context.Background(), u, cred, cache)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("cached load = entries %v, warnings %v, error %v", second.Entries, warnings, err)
	}
	if configRequests != 1 {
		t.Fatalf("config requests after unchanged digest = %d, want 1", configRequests)
	}
	third, warnings, err := LoadOCIRepoIndexWithCache(context.Background(), u, cred, cache)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("changed load = entries %v, warnings %v, error %v", third.Entries, warnings, err)
	}
	if configRequests != 1 {
		t.Fatalf("config requests after cached load = %d, want 1", configRequests)
	}
	if third.Entries["demo"][0].Digest != firstDigest || third.Entries["demo"][0].Description != "old" {
		t.Fatalf("cached chart = %#v", third.Entries["demo"][0])
	}
}

func TestLoadOCIRepoIndexWithCacheDirectRepoInspectsEveryMissingTag(t *testing.T) {
	const repository = "charts/demo"
	const latestTag = "2.0.0"
	const latestDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	var manifestRequests atomic.Int32
	var configRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/" + repository + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0", "1.2.0", latestTag, "2.0.0-metadata", "not-a-version"}})
		case "/v2/" + repository + "/manifests/1.0.0", "/v2/" + repository + "/manifests/1.2.0", "/v2/" + repository + "/manifests/" + latestTag:
			manifestRequests.Add(1)
			w.Header().Set("Docker-Content-Digest", latestDigest)
			_ = json.NewEncoder(w).Encode(helmOCIManifest("sha256:config"))
		case "/v2/" + repository + "/blobs/sha256:config":
			configRequests.Add(1)
			_, _ = w.Write([]byte(`{"apiVersion":"v2","name":"demo","version":"2.0.0"}`))
		default:
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	index, warnings, err := LoadOCIRepoIndexWithCache(context.Background(), fmt.Sprintf("oci://%s/%s", server.Listener.Addr(), repository), appv2.RepoCredential{PlainHTTP: true}, nil)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("LoadOCIRepoIndexWithCache() = entries %v, warnings %v, error %v", index.Entries, warnings, err)
	}
	versions := index.Entries["demo"]
	if len(versions) != 3 {
		t.Fatalf("demo versions = %d, want 3", len(versions))
	}
	for _, version := range versions {
		if got := version.URLs[0]; got != fmt.Sprintf("oci://%s/%s:%s", server.Listener.Addr(), repository, version.Version) {
			t.Fatalf("version URL = %q, want tag URL for %q", got, version.Version)
		}
		if version.Digest != latestDigest {
			t.Fatalf("digest for %q = %q, want %q", version.Version, version.Digest, latestDigest)
		}
	}
	if manifestRequests.Load() != 3 || configRequests.Load() != 3 {
		t.Fatalf("got %d manifest and %d config requests, want 3 each", manifestRequests.Load(), configRequests.Load())
	}
}

func TestLoadOCIRepoIndexWithCacheCachedTagCanBeFullyRefreshed(t *testing.T) {
	const repository = "charts/demo"
	const tag = "2.0.0"
	var manifestRequests atomic.Int32
	var configRequests atomic.Int32
	var metadataManifestRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/", "/v2":
			w.WriteHeader(http.StatusOK)
		case "/v2/" + repository + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {tag, tag + "-metadata"}})
		case "/v2/" + repository + "/manifests/" + tag:
			manifestRequests.Add(1)
			_ = json.NewEncoder(w).Encode(helmOCIManifest("sha256:config"))
		case "/v2/" + repository + "/manifests/" + tag + "-metadata":
			metadataManifestRequests.Add(1)
			w.WriteHeader(http.StatusNotFound)
		case "/v2/" + repository + "/blobs/sha256:config":
			configRequests.Add(1)
			_, _ = w.Write([]byte(`{"apiVersion":"v2","name":"demo","version":"2.0.0"}`))
		default:
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	host := server.Listener.Addr().String()
	u := fmt.Sprintf("oci://%s/%s", host, repository)
	cache := OCIChartVersionCache{
		ociCacheKey(host, repository, tag): {
			Metadata: &chart.Metadata{Name: "demo", Version: tag},
			URLs:     []string{fmt.Sprintf("oci://%s/%s:%s", host, repository, tag)},
			Digest:   "sha256:cached",
		},
	}

	if _, warnings, err := LoadOCIRepoIndexWithCache(context.Background(), u, appv2.RepoCredential{PlainHTTP: true}, cache); err != nil || len(warnings) != 0 {
		t.Fatalf("cached LoadOCIRepoIndexWithCache() = warnings %v, error %v", warnings, err)
	}
	if got := manifestRequests.Load(); got != 0 {
		t.Fatalf("cached manifest requests = %d, want 0", got)
	}
	if got := configRequests.Load(); got != 0 {
		t.Fatalf("cached config requests = %d, want 0", got)
	}
	if got := metadataManifestRequests.Load(); got != 0 {
		t.Fatalf("metadata manifest requests = %d, want 0", got)
	}

	if _, warnings, err := LoadOCIRepoIndexWithCache(context.Background(), u, appv2.RepoCredential{PlainHTTP: true}, nil); err != nil || len(warnings) != 0 {
		t.Fatalf("full LoadOCIRepoIndexWithCache() = warnings %v, error %v", warnings, err)
	}
	if got := manifestRequests.Load(); got != 1 {
		t.Fatalf("full manifest requests = %d, want 1", got)
	}
	if got := configRequests.Load(); got != 1 {
		t.Fatalf("full config requests = %d, want 1", got)
	}
	if got := metadataManifestRequests.Load(); got != 0 {
		t.Fatalf("metadata manifest requests = %d, want 0", got)
	}
}

func TestLoadOCIRepoIndexWithCacheDirectRepoInspectsMissingHistoricalTags(t *testing.T) {
	const repository = "charts/demo"
	const latestTag = "2.0.0"
	var cachedManifestRequests atomic.Int32
	var cachedConfigRequests atomic.Int32
	var firstHistoricalManifestRequests atomic.Int32
	var firstHistoricalConfigRequests atomic.Int32
	var secondHistoricalManifestRequests atomic.Int32
	var secondHistoricalConfigRequests atomic.Int32
	var invalidManifestRequests atomic.Int32
	var invalidConfigRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/" + repository + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0", "1.1.0", "1.2.0", latestTag, "2.0.0-metadata"}})
		case "/v2/" + repository + "/manifests/" + latestTag:
			cachedManifestRequests.Add(1)
			_ = json.NewEncoder(w).Encode(helmOCIManifest("sha256:cached-config"))
		case "/v2/" + repository + "/blobs/sha256:cached-config":
			cachedConfigRequests.Add(1)
			_, _ = w.Write([]byte(`{"apiVersion":"v2","name":"demo","version":"2.0.0"}`))
		case "/v2/" + repository + "/manifests/1.0.0":
			firstHistoricalManifestRequests.Add(1)
			_ = json.NewEncoder(w).Encode(helmOCIManifest("sha256:first-historical-config"))
		case "/v2/" + repository + "/blobs/sha256:first-historical-config":
			firstHistoricalConfigRequests.Add(1)
			_, _ = w.Write([]byte(`{"apiVersion":"v2","name":"demo","version":"1.0.0"}`))
		case "/v2/" + repository + "/manifests/1.1.0":
			secondHistoricalManifestRequests.Add(1)
			_ = json.NewEncoder(w).Encode(helmOCIManifest("sha256:second-historical-config"))
		case "/v2/" + repository + "/blobs/sha256:second-historical-config":
			secondHistoricalConfigRequests.Add(1)
			_, _ = w.Write([]byte(`{"apiVersion":"v2","name":"demo","version":"1.1.0"}`))
		case "/v2/" + repository + "/manifests/1.2.0":
			invalidManifestRequests.Add(1)
			_ = json.NewEncoder(w).Encode(ocispec.Manifest{Config: ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig}})
		case "/v2/" + repository + "/blobs/sha256:invalid-config":
			invalidConfigRequests.Add(1)
		default:
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	host := server.Listener.Addr().String()
	cache := OCIChartVersionCache{
		ociCacheKey(host, repository, latestTag): {
			Metadata: &chart.Metadata{Name: "demo", Version: latestTag},
			URLs:     []string{fmt.Sprintf("oci://%s/%s:%s", host, repository, latestTag)},
			Digest:   "sha256:cached",
		},
	}
	index, warnings, err := LoadOCIRepoIndexWithCache(context.Background(), fmt.Sprintf("oci://%s/%s", host, repository), appv2.RepoCredential{PlainHTTP: true}, cache)
	if err != nil {
		t.Fatalf("LoadOCIRepoIndexWithCache() error = %v", err)
	}
	if got := len(index.Entries["demo"]); got != 3 {
		t.Fatalf("demo versions = %d, want 3", got)
	}
	if got := cachedManifestRequests.Load(); got != 0 {
		t.Fatalf("cached manifest requests = %d, want 0", got)
	}
	if got := cachedConfigRequests.Load(); got != 0 {
		t.Fatalf("cached config requests = %d, want 0", got)
	}
	if got := firstHistoricalManifestRequests.Load(); got != 1 {
		t.Fatalf("first historical manifest requests = %d, want 1", got)
	}
	if got := firstHistoricalConfigRequests.Load(); got != 1 {
		t.Fatalf("first historical config requests = %d, want 1", got)
	}
	if got := secondHistoricalManifestRequests.Load(); got != 1 {
		t.Fatalf("second historical manifest requests = %d, want 1", got)
	}
	if got := secondHistoricalConfigRequests.Load(); got != 1 {
		t.Fatalf("second historical config requests = %d, want 1", got)
	}
	if got := invalidManifestRequests.Load(); got != 1 {
		t.Fatalf("invalid manifest requests = %d, want 1", got)
	}
	if got := invalidConfigRequests.Load(); got != 0 {
		t.Fatalf("invalid config requests = %d, want 0", got)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one invalid-artifact warning", warnings)
	}
	var warning *OCIIndexWarning
	if !errors.As(warnings[0], &warning) || warning.Tag != "1.2.0" || !errors.Is(warning, ErrNotHelmOCIArtifact) {
		t.Fatalf("warning = %#v, want invalid artifact for 1.2.0", warnings[0])
	}
}

func TestLoadOCIRepoIndexWithCacheDirectRepoBootstrapsWithOnlyForeignOrStaleCache(t *testing.T) {
	const repository = "charts/demo"
	const latestTag = "2.0.0"
	var manifestRequests atomic.Int32
	var configRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/" + repository + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0", latestTag}})
		case "/v2/" + repository + "/manifests/1.0.0", "/v2/" + repository + "/manifests/" + latestTag:
			manifestRequests.Add(1)
			w.Header().Set("Docker-Content-Digest", "sha256:2222222222222222222222222222222222222222222222222222222222222222")
			_ = json.NewEncoder(w).Encode(helmOCIManifest("sha256:config"))
		case "/v2/" + repository + "/blobs/sha256:config":
			configRequests.Add(1)
			_, _ = w.Write([]byte(`{"apiVersion":"v2","name":"demo","version":"2.0.0"}`))
		default:
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	host := server.Listener.Addr().String()
	foreign := &helmrepo.ChartVersion{Metadata: &chart.Metadata{Name: "foreign", Version: latestTag}, URLs: []string{"oci://example.invalid/charts/foreign:2.0.0"}}
	cache := OCIChartVersionCache{
		ociCacheKey(host, "charts/foreign", latestTag):            foreign,
		ociCacheKey("old.example.invalid", repository, latestTag): foreign,
		ociCacheKey(host, repository, "0.9.0"):                    foreign,
	}
	index, warnings, err := LoadOCIRepoIndexWithCache(context.Background(), fmt.Sprintf("oci://%s/%s", host, repository), appv2.RepoCredential{PlainHTTP: true}, cache)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("LoadOCIRepoIndexWithCache() error = %v, warning count = %d", err, len(warnings))
	}
	if got := len(index.Entries["demo"]); got != 2 {
		t.Fatalf("demo versions = %d, want 2", got)
	}
	if manifestRequests.Load() != 2 || configRequests.Load() != 2 {
		t.Fatalf("got %d manifest and %d config requests, want 2 each", manifestRequests.Load(), configRequests.Load())
	}
}

func TestLoadOCIRepoIndexWithCacheDirectRepoInspectsAllMissingTags(t *testing.T) {
	const repository = "charts/demo"
	const oldDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	const newDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	manifestRequests := make(map[string]int)
	configRequests := make(map[string]int)
	var requestsMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/" + repository + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0", "1.2.0", "1.3.0", "2.0.0"}})
		case "/v2/" + repository + "/manifests/1.3.0":
			requestsMu.Lock()
			manifestRequests["1.3.0"]++
			requestsMu.Unlock()
			w.Header().Set("Docker-Content-Digest", newDigest)
			_ = json.NewEncoder(w).Encode(helmOCIManifest("sha256:1.3.0-config"))
		case "/v2/" + repository + "/blobs/sha256:1.3.0-config":
			requestsMu.Lock()
			configRequests["1.3.0"]++
			requestsMu.Unlock()
			_, _ = w.Write([]byte(`{"apiVersion":"v2","name":"demo","version":"1.3.0"}`))
		case "/v2/" + repository + "/manifests/2.0.0":
			requestsMu.Lock()
			manifestRequests["2.0.0"]++
			requestsMu.Unlock()
			w.Header().Set("Docker-Content-Digest", newDigest)
			_ = json.NewEncoder(w).Encode(helmOCIManifest("sha256:2.0.0-config"))
		case "/v2/" + repository + "/blobs/sha256:2.0.0-config":
			requestsMu.Lock()
			configRequests["2.0.0"]++
			requestsMu.Unlock()
			_, _ = w.Write([]byte(`{"apiVersion":"v2","name":"demo","version":"2.0.0"}`))
		default:
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	u := fmt.Sprintf("oci://%s/%s", server.Listener.Addr(), repository)
	cache := OCIChartVersionCache{}
	for _, tag := range []string{"1.0.0", "1.2.0"} {
		cache[ociCacheKey(server.Listener.Addr().String(), repository, tag)] = &helmrepo.ChartVersion{
			Metadata: &chart.Metadata{Name: "demo", Version: tag},
			URLs:     []string{fmt.Sprintf("oci://%s/%s:%s", server.Listener.Addr(), repository, tag)},
			Digest:   oldDigest,
		}
	}

	index, warnings, err := LoadOCIRepoIndexWithCache(context.Background(), u, appv2.RepoCredential{PlainHTTP: true}, cache)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("LoadOCIRepoIndexWithCache() = entries %v, warnings %v, error %v", index.Entries, warnings, err)
	}
	versions := index.Entries["demo"]
	if len(versions) != 4 {
		t.Fatalf("demo versions = %d, want 4", len(versions))
	}
	for _, version := range versions {
		if (version.Version == "1.3.0" || version.Version == "2.0.0") && version.Digest != newDigest {
			t.Fatalf("new version %q digest = %q, want %q", version.Version, version.Digest, newDigest)
		}
		if (version.Version == "1.0.0" || version.Version == "1.2.0") && version.Digest != oldDigest {
			t.Fatalf("cached version %q digest = %q, want %q", version.Version, version.Digest, oldDigest)
		}
	}
	requestsMu.Lock()
	requestsMatch := reflect.DeepEqual(manifestRequests, map[string]int{"1.3.0": 1, "2.0.0": 1}) && reflect.DeepEqual(configRequests, map[string]int{"1.3.0": 1, "2.0.0": 1})
	requestsMu.Unlock()
	if !requestsMatch {
		t.Fatalf("manifest requests = %v, config requests = %v; want one request for each missing tag", manifestRequests, configRequests)
	}
}

func TestLoadOCIRepoIndexWithCacheDirectRepoWarnsForNewNonHelmTag(t *testing.T) {
	const repository = "charts/demo"
	const oldTag = "1.0.0"
	const newTag = "2.0.0"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/" + repository + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {oldTag, newTag}})
		case "/v2/" + repository + "/manifests/" + newTag:
			_ = json.NewEncoder(w).Encode(ocispec.Manifest{Config: ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig}})
		default:
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	host := server.Listener.Addr().String()
	cache := OCIChartVersionCache{
		ociCacheKey(host, repository, oldTag): {
			Metadata: &chart.Metadata{Name: "demo", Version: oldTag},
			URLs:     []string{fmt.Sprintf("oci://%s/%s:%s", host, repository, oldTag)},
		},
	}
	index, warnings, err := LoadOCIRepoIndexWithCache(context.Background(), fmt.Sprintf("oci://%s/%s", host, repository), appv2.RepoCredential{PlainHTTP: true}, cache)
	if err != nil {
		t.Fatalf("LoadOCIRepoIndexWithCache() error = %v", err)
	}
	if got := len(index.Entries["demo"]); got != 1 {
		t.Fatalf("cached demo versions = %d, want 1", got)
	}
	if len(warnings) != 1 {
		t.Fatalf("warning count = %d, want 1", len(warnings))
	}
	var warning *OCIIndexWarning
	if !errors.As(warnings[0], &warning) || warning.Tag != newTag || !errors.Is(warning, ErrNotHelmOCIArtifact) {
		t.Fatalf("warning = %#v, want OCIIndexWarning for the new non-Helm tag", warnings[0])
	}
}

func TestLoadOCIRepoIndexWithCache77TagsLimitsMetadataConcurrencyAndAggregatesPartialFailures(t *testing.T) {
	const repository = "charts/demo"
	const failedTag = "1.0.76"
	tags := make([]string, 77)
	for i := range tags {
		tags[i] = fmt.Sprintf("1.0.%d", i)
	}

	var activeManifestRequests atomic.Int32
	var peakManifestRequests atomic.Int32
	var manifestRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/"+repository+"/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": tags})
		case strings.HasPrefix(r.URL.Path, "/v2/"+repository+"/manifests/"):
			tag := path.Base(r.URL.Path)
			manifestRequests.Add(1)
			if tag == failedTag {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			active := activeManifestRequests.Add(1)
			for {
				peak := peakManifestRequests.Load()
				if active <= peak || peakManifestRequests.CompareAndSwap(peak, active) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			activeManifestRequests.Add(-1)
			_ = json.NewEncoder(w).Encode(helmOCIManifest("sha256:config-" + tag))
		case strings.HasPrefix(r.URL.Path, "/v2/"+repository+"/blobs/sha256:config-"):
			_, _ = w.Write([]byte(`{"apiVersion":"v2","name":"demo","version":"1.0.0"}`))
		default:
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	index, warnings, err := LoadOCIRepoIndexWithCache(context.Background(), fmt.Sprintf("oci://%s/%s", server.Listener.Addr(), repository), appv2.RepoCredential{PlainHTTP: true}, nil, OCIIndexOptions{MetadataConcurrency: 2})
	if err != nil {
		t.Fatalf("LoadOCIRepoIndexWithCache() error = %v", err)
	}
	if got := len(index.Entries["demo"]); got != 76 {
		t.Fatalf("demo versions = %d, want 76", got)
	}
	if got := manifestRequests.Load(); got != 77 {
		t.Fatalf("manifest requests = %d, want 77", got)
	}
	if got := peakManifestRequests.Load(); got <= 1 || got > 2 {
		t.Fatalf("peak manifest requests = %d, want between 2 and 2", got)
	}
	if len(warnings) != 1 {
		t.Fatalf("warning count = %d, want 1", len(warnings))
	}
	var warning *OCIIndexWarning
	if !errors.As(warnings[0], &warning) || warning.Tag != failedTag {
		t.Fatalf("warning = %#v, want OCIIndexWarning for %s", warnings[0], failedTag)
	}
}

func TestLoadOCIChartVersionsSortsCachedAndInspectedTagsTogether(t *testing.T) {
	const repository = "charts/demo"
	const cachedTag = "1.0.1"
	tags := []string{"1.0.0", cachedTag, "1.0.2"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v2/"+repository+"/manifests/"):
			_ = json.NewEncoder(w).Encode(helmOCIManifest("sha256:config"))
		case r.URL.Path == "/v2/"+repository+"/blobs/sha256:config":
			_, _ = w.Write([]byte(`{"apiVersion":"v2","name":"demo","version":"1.0.0"}`))
		default:
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	host := server.Listener.Addr().String()
	reg, err := newOCIRegistry(fmt.Sprintf("oci://%s", host), appv2.RepoCredential{PlainHTTP: true})
	if err != nil {
		t.Fatalf("newOCIRegistry() error = %v", err)
	}
	cache := OCIChartVersionCache{
		ociCacheKey(host, repository, cachedTag): {
			Metadata: &chart.Metadata{Name: "demo", Version: cachedTag},
			URLs:     []string{fmt.Sprintf("oci://%s/%s:%s", host, repository, cachedTag)},
		},
	}
	versions, warnings := loadOCIChartVersions(context.Background(), reg, host, repository, tags, cache)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	gotTags := make([]string, 0, len(versions))
	for _, version := range versions {
		_, _, tag, found := ociReferenceFromPullURL(version.URLs[0])
		if !found {
			t.Fatalf("version URL %q is not an OCI tag reference", version.URLs[0])
		}
		gotTags = append(gotTags, tag)
	}
	if !reflect.DeepEqual(gotTags, tags) {
		t.Fatalf("combined tags = %v, want %v", gotTags, tags)
	}
}

func TestLoadOCIChartVersionsStopsQueuedWorkWhenParentContextCanceled(t *testing.T) {
	const repository = "charts/demo"
	tags := []string{"1.0.0", "1.0.1", "1.0.2", "1.0.3", "1.0.4", "1.0.5"}
	started := make(chan struct{}, ociMetadataConcurrency)
	var manifestRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v2/"+repository+"/manifests/") {
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		manifestRequests.Add(1)
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()

	host := server.Listener.Addr().String()
	reg, err := newOCIRegistry(fmt.Sprintf("oci://%s", host), appv2.RepoCredential{PlainHTTP: true})
	if err != nil {
		t.Fatalf("newOCIRegistry() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		versions []*helmrepo.ChartVersion
		warnings []error
	}
	completed := make(chan result, 1)
	go func() {
		versions, warnings := loadOCIChartVersions(ctx, reg, host, repository, tags, nil)
		completed <- result{versions: versions, warnings: warnings}
	}()
	for range ociMetadataConcurrency {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("workers did not start manifest requests")
		}
	}
	cancel()
	select {
	case result := <-completed:
		if len(result.versions) != 0 || len(result.warnings) != ociMetadataConcurrency {
			t.Fatalf("result = %d versions, %d warnings; want no versions and %d warnings", len(result.versions), len(result.warnings), ociMetadataConcurrency)
		}
	case <-time.After(time.Second):
		t.Fatal("loadOCIChartVersions() did not return after context cancellation")
	}
	if got := manifestRequests.Load(); got != ociMetadataConcurrency {
		t.Fatalf("manifest requests = %d, want %d; queued work must stop", got, ociMetadataConcurrency)
	}
}

func TestLoadOCIRepoIndexSortsMultipleTagWarnings(t *testing.T) {
	const repository = "charts/demo"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/" + repository + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0", "1.0.1", "1.0.2"}})
		case "/v2/" + repository + "/manifests/1.0.0":
			w.WriteHeader(http.StatusUnauthorized)
		case "/v2/" + repository + "/manifests/1.0.1":
			_ = json.NewEncoder(w).Encode(ocispec.Manifest{Config: ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig}})
		case "/v2/" + repository + "/manifests/1.0.2":
			_ = json.NewEncoder(w).Encode(helmOCIManifest("sha256:bad-config"))
		case "/v2/" + repository + "/blobs/sha256:bad-config":
			_, _ = w.Write([]byte(`{"name":`))
		default:
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	_, warnings, err := LoadOCIRepoIndex(context.Background(), fmt.Sprintf("oci://%s/%s", server.Listener.Addr(), repository), appv2.RepoCredential{PlainHTTP: true})
	if err != nil {
		t.Fatalf("LoadOCIRepoIndex() error = %v", err)
	}
	if len(warnings) != 3 {
		t.Fatalf("warning count = %d, want 3", len(warnings))
	}
	for i, wantTag := range []string{"1.0.0", "1.0.1", "1.0.2"} {
		var warning *OCIIndexWarning
		if !errors.As(warnings[i], &warning) || warning.Tag != wantTag {
			t.Fatalf("warning %d = %#v, want tag %s", i, warnings[i], wantTag)
		}
	}
}

func helmOCIManifest(configDigest string) ocispec.Manifest {
	return ocispec.Manifest{
		Config: ocispec.Descriptor{MediaType: registry.ConfigMediaType, Digest: digest.Digest(configDigest)},
		Layers: []ocispec.Descriptor{{MediaType: registry.ChartLayerMediaType}},
	}
}

func TestValidateOCIRepositoryAcceptsInspectedHelmArtifact(t *testing.T) {
	const repo = "charts/demo"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/", "/v2":
			w.WriteHeader(http.StatusOK)
		case "/v2/" + repo + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0", "3.0.0", "2.0.0"}})
		case "/v2/" + repo + "/manifests/1.0.0", "/v2/" + repo + "/manifests/2.0.0", "/v2/" + repo + "/manifests/3.0.0":
			w.Header().Set("Docker-Content-Digest", "sha256:manifest")
			_ = json.NewEncoder(w).Encode(ocispec.Manifest{Config: ocispec.Descriptor{MediaType: registry.ConfigMediaType, Digest: "sha256:config"}, Layers: []ocispec.Descriptor{{MediaType: registry.ChartLayerMediaType}}})
		case "/v2/" + repo + "/blobs/sha256:config":
			_, _ = w.Write([]byte(`{"apiVersion":"v2","name":"demo","version":"1.0.0"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	if err := ValidateOCIRepository(fmt.Sprintf("oci://%s/%s", server.Listener.Addr(), repo), appv2.RepoCredential{PlainHTTP: true}); err != nil {
		t.Fatalf("ValidateOCIRepository() error = %v", err)
	}
}
