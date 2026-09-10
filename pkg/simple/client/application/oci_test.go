/*
 * Copyright 2024 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 * https://github.com/kubesphere/kubesphere/blob/master/LICENSE
 */

package application

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/registry"
	appv2 "kubesphere.io/api/application/v2"
)

func TestGetRepoChartsFromOciWithCatalog(t *testing.T) {
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
	repos, err := GetRepoChartsFromOci(serverURL, cred)
	if err != nil {
		t.Fatalf("GetRepoChartsFromOci() error: %s", err)
	}
	if len(repos) != len(testRepos) {
		t.Fatalf("expected %d repos, got %d", len(testRepos), len(repos))
	}
}

func TestGetRepoChartsFromOciDirectRepoPath(t *testing.T) {
	testRepo := "helmcharts/nginx"
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
	repos, err := GetRepoChartsFromOci(parsedURL, cred)
	if err != nil {
		t.Fatalf("GetRepoChartsFromOci() error: %s", err)
	}
	if len(repos) != 1 || repos[0] != testRepo {
		t.Fatalf("expected repos [%s], got %v", testRepo, repos)
	}
}

func TestGetRepoChartsFromOciFallsBackToCatalogForNamespace(t *testing.T) {
	testRepos := []string{"helmcharts/nginx", "helmcharts/test-api", "other/demo"}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && (r.URL.Path == "/v2" || r.URL.Path == "/v2/") {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v2/helmcharts/tags/list" {
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

		t.Logf("unexpected access: %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	serverURL, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("invalid test http server: %v", err)
	}

	ociURL := fmt.Sprintf("oci://%s/helmcharts", serverURL.Host)
	parsedURL, err := url.Parse(ociURL)
	if err != nil {
		t.Fatalf("invalid OCI URL: %v", err)
	}

	repos, err := GetRepoChartsFromOci(parsedURL, appv2.RepoCredential{PlainHTTP: true})
	if err != nil {
		t.Fatalf("GetRepoChartsFromOci() error: %s", err)
	}
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos, got %d: %v", len(repos), repos)
	}
}

func TestLoadRepoIndexFromOciSkipsAuxiliaryArtifacts(t *testing.T) {
	const repo = "charts/demo"
	config, err := json.Marshal(chart.Metadata{
		APIVersion: "v2",
		Name:       "demo",
		Version:    "1.0.0",
	})
	if err != nil {
		t.Fatalf("marshal chart metadata: %v", err)
	}
	chartData := []byte("chart archive")
	configDigest := digest.FromBytes(config)
	chartDigest := digest.FromBytes(chartData)
	validManifest, err := json.Marshal(ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config: ocispec.Descriptor{
			MediaType: registry.ConfigMediaType,
			Digest:    configDigest,
			Size:      int64(len(config)),
		},
		Layers: []ocispec.Descriptor{{
			MediaType: registry.ChartLayerMediaType,
			Digest:    chartDigest,
			Size:      int64(len(chartData)),
		}},
	})
	if err != nil {
		t.Fatalf("marshal valid manifest: %v", err)
	}

	metadataManifestRequests := 0
	nonHelmManifestRequests := 0
	chartBlobRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/", "/v2":
			w.WriteHeader(http.StatusOK)
		case "/v2/" + repo + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0", "1.0.0-metadata", "1.0.1", "1.0.2_build.1"}})
		case "/v2/" + repo + "/manifests/1.0.0":
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			_, _ = w.Write(validManifest)
		case "/v2/" + repo + "/manifests/1.0.0-metadata":
			metadataManifestRequests++
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			_, _ = w.Write([]byte(`{"schemaVersion":2}`))
		case "/v2/" + repo + "/manifests/1.0.1", "/v2/" + repo + "/manifests/1.0.2_build.1":
			nonHelmManifestRequests++
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			_, _ = w.Write([]byte(`{"schemaVersion":2}`))
		case "/v2/" + repo + "/blobs/" + configDigest.String():
			_, _ = w.Write(config)
		case "/v2/" + repo + "/blobs/" + chartDigest.String():
			chartBlobRequests++
			_, _ = w.Write(chartData)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	index, err := LoadRepoIndexFromOci(fmt.Sprintf("oci://%s/%s", server.Listener.Addr(), repo), appv2.RepoCredential{PlainHTTP: true})
	if err != nil {
		t.Fatalf("LoadRepoIndexFromOci() error = %v", err)
	}
	if got := len(index.Entries["demo"]); got != 1 {
		t.Fatalf("chart entries = %d, want 1", got)
	}
	if metadataManifestRequests != 0 {
		t.Fatalf("metadata artifact was requested %d times, want 0", metadataManifestRequests)
	}
	if nonHelmManifestRequests != 2 {
		t.Fatalf("non-Helm artifact manifest requests = %d, want 2", nonHelmManifestRequests)
	}
	if chartBlobRequests != 0 {
		t.Fatalf("chart package was downloaded %d times, want 0", chartBlobRequests)
	}
}
