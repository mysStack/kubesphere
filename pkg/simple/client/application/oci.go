package application

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/registry"
	helmrepo "helm.sh/helm/v3/pkg/repo"
	"k8s.io/klog/v2"
	appv2 "kubesphere.io/api/application/v2"
	ksconstants "kubesphere.io/kubesphere/pkg/constants"

	"kubesphere.io/kubesphere/pkg/simple/client/oci"
)

const (
	ociRequestTimeout      = 30 * time.Second
	ociMetadataConcurrency = 4
)

// ErrNotHelmOCIArtifact indicates that an OCI manifest is not a Helm chart.
// Callers can safely skip this artifact without treating it as a repository failure.
var ErrNotHelmOCIArtifact = errors.New("OCI artifact is not a Helm chart")

// OCIIndexWarning identifies one repository or tag that could not be indexed.
// LoadOCIRepoIndex returns these warnings alongside any usable chart entries.
type OCIIndexWarning struct {
	Repository string
	Tag        string
	Digest     string
	Err        error
}

func (w *OCIIndexWarning) Error() string {
	if w.Tag == "" {
		return fmt.Sprintf("index OCI repository %s: %v", w.Repository, w.Err)
	}
	if w.Digest != "" {
		return fmt.Sprintf("index OCI artifact %s:%s (%s): %v", w.Repository, w.Tag, w.Digest, w.Err)
	}
	return fmt.Sprintf("index OCI artifact %s:%s: %v", w.Repository, w.Tag, w.Err)
}

func (w *OCIIndexWarning) Unwrap() error { return w.Err }

func HelmPullFromOci(u string, cred appv2.RepoCredential) ([]byte, error) {
	safeURL := SanitizeOCIURL(u)
	if !registry.IsOCI(u) {
		return nil, fmt.Errorf("invalid oci URL format: %s", safeURL)
	}
	_, err := url.Parse(u)
	if err != nil {
		klog.Errorf("invalid oci chart URL format: %s", safeURL)
		return nil, errors.New("invalid OCI chart URL")
	}

	client, err := newOCIRegistryClient(safeURL, cred)
	if err != nil {
		return nil, err
	}

	pullRef := strings.TrimPrefix(safeURL, fmt.Sprintf("%s://", registry.OCIScheme))
	pullResult, err := client.Pull(pullRef)
	if err != nil {
		klog.Errorf("An error occurred to pull chart from repository: %s,err:%v", pullRef, err)
		return nil, err
	}

	return pullResult.Chart.Data, nil
}

func LoadRepoIndexFromOci(u string, cred appv2.RepoCredential) (idx helmrepo.IndexFile, err error) {
	safeURL := SanitizeOCIURL(u)
	idx, warnings, err := LoadOCIRepoIndex(context.Background(), u, cred)
	if err != nil {
		return idx, err
	}
	if len(idx.Entries) == 0 {
		if len(warnings) > 0 {
			return idx, fmt.Errorf("no valid OCI Helm charts found at %s: %w", safeURL, errors.Join(warnings...))
		}
		return idx, fmt.Errorf("no valid OCI Helm charts found at %s", safeURL)
	}
	return idx, nil
}

// LoadRepoIndexFromOciTags is retained for compatibility and now performs full
// Helm OCI artifact inspection.
func LoadRepoIndexFromOciTags(u string, cred appv2.RepoCredential) (idx helmrepo.IndexFile, err error) {
	return LoadRepoIndexFromOci(u, cred)
}

// LoadOCIRepoIndex discovers repositories and inspects their Helm artifacts.
// Warnings contain per-artifact failures and do not prevent usable entries
// from being returned.
func LoadOCIRepoIndex(ctx context.Context, u string, cred appv2.RepoCredential) (idx helmrepo.IndexFile, warnings []error, err error) {
	return LoadOCIRepoIndexWithCache(ctx, u, cred, nil)
}

// OCIChartVersionCache maps an original OCI reference to cached chart metadata.
type OCIChartVersionCache map[string]*helmrepo.ChartVersion

// BuildOCIChartVersionCache builds cache entries from synchronized Applications and ApplicationVersions.
func BuildOCIChartVersionCache(apps []appv2.Application, versions []appv2.ApplicationVersion) OCIChartVersionCache {
	appMetadata := make(map[string]*chart.Metadata, len(apps))
	for i := range apps {
		app := &apps[i]
		name := app.Annotations[appv2.AppOriginalNameLabelKey]
		if name == "" {
			continue
		}
		appMetadata[app.Name] = &chart.Metadata{Name: name, Home: app.Spec.AppHome, Icon: app.Spec.Icon, Description: app.Annotations[ksconstants.DescriptionAnnotationKey]}
	}
	cache := make(OCIChartVersionCache)
	for i := range versions {
		version := &versions[i]
		metadata, found := appMetadata[version.Labels[appv2.AppIDLabelKey]]
		if !found {
			continue
		}
		host, repository, tag, found := ociReferenceFromPullURL(version.Spec.PullUrl)
		if !found {
			continue
		}
		cachedMetadata := *metadata
		cachedMetadata.Version = version.Spec.VersionName
		cachedMetadata.Home = version.Spec.AppHome
		cachedMetadata.Icon = version.Spec.Icon
		if description := version.Annotations[ksconstants.DescriptionAnnotationKey]; description != "" {
			cachedMetadata.Description = description
		}
		cachedMetadata.Maintainers = chartMaintainers(version.Spec.Maintainer)
		cache[ociCacheKey(host, repository, tag)] = &helmrepo.ChartVersion{Metadata: &cachedMetadata, URLs: []string{version.Spec.PullUrl}, Digest: version.Spec.Digest, Created: version.CreationTimestamp.Time}
	}
	return cache
}

func ociReferenceFromPullURL(pullURL string) (host, repository, tag string, found bool) {
	u, err := url.Parse(pullURL)
	if err != nil || !registry.IsOCI(pullURL) {
		return "", "", "", false
	}
	reference := strings.TrimPrefix(u.Path, "/")
	separator := strings.LastIndex(reference, ":")
	if u.Host == "" || separator <= 0 || separator == len(reference)-1 {
		return "", "", "", false
	}
	return u.Host, reference[:separator], reference[separator+1:], true
}

func ociCacheKey(host, repository, tag string) string { return host + "/" + repository + ":" + tag }

// SanitizeOCIURL removes embedded credentials before the URL is persisted,
// returned in an error, or logged.
func SanitizeOCIURL(rawURL string) string {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "<invalid OCI URL>"
	}
	parsedURL.User = nil
	return parsedURL.String()
}

func chartMaintainers(maintainers []appv2.Maintainer) []*chart.Maintainer {
	result := make([]*chart.Maintainer, 0, len(maintainers))
	for _, maintainer := range maintainers {
		result = append(result, &chart.Maintainer{Name: maintainer.Name, Email: maintainer.Email, URL: maintainer.URL})
	}
	return result
}

// LoadOCIRepoIndexWithCache loads OCI metadata, reusing complete cached versions by tag.
func LoadOCIRepoIndexWithCache(ctx context.Context, u string, cred appv2.RepoCredential, cached OCIChartVersionCache) (idx helmrepo.IndexFile, warnings []error, err error) {
	if !registry.IsOCI(u) {
		return idx, nil, fmt.Errorf("invalid oci URL format: %s", SanitizeOCIURL(u))
	}
	parsedURL, err := url.Parse(u)
	if err != nil {
		return idx, nil, errors.New("invalid OCI URL")
	}
	parsedURL.User = nil
	u = parsedURL.String()
	repoCharts, err := DiscoverOCIRepositories(ctx, parsedURL, cred)
	if err != nil {
		return idx, nil, err
	}
	ociRegistry, err := newOCIRegistry(u, cred)
	if err != nil {
		return idx, nil, err
	}

	index := helmrepo.NewIndexFile()
	for _, repoChart := range repoCharts {
		tags, err := getOCITags(ctx, ociRegistry, repoChart)
		if err != nil {
			warnings = append(warnings, &OCIIndexWarning{Repository: repoChart, Err: fmt.Errorf("load OCI tags: %w", err)})
			continue
		}
		directRepository := len(repoCharts) == 1 && repoChart == ociRepositoryPath(parsedURL)
		chartVersions, inspectionWarnings := loadOCIChartVersions(ctx, ociRegistry, parsedURL.Host, repoChart, semanticOCITags(tags), cached)
		for _, warning := range inspectionWarnings {
			if directRepository || !errors.Is(warning, ErrNotHelmOCIArtifact) {
				warnings = append(warnings, warning)
			}
		}
		for _, chartVersion := range chartVersions {
			if err := index.MustAdd(chartVersion.Metadata, "", chartVersion.URLs[0], chartVersion.Digest); err != nil {
				warnings = append(warnings, &OCIIndexWarning{Repository: repoChart, Tag: chartVersion.Version, Digest: chartVersion.Digest, Err: err})
			}
		}
	}
	index.SortEntries()
	return *index, warnings, nil
}

// loadOCIChartVersions adds cached versions without metadata requests and inspects only uncached tags.
func loadOCIChartVersions(ctx context.Context, reg *oci.Registry, host, repository string, tags []string, cached OCIChartVersionCache) (versions []*helmrepo.ChartVersion, warnings []error) {
	tags = append([]string(nil), tags...)
	sort.Strings(tags)
	missingTags := make([]string, 0, len(tags))
	for _, tag := range tags {
		if version := cached[ociCacheKey(host, repository, tag)]; version != nil {
			versions = append(versions, cloneChartVersionForTag(version, tag))
			continue
		}
		missingTags = append(missingTags, tag)
	}
	if len(missingTags) == 0 {
		return versions, nil
	}

	type inspectionResult struct {
		tag     string
		version *helmrepo.ChartVersion
		digest  string
		err     error
	}
	jobs := make(chan string, len(missingTags))
	results := make(chan inspectionResult, len(missingTags))
	var workers sync.WaitGroup
	for range ociMetadataConcurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				tag, ok := <-jobs
				if !ok {
					return
				}
				version, digest, err := inspectOCIChart(ctx, reg, repository, tag)
				results <- inspectionResult{tag: tag, version: version, digest: digest, err: err}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, tag := range missingTags {
			select {
			case <-ctx.Done():
				return
			case jobs <- tag:
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	inspectionResults := make([]inspectionResult, 0, len(missingTags))
	for result := range results {
		inspectionResults = append(inspectionResults, result)
	}
	sort.Slice(inspectionResults, func(i, j int) bool {
		return inspectionResults[i].tag < inspectionResults[j].tag
	})
	for _, result := range inspectionResults {
		if result.err != nil {
			warnings = append(warnings, &OCIIndexWarning{Repository: repository, Tag: result.tag, Digest: result.digest, Err: result.err})
			continue
		}
		versions = append(versions, result.version)
	}
	sort.Slice(versions, func(i, j int) bool {
		return ociChartVersionTag(versions[i]) < ociChartVersionTag(versions[j])
	})
	return versions, warnings
}

func ociChartVersionTag(version *helmrepo.ChartVersion) string {
	if len(version.URLs) > 0 {
		_, _, tag, found := ociReferenceFromPullURL(version.URLs[0])
		if found {
			return tag
		}
	}
	return version.Version
}

func semanticOCITags(tags []string) []string {
	result := make([]string, 0, len(tags))
	for _, tag := range tags {
		if isAuxiliaryOCITag(tag) {
			continue
		}
		if _, err := semver.StrictNewVersion(strings.ReplaceAll(tag, "_", "+")); err == nil {
			result = append(result, tag)
		}
	}
	return result
}

func cloneChartVersionForTag(source *helmrepo.ChartVersion, tag string) *helmrepo.ChartVersion {
	result := *source
	metadata := *source.Metadata
	metadata.Version = strings.ReplaceAll(tag, "_", "+")
	result.Metadata = &metadata
	result.URLs = []string{source.URLs[0][:strings.LastIndex(source.URLs[0], ":")+1] + tag}
	return &result
}

// inspectOCIChart validates Helm media types and reads only the config blob
// containing chart metadata; chart layers are intentionally not downloaded.
func inspectOCIChart(ctx context.Context, reg *oci.Registry, repository, tag string) (*helmrepo.ChartVersion, string, error) {
	manifest, digest, err := reg.FetchManifestDescriptor(ctx, repository, tag)
	if err != nil {
		return nil, digest, err
	}
	if manifest.Config.MediaType != registry.ConfigMediaType {
		return nil, digest, ErrNotHelmOCIArtifact
	}
	hasChartLayer := false
	for _, layer := range manifest.Layers {
		if layer.MediaType == registry.ChartLayerMediaType || layer.MediaType == registry.LegacyChartLayerMediaType {
			hasChartLayer = true
			break
		}
	}
	if !hasChartLayer {
		return nil, digest, ErrNotHelmOCIArtifact
	}
	config, err := reg.FetchBlob(ctx, repository, manifest.Config)
	if err != nil {
		return nil, digest, err
	}
	metadata := new(chart.Metadata)
	if err := json.Unmarshal(config, metadata); err != nil {
		return nil, digest, fmt.Errorf("decode chart metadata: %w", err)
	}
	metadata.Version = strings.ReplaceAll(tag, "_", "+")
	pullURL := fmt.Sprintf("%s://%s/%s:%s", registry.OCIScheme, reg.Reference.Host(), repository, tag)
	return &helmrepo.ChartVersion{Metadata: metadata, URLs: []string{pullURL}, Digest: digest}, digest, nil
}

// ValidateOCIRepository checks connectivity and that the repository exposes at least one Helm chart.
func ValidateOCIRepository(u string, cred appv2.RepoCredential) error {
	index, err := LoadRepoIndexFromOci(u, cred)
	if err != nil {
		return err
	}
	for _, versions := range index.Entries {
		if len(versions) > 0 {
			return nil
		}
	}
	return fmt.Errorf("no valid OCI Helm charts found at %s", SanitizeOCIURL(u))
}

func getOCITags(ctx context.Context, reg *oci.Registry, repository string) ([]string, error) {
	repo, err := reg.Repository(ctx, repository)
	if err != nil {
		return nil, err
	}
	var tags []string
	if err := repo.Tags(ctx, func(page []string) error {
		tags = append(tags, page...)
		return nil
	}); err != nil {
		return nil, err
	}
	return tags, nil
}

func isAuxiliaryOCITag(tag string) bool {
	return strings.HasSuffix(tag, "-metadata")
}

func isOCIRepositoryNotFound(err error) bool {
	if err == nil {
		return false
	}

	errMsg := strings.ToLower(err.Error())
	return strings.Contains(errMsg, "not found") ||
		strings.Contains(errMsg, "name unknown") ||
		strings.Contains(errMsg, "repository name not known")
}

func newOCIRegistryClient(u string, cred appv2.RepoCredential) (*registry.Client, error) {
	parsedURL, err := url.Parse(u)
	if err != nil {
		klog.Errorf("invalid oci repo URL format: %s", SanitizeOCIURL(u))
		return nil, errors.New("invalid OCI repository URL")
	}
	parsedURL.User = nil

	tlsConfig, err := newOCITLSConfig(cred)
	if err != nil {
		return nil, err
	}

	transport := &http.Transport{
		TLSClientConfig: tlsConfig,
		Proxy:           http.ProxyFromEnvironment,
	}

	opts := []registry.ClientOption{registry.ClientOptHTTPClient(&http.Client{
		Transport: transport,
		Timeout:   5 * time.Second,
	})}

	if cred.PlainHTTP {
		opts = append(opts, registry.ClientOptPlainHTTP())
	}

	client, err := registry.NewClient(opts...)

	if err != nil {
		return nil, err
	}

	if cred.Username != "" || cred.Password != "" {
		err = client.Login(parsedURL.Host,
			registry.LoginOptBasicAuth(cred.Username, cred.Password))

		if err != nil {
			return nil, err
		}
	}
	return client, nil
}

func newOCIRegistry(u string, cred appv2.RepoCredential) (*oci.Registry, error) {
	parsedURL, err := url.Parse(u)
	if err != nil {
		return nil, errors.New("invalid OCI repository URL")
	}
	parsedURL.User = nil

	tlsConfig, err := newOCITLSConfig(cred)
	if err != nil {
		return nil, err
	}

	options := []oci.RegistryOption{
		oci.WithTimeout(ociRequestTimeout),
		oci.WithBasicAuth(cred.Username, cred.Password),
		oci.WithTLSClientConfig(tlsConfig),
	}
	if cred.PlainHTTP {
		options = append(options, oci.WithPlainHTTP())
	}

	return oci.NewRegistry(parsedURL.Host, options...)
}

func newOCITLSConfig(cred appv2.RepoCredential) (*tls.Config, error) {
	skipTLS := true
	if cred.InsecureSkipTLSVerify != nil {
		skipTLS = *cred.InsecureSkipTLSVerify
	}
	config := &tls.Config{InsecureSkipVerify: skipTLS}
	if cred.CAFile != "" {
		data, err := os.ReadFile(cred.CAFile)
		if err != nil {
			return nil, fmt.Errorf("load CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("load CA file: no certificates found")
		}
		config.RootCAs = pool
	}
	if cred.CertFile != "" || cred.KeyFile != "" {
		if cred.CertFile == "" || cred.KeyFile == "" {
			return nil, fmt.Errorf("client certificate and key must both be specified")
		}
		cert, err := tls.LoadX509KeyPair(cred.CertFile, cred.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load client certificate: %w", err)
		}
		config.Certificates = []tls.Certificate{cert}
	}
	return config, nil
}
