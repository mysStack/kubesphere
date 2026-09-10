/*
 * Copyright 2024 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 * https://github.com/kubesphere/kubesphere/blob/master/LICENSE
 */

package application

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/registry"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	appv2 "kubesphere.io/api/application/v2"
	"kubesphere.io/kubesphere/pkg/constants"
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

func TestLoadRepoIndexFromOciReusesCachedChartTag(t *testing.T) {
	const repo = "charts/demo"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/", "/v2":
			w.WriteHeader(http.StatusOK)
		case "/v2/" + repo + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0"}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cached := OCIChartVersionCache{
		ociCacheKey(server.Listener.Addr().String(), repo, "1.0.0"): {
			Metadata: &chart.Metadata{APIVersion: "v2", Name: "demo", Version: "1.0.0"},
			Digest:   "cached-digest",
			URLs:     []string{fmt.Sprintf("oci://%s/%s:1.0.0", server.Listener.Addr(), repo)},
		},
	}
	index, err := LoadRepoIndexFromOciWithCache(fmt.Sprintf("oci://%s/%s", server.Listener.Addr(), repo), appv2.RepoCredential{PlainHTTP: true}, cached)
	if err != nil {
		t.Fatalf("LoadRepoIndexFromOciWithCache() error = %v", err)
	}
	versions := index.Entries["demo"]
	if len(versions) != 1 || versions[0].Digest != "cached-digest" {
		t.Fatalf("cached chart entry = %#v, want cached version", versions)
	}
}

func TestBuildOCIChartVersionCacheUsesApplicationVersions(t *testing.T) {
	apps := []appv2.Application{{
		ObjectMeta: metav1.ObjectMeta{
			Name: "repo-demo",
			Annotations: map[string]string{
				appv2.AppOriginalNameLabelKey:      "demo",
				constants.DescriptionAnnotationKey: "cached chart",
			},
		},
		Spec: appv2.ApplicationSpec{
			AppHome: "https://application.example",
			Icon:    "application-icon",
		},
	}}
	versions := []appv2.ApplicationVersion{{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{appv2.AppIDLabelKey: "repo-demo"},
			Annotations: map[string]string{
				constants.DescriptionAnnotationKey: "version description",
			},
		},
		Spec: appv2.ApplicationVersionSpec{
			VersionName: "1.0.0",
			Digest:      "cached-digest",
			PullUrl:     "oci://registry.example/charts/demo:1.0.0",
			AppHome:     "https://version.example",
			Icon:        "version-icon",
		},
	}}

	cache := BuildOCIChartVersionCache(apps, versions)
	entry, found := cache[ociCacheKey("registry.example", "charts/demo", "1.0.0")]
	if !found {
		t.Fatal("cached OCI tag not found")
	}
	if entry.Name != "demo" || entry.Version != "1.0.0" || entry.Digest != "cached-digest" || entry.Description != "version description" || entry.Home != "https://version.example" || entry.Icon != "version-icon" {
		t.Fatalf("cached chart entry = %#v", entry)
	}
}
