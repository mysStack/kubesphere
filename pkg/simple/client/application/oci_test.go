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
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestLoadRepoIndexFromOciTagsDoesNotFetchManifests(t *testing.T) {
	const repo = "charts/demo"
	manifestRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/", "/v2":
			w.WriteHeader(http.StatusOK)
		case "/v2/" + repo + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0", "1.1.0_build.1", "1.1.0-metadata", "latest"}})
		default:
			if strings.Contains(r.URL.Path, "/manifests/") {
				manifestRequests++
			}
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	index, err := LoadRepoIndexFromOciTags(
		fmt.Sprintf("oci://%s/%s", server.Listener.Addr(), repo),
		appv2.RepoCredential{PlainHTTP: true},
	)
	if err != nil {
		t.Fatalf("LoadRepoIndexFromOciTags() error = %v", err)
	}
	if got := len(index.Entries["demo"]); got != 2 {
		t.Fatalf("chart entries = %d, want 2", got)
	}
	if got := index.Entries["demo"][0].Version; got != "1.1.0+build.1" {
		t.Fatalf("newest chart version = %q, want 1.1.0+build.1", got)
	}
	wantPullURL := fmt.Sprintf("oci://%s/%s:1.1.0_build.1", server.Listener.Addr(), repo)
	if got := index.Entries["demo"][0].URLs[0]; got != wantPullURL {
		t.Fatalf("chart pull URL = %q, want original OCI tag", got)
	}
	if manifestRequests != 0 {
		t.Fatalf("manifest requests = %d, want 0", manifestRequests)
	}
}

func TestLoadRepoIndexUsesTagsForOCIRepositories(t *testing.T) {
	const repo = "charts/demo"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/", "/v2":
			w.WriteHeader(http.StatusOK)
		case "/v2/" + repo + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0"}})
		default:
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	index, err := LoadRepoIndex(fmt.Sprintf("oci://%s/%s", server.Listener.Addr(), repo), appv2.RepoCredential{PlainHTTP: true})
	if err != nil {
		t.Fatalf("LoadRepoIndex() error = %v", err)
	}
	if got := len(index.Entries["demo"]); got != 1 {
		t.Fatalf("chart entries = %d, want 1", got)
	}
}

func TestValidateOCIRepositoryAcceptsTagsWithoutFetchingManifests(t *testing.T) {
	const repo = "charts/demo"
	manifestRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/", "/v2":
			w.WriteHeader(http.StatusOK)
		case "/v2/" + repo + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0", "3.0.0", "2.0.0"}})
		default:
			if strings.Contains(r.URL.Path, "/manifests/") {
				manifestRequests++
			}
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	if err := ValidateOCIRepository(fmt.Sprintf("oci://%s/%s", server.Listener.Addr(), repo), appv2.RepoCredential{PlainHTTP: true}); err != nil {
		t.Fatalf("ValidateOCIRepository() error = %v", err)
	}
	if manifestRequests != 0 {
		t.Fatalf("manifest requests = %d, want 0", manifestRequests)
	}
}
