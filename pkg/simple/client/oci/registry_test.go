/*
 * Copyright 2024 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 * https://github.com/kubesphere/kubesphere/blob/master/LICENSE
 */

package oci

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestFetchManifestDescriptorComputesDigestWhenHeaderMissing(t *testing.T) {
	body := []byte(`{"schemaVersion":2,"config":{"mediaType":"application/vnd.example.config.v1+json","digest":"sha256:config","size":7}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/charts/example/manifests/1.0.0" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
		_, _ = w.Write(body)
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
	_, got, err := reg.FetchManifestDescriptor(context.Background(), "charts/example", "1.0.0")
	if err != nil {
		t.Fatalf("FetchManifestDescriptor() error = %v", err)
	}
	want := digest.NewDigestFromEncoded(digest.SHA256, fmt.Sprintf("%x", sha256.Sum256(body))).String()
	if got != want {
		t.Fatalf("manifest digest = %q, want %q", got, want)
	}
}

func TestFetchBlobRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "0123456789")
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
	reg.MaxMetadataBytes = 5
	_, err = reg.FetchBlob(context.Background(), "charts/example", ocispec.Descriptor{Digest: "sha256:blob"})
	if err == nil {
		t.Fatal("FetchBlob() error = nil, want oversized response error")
	}
}

func TestFetchManifestDescriptorReturnsDigest(t *testing.T) {
	const manifestDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
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
		w.Header().Set("Docker-Content-Digest", manifestDigest)
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
	if digest != manifestDigest {
		t.Fatalf("manifest digest = %q, want %q", digest, manifestDigest)
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
