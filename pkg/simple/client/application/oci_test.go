/*
 * Copyright 2024 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 * https://github.com/kubesphere/kubesphere/blob/master/LICENSE
 */

package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"helm.sh/helm/v3/pkg/registry"
	appv2 "kubesphere.io/api/application/v2"
)

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
					w.Header().Set("Docker-Content-Digest", "sha256:"+strings.ReplaceAll(repository, "/", "-")+"-manifest")
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
	if got := traefik.Digest; got != "sha256:charts-traefik-manifest" {
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
	publicIndex, err := LoadRepoIndexFromOci(fmt.Sprintf("oci://%s", server.Listener.Addr()), appv2.RepoCredential{PlainHTTP: true})
	if err != nil || len(publicIndex.Entries["good"]) != 1 {
		t.Fatalf("LoadRepoIndexFromOci() = entries %v, error %v; want usable partial index", publicIndex.Entries, err)
	}
}

func TestLoadOCIRepoIndexSkipsAuxiliaryAndInvalidTagsBeforeManifestRequests(t *testing.T) {
	const repository = "charts/good"
	manifestRequests := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/" + repository + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0", "1.0.0-metadata", "1.2", "latest"}})
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
