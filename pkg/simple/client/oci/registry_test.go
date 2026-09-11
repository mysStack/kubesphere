/*
 * Copyright 2024 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 * https://github.com/kubesphere/kubesphere/blob/master/LICENSE
 */

package oci

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestFetchManifestDescriptorReturnsDigest(t *testing.T) {
	manifest := ocispec.Manifest{
		Config: ocispec.Descriptor{
			MediaType: "application/vnd.example.config.v1+json",
			Digest:    "sha256:config",
			Size:      7,
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/charts/example/manifests/1.0.0" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Docker-Content-Digest", "sha256:manifest-digest")
		w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	reg, err := NewRegistry(serverURL.Host, WithPlainHTTP())
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	got, digest, err := reg.FetchManifestDescriptor(context.Background(), "charts/example", "1.0.0")
	if err != nil {
		t.Fatalf("FetchManifestDescriptor() error = %v", err)
	}
	if got.Config.Digest != manifest.Config.Digest {
		t.Fatalf("manifest config digest = %q, want %q", got.Config.Digest, manifest.Config.Digest)
	}
	if digest != "sha256:manifest-digest" {
		t.Fatalf("manifest digest = %q, want %q", digest, "sha256:manifest-digest")
	}
}

func TestFetchManifestDescriptorReturnsRegistryError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	reg, err := NewRegistry(serverURL.Host, WithPlainHTTP())
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	_, _, err = reg.FetchManifestDescriptor(context.Background(), "charts/example", "1.0.0")
	if err == nil {
		t.Fatal("FetchManifestDescriptor() error = nil, want registry error")
	}
}
