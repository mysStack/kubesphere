package oci

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/pkg/registry"
	"oras.land/oras-go/pkg/registry/remote"
	"oras.land/oras-go/pkg/registry/remote/auth"
)

type RepositoryOptions remote.Repository
type RegistryOption func(*Registry)

const retryAttempts = 3

var retryDelays = []time.Duration{200 * time.Millisecond, 500 * time.Millisecond}

// registryResponseError preserves registry response details for callers that
// need to distinguish transient responses from permanent failures.
type registryResponseError struct {
	StatusCode int
	RetryAfter *time.Duration
	err        error
}

func (e *registryResponseError) Error() string {
	return e.err.Error()
}

func (e *registryResponseError) Unwrap() error {
	return e.err
}

type retryClient struct {
	client remote.Client
}

func (c retryClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := retry(req.Context(), retryAttempts, func() (*http.Response, error) {
		return c.client.Do(req)
	})
	if err != nil || resp == nil || resp.StatusCode < http.StatusBadRequest {
		return resp, err
	}
	err = newRegistryResponseError(resp)
	resp.Body.Close()
	return nil, err
}

// Registry is an HTTP client to a remote registry by oras-go 2.x.
// Registry with authentication requires an administrator account.
type Registry struct {
	RepositoryOptions

	RepositoryListPageSize int

	username              string
	password              string
	timeout               time.Duration
	insecureSkipVerifyTLS bool
	tlsClientConfig       *tls.Config
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
			Transport: &http.Transport{TLSClientConfig: reg.tlsConfig(), Proxy: http.ProxyFromEnvironment},
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

func (r *Registry) tlsConfig() *tls.Config {
	if r.tlsClientConfig != nil {
		return r.tlsClientConfig.Clone()
	}
	return &tls.Config{InsecureSkipVerify: r.insecureSkipVerifyTLS}
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

// WithTLSClientConfig configures CA roots and client certificates for registry requests.
func WithTLSClientConfig(config *tls.Config) RegistryOption {
	return func(reg *Registry) {
		reg.tlsClientConfig = config
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

func (r *Registry) doWithRetry(req *http.Request) (*http.Response, error) {
	return retry(req.Context(), retryAttempts, func() (*http.Response, error) {
		return r.do(req)
	})
}

func retry(ctx context.Context, attempts int, operation func() (*http.Response, error)) (*http.Response, error) {
	if attempts < 1 {
		attempts = 1
	}

	for attempt := 0; attempt < attempts; attempt++ {
		resp, err := operation()
		if !isRetryableResponse(resp) && !isRetryableTransportError(err) {
			return resp, err
		}
		if attempt == attempts-1 {
			return resp, err
		}

		delay := retryDelay(resp, attempt)
		if resp != nil {
			resp.Body.Close()
		}
		if err := waitForRetry(ctx, delay); err != nil {
			return nil, err
		}
	}

	return nil, nil
}

func isRetryableResponse(resp *http.Response) bool {
	return resp != nil && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError)
}

func isRetryableTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func retryDelay(resp *http.Response, attempt int) time.Duration {
	if resp != nil {
		if retryAfter, ok := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()); ok {
			return retryAfter
		}
	}
	if attempt < len(retryDelays) {
		return retryDelays[attempt]
	}
	return retryDelays[len(retryDelays)-1]
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	if retryAt, err := http.ParseTime(value); err == nil {
		return max(retryAt.Sub(now), 0), true
	}
	return 0, false
}

func newRegistryResponseError(resp *http.Response) error {
	retryAfter, ok := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	var parsedRetryAfter *time.Duration
	if ok {
		parsedRetryAfter = &retryAfter
	}
	return &registryResponseError{
		StatusCode: resp.StatusCode,
		RetryAfter: parsedRetryAfter,
		err:        ParseErrorResponse(resp),
	}
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

	resp, err := r.doWithRetry(req)
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
		return newRegistryResponseError(resp)
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
	resp, err := r.doWithRetry(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", newRegistryResponseError(resp)
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
	resp, err := r.doWithRetry(req)
	if err != nil {
		return manifest, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return manifest, newRegistryResponseError(resp)
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
	resp, err := r.doWithRetry(req)
	if err != nil {
		return manifest, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return manifest, "", newRegistryResponseError(resp)
	}
	body, err := readBounded(resp.Body, r.MaxMetadataBytes)
	if err != nil {
		return manifest, "", err
	}
	manifestDigest := resp.Header.Get("Docker-Content-Digest")
	if parsed, err := digest.Parse(manifestDigest); err == nil {
		manifestDigest = parsed.String()
	} else {
		manifestDigest = digest.FromBytes(body).String()
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return manifest, manifestDigest, err
	}
	return manifest, manifestDigest, nil
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
	resp, err := r.doWithRetry(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, newRegistryResponseError(resp)
	}
	return readBounded(resp.Body, r.MaxMetadataBytes)
}

func readBounded(reader io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = defaultMaxMetadataBytes
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("response exceeds maximum size of %d bytes", maxBytes)
	}
	return data, nil
}

func (r *Registry) repository(repo *remote.Repository) *remote.Repository {
	return &remote.Repository{
		Client:               retryClient{client: r.client()},
		Reference:            repo.Reference,
		PlainHTTP:            repo.PlainHTTP,
		ManifestMediaTypes:   slices.Clone(repo.ManifestMediaTypes),
		TagListPageSize:      repo.TagListPageSize,
		ReferrerListPageSize: repo.ReferrerListPageSize,
		MaxMetadataBytes:     repo.MaxMetadataBytes,
	}
}
