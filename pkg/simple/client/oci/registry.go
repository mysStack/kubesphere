/*
 * Copyright 2024 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 * https://github.com/kubesphere/kubesphere/blob/master/LICENSE
 */

package oci

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/pkg/registry"
	"oras.land/oras-go/pkg/registry/remote"
	"oras.land/oras-go/pkg/registry/remote/auth"
)

type RepositoryOptions remote.Repository
type RegistryOption func(*Registry)

// Registry is an HTTP client to a remote registry by oras-go 2.x.
// Registry with authentication requires an administrator account.
type Registry struct {
	RepositoryOptions

	RepositoryListPageSize int

	username              string
	password              string
	timeout               time.Duration
	insecureSkipVerifyTLS bool
}

func NewRegistry(name string, options ...RegistryOption) (*Registry, error) {
	ref := registry.Reference{
		Registry: name,
	}
	if err := ref.ValidateRegistry(); err != nil {
		return nil, err
	}

	reg := &Registry{RepositoryOptions: RepositoryOptions{
		Reference: ref,
	}}
	for _, option := range options {
		option(reg)
	}

	headers := http.Header{}
	headers.Set("User-Agent", "kubesphere.io")
	reg.Client = &auth.Client{
		Client: &http.Client{
			Timeout:   reg.timeout,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: reg.insecureSkipVerifyTLS}, Proxy: http.ProxyFromEnvironment},
		},
		Header: headers,
		Credential: func(_ context.Context, _ string) (auth.Credential, error) {
			if reg.username == "" && reg.password == "" {
				return auth.EmptyCredential, nil
			}

			return auth.Credential{
				Username: reg.username,
				Password: reg.password,
			}, nil
		},
	}

	return reg, nil
}

func WithBasicAuth(username, password string) RegistryOption {
	return func(reg *Registry) {
		reg.username = username
		reg.password = password
	}
}

func WithTimeout(timeout time.Duration) RegistryOption {
	return func(reg *Registry) {
		reg.timeout = timeout
	}
}

func WithInsecureSkipVerifyTLS(insecureSkipVerifyTLS bool) RegistryOption {
	return func(reg *Registry) {
		reg.insecureSkipVerifyTLS = insecureSkipVerifyTLS
	}
}

func WithPlainHTTP() RegistryOption {
	return func(reg *Registry) {
		reg.PlainHTTP = true
	}
}

func (r *Registry) client() remote.Client {
	if r.Client == nil {
		return auth.DefaultClient
	}
	return r.Client
}

func (r *Registry) do(req *http.Request) (*http.Response, error) {
	return r.client().Do(req)
}

func (r *Registry) IsPlainHttp() (bool, error) {
	schemaProbeList := []bool{false, true}

	var err error
	for _, probe := range schemaProbeList {
		r.PlainHTTP = probe
		err = r.Ping(context.Background())
		if err == nil {
			return probe, nil
		}
	}

	return r.PlainHTTP, err
}

func (r *Registry) Ping(ctx context.Context) error {
	url := buildRegistryBaseURL(r.PlainHTTP, r.Reference)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := r.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return errors.New("not found")
	default:
		return ParseErrorResponse(resp)
	}
}

func (r *Registry) Repositories(ctx context.Context, last string, fn func(repos []string) error) error {
	url := buildRegistryCatalogURL(r.PlainHTTP, r.Reference)
	var err error
	for err == nil {
		url, err = r.repositories(ctx, last, fn, url)
		// clear `last` for subsequent pages
		last = ""
	}
	if err != errNoLink {
		return err
	}
	return nil
}

func (r *Registry) repositories(ctx context.Context, last string, fn func(repos []string) error, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	if r.RepositoryListPageSize > 0 || last != "" {
		q := req.URL.Query()
		if r.RepositoryListPageSize > 0 {
			q.Set("n", strconv.Itoa(r.RepositoryListPageSize))
		}
		if last != "" {
			q.Set("last", last)
		}
		req.URL.RawQuery = q.Encode()
	}
	resp, err := r.do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", ParseErrorResponse(resp)
	}
	var page struct {
		Repositories []string `json:"repositories"`
	}
	lr := limitReader(resp.Body, r.MaxMetadataBytes)
	if err := json.NewDecoder(lr).Decode(&page); err != nil {
		return "", fmt.Errorf("%s %q: failed to decode response: %w", resp.Request.Method, resp.Request.URL, err)
	}
	if err := fn(page.Repositories); err != nil {
		return "", err
	}

	return parseLink(resp)
}

func (r *Registry) Repository(ctx context.Context, name string) (registry.Repository, error) {
	ref := registry.Reference{
		Registry:   r.Reference.Registry,
		Repository: name,
	}
	if err := ref.ValidateRepository(); err != nil {
		return nil, err
	}
	repo := r.repository((*remote.Repository)(&r.RepositoryOptions))
	repo.Reference = ref
	return repo, nil
}

// FetchManifest returns the OCI manifest for a tag without downloading its layers.
func (r *Registry) FetchManifest(ctx context.Context, repository, tag string) (ocispec.Manifest, error) {
	var manifest ocispec.Manifest
	ref := registry.Reference{Registry: r.Reference.Registry, Repository: repository, Reference: tag}
	if err := ref.ValidateReference(); err != nil {
		return manifest, err
	}

	u := url.URL{
		Scheme: buildScheme(r.PlainHTTP),
		Host:   r.Reference.Host(),
		Path:   fmt.Sprintf("/v2/%s/manifests/%s", repository, tag),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return manifest, err
	}
	req.Header.Set("Accept", ocispec.MediaTypeImageManifest)
	resp, err := r.do(req)
	if err != nil {
		return manifest, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return manifest, ParseErrorResponse(resp)
	}
	if err := json.NewDecoder(limitReader(resp.Body, r.MaxMetadataBytes)).Decode(&manifest); err != nil {
		return manifest, err
	}
	return manifest, nil
}

// FetchManifestDescriptor returns the OCI manifest and its registry digest for a tag.
func (r *Registry) FetchManifestDescriptor(ctx context.Context, repository, tag string) (ocispec.Manifest, string, error) {
	var manifest ocispec.Manifest
	ref := registry.Reference{Registry: r.Reference.Registry, Repository: repository, Reference: tag}
	if err := ref.ValidateReference(); err != nil {
		return manifest, "", err
	}

	u := url.URL{
		Scheme: buildScheme(r.PlainHTTP),
		Host:   r.Reference.Host(),
		Path:   fmt.Sprintf("/v2/%s/manifests/%s", repository, tag),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return manifest, "", err
	}
	req.Header.Set("Accept", ocispec.MediaTypeImageManifest)
	resp, err := r.do(req)
	if err != nil {
		return manifest, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return manifest, "", ParseErrorResponse(resp)
	}
	if err := json.NewDecoder(limitReader(resp.Body, r.MaxMetadataBytes)).Decode(&manifest); err != nil {
		return manifest, "", err
	}
	return manifest, resp.Header.Get("Docker-Content-Digest"), nil
}

// FetchBlob downloads a single blob referenced by an OCI manifest.
func (r *Registry) FetchBlob(ctx context.Context, repository string, desc ocispec.Descriptor) ([]byte, error) {
	ref := registry.Reference{Registry: r.Reference.Registry, Repository: repository}
	if err := ref.ValidateRepository(); err != nil {
		return nil, err
	}

	u := url.URL{
		Scheme: buildScheme(r.PlainHTTP),
		Host:   r.Reference.Host(),
		Path:   fmt.Sprintf("/v2/%s/blobs/%s", repository, desc.Digest),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ParseErrorResponse(resp)
	}
	return io.ReadAll(resp.Body)
}

func (r *Registry) repository(repo *remote.Repository) *remote.Repository {
	return &remote.Repository{
		Client:               repo.Client,
		Reference:            repo.Reference,
		PlainHTTP:            repo.PlainHTTP,
		ManifestMediaTypes:   slices.Clone(repo.ManifestMediaTypes),
		TagListPageSize:      repo.TagListPageSize,
		ReferrerListPageSize: repo.ReferrerListPageSize,
		MaxMetadataBytes:     repo.MaxMetadataBytes,
	}
}
