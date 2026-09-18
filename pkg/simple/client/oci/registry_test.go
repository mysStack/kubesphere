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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestFetchManifestDescriptorRetriesRetryableStatus(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
	}{
		{name: "too many requests", status: http.StatusTooManyRequests},
		{name: "service unavailable", status: http.StatusServiceUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				if tt.status == http.StatusTooManyRequests {
					w.Header().Set("Retry-After", "0")
				}
				w.WriteHeader(tt.status)
			}))
			defer server.Close()

			reg := newTestRegistry(t, server)
			_, _, err := reg.FetchManifestDescriptor(context.Background(), "charts/example", "1.0.0")
			if err == nil {
				t.Fatalf("FetchManifestDescriptor() error = nil, want status %d error", tt.status)
			}
			if requests != 3 {
				t.Fatalf("requests = %d, want 3", requests)
			}
			var responseErr *registryResponseError
			if !errors.As(err, &responseErr) {
				t.Fatalf("FetchManifestDescriptor() error = %T, want registryResponseError", err)
			}
			if responseErr.StatusCode != tt.status {
				t.Fatalf("status code = %d, want %d", responseErr.StatusCode, tt.status)
			}
			if tt.status == http.StatusTooManyRequests && (responseErr.RetryAfter == nil || *responseErr.RetryAfter != 0) {
				t.Fatalf("RetryAfter = %v, want 0", responseErr.RetryAfter)
			}
		})
	}
}

func TestFetchManifestDescriptorDoesNotRetryPermanentStatus(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
	}{
		{name: "unauthorized", status: http.StatusUnauthorized},
		{name: "not found", status: http.StatusNotFound},
		{name: "non-standard 600", status: 600},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				w.WriteHeader(tt.status)
			}))
			defer server.Close()

			reg := newTestRegistry(t, server)
			_, _, err := reg.FetchManifestDescriptor(context.Background(), "charts/example", "1.0.0")
			if err == nil {
				t.Fatalf("FetchManifestDescriptor() error = nil, want status %d error", tt.status)
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want 1", requests)
			}
		})
	}
}

func TestFetchManifestDescriptorRetryAfterZeroDoesNotDelay(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if requests == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	reg := newTestRegistry(t, server)
	started := time.Now()
	_, _, err := reg.FetchManifestDescriptor(context.Background(), "charts/example", "1.0.0")
	if err != nil {
		t.Fatalf("FetchManifestDescriptor() error = %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if elapsed := time.Since(started); elapsed >= 150*time.Millisecond {
		t.Fatalf("Retry-After: 0 delayed request for %s", elapsed)
	}
}

func newTestRegistry(t *testing.T, server *httptest.Server) *Registry {
	t.Helper()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	reg, err := NewRegistry(serverURL.Host, WithPlainHTTP())
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	return reg
}

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
