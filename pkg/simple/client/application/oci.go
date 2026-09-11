/*
 * Copyright 2024 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 * https://github.com/kubesphere/kubesphere/blob/master/LICENSE
 */

package application

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/registry"
	helmrepo "helm.sh/helm/v3/pkg/repo"
	"k8s.io/klog/v2"
	appv2 "kubesphere.io/api/application/v2"

	"kubesphere.io/kubesphere/pkg/constants"
	"kubesphere.io/kubesphere/pkg/simple/client/oci"
)

const ociRequestTimeout = 30 * time.Second

func HelmPullFromOci(u string, cred appv2.RepoCredential) ([]byte, error) {
	if !registry.IsOCI(u) {
		return nil, fmt.Errorf("invalid oci URL format: %s", u)
	}
	_, err := url.Parse(u)
	if err != nil {
		klog.Errorf("invalid oci chart URL format: %s, err:%v", u, err)
		return nil, err
	}

	client, err := newOCIRegistryClient(u, cred)
	if err != nil {
		return nil, err
	}

	pullRef := strings.TrimPrefix(u, fmt.Sprintf("%s://", registry.OCIScheme))
	pullResult, err := client.Pull(pullRef)
	if err != nil {
		klog.Errorf("An error occurred to pull chart from repository: %s,err:%v", pullRef, err)
		return nil, err
	}

	return pullResult.Chart.Data, nil
}

// OCIChartVersionCache maps an OCI tag to a previously synchronized chart version.
type OCIChartVersionCache map[string]*helmrepo.ChartVersion

// OCIIndexState is the durable metadata cache for a paged OCI repository sync.
type OCIIndexState struct {
	Source      string              `json:"source"`
	Index       helmrepo.IndexFile  `json:"index"`
	IgnoredTags map[string]struct{} `json:"ignoredTags,omitempty"`
}

// NewOCIIndexState creates an empty OCI index cache for a repository.
func NewOCIIndexState(source string) *OCIIndexState {
	return &OCIIndexState{
		Source:      source,
		Index:       *helmrepo.NewIndexFile(),
		IgnoredTags: make(map[string]struct{}),
	}
}

// MergeOCIChartVersionCache adds previously synchronized versions to an OCI index state.
func MergeOCIChartVersionCache(index *helmrepo.IndexFile, cached OCIChartVersionCache) {
	if index == nil {
		return
	}
	if index.Entries == nil {
		index.Entries = make(map[string]helmrepo.ChartVersions)
	}
	for _, version := range cached {
		if version != nil && version.Metadata != nil {
			index.Entries[version.Name] = append(index.Entries[version.Name], version)
		}
	}
}

// BuildOCIChartVersionCache rebuilds OCI tag metadata from synchronized application versions.
func BuildOCIChartVersionCache(apps []appv2.Application, versions []appv2.ApplicationVersion) OCIChartVersionCache {
	appMetadata := make(map[string]*chart.Metadata, len(apps))
	for i := range apps {
		app := &apps[i]
		name := app.Annotations[appv2.AppOriginalNameLabelKey]
		if name == "" {
			continue
		}
		appMetadata[app.Name] = &chart.Metadata{
			Name:        name,
			Home:        app.Spec.AppHome,
			Icon:        app.Spec.Icon,
			Description: app.Annotations[constants.DescriptionAnnotationKey],
		}
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
		if description := version.Annotations[constants.DescriptionAnnotationKey]; description != "" {
			cachedMetadata.Description = description
		}
		cachedMetadata.Maintainers = chartMaintainers(version.Spec.Maintainer)
		cache[ociCacheKey(host, repository, tag)] = &helmrepo.ChartVersion{
			Metadata: &cachedMetadata,
			URLs:     []string{version.Spec.PullUrl},
			Digest:   version.Spec.Digest,
			Created:  version.CreationTimestamp.Time,
		}
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

func ociCacheKey(host, repository, tag string) string {
	return host + "/" + repository + ":" + tag
}

func chartMaintainers(maintainers []appv2.Maintainer) []*chart.Maintainer {
	result := make([]*chart.Maintainer, 0, len(maintainers))
	for _, maintainer := range maintainers {
		result = append(result, &chart.Maintainer{Name: maintainer.Name, Email: maintainer.Email, URL: maintainer.URL})
	}
	return result
}

func LoadRepoIndexFromOci(u string, cred appv2.RepoCredential) (idx helmrepo.IndexFile, err error) {
	return LoadRepoIndexFromOciWithCache(u, cred, nil)
}

// ValidateOCIRepository checks connectivity, tags, and one newest Helm chart artifact.
func ValidateOCIRepository(u string, cred appv2.RepoCredential) error {
	if !registry.IsOCI(u) {
		return fmt.Errorf("invalid oci URL format: %s", u)
	}
	parsedURL, err := url.Parse(u)
	if err != nil {
		return err
	}
	repoCharts, err := GetRepoChartsFromOci(parsedURL, cred)
	if err != nil {
		return err
	}
	if len(repoCharts) == 0 {
		return fmt.Errorf("no OCI chart repositories found at %s", u)
	}
	reg, err := newOCIRegistry(u, cred)
	if err != nil {
		return err
	}
	for _, repository := range repoCharts {
		tags, err := getOCITags(context.Background(), reg, repository)
		if err != nil {
			return err
		}
		sort.Slice(tags, func(i, j int) bool { return ociTagNewer(tags[i], tags[j]) })
		for _, tag := range tags {
			if isAuxiliaryOCITag(tag) {
				continue
			}
			if _, _, err := loadOCIChartMetadata(context.Background(), reg, repository, tag); err != nil {
				if errors.Is(err, errNotHelmChartArtifact) {
					continue
				}
				return err
			}
			return nil
		}
	}
	return fmt.Errorf("no Helm chart artifacts found at %s", u)
}

func LoadRepoIndexFromOciWithCache(u string, cred appv2.RepoCredential, cached OCIChartVersionCache) (idx helmrepo.IndexFile, err error) {
	if !registry.IsOCI(u) {
		return idx, fmt.Errorf("invalid oci URL format: %s", u)
	}

	parsedURL, err := url.Parse(u)
	if err != nil {
		klog.Errorf("invalid repo URL format: %s, err:%v", u, err)
		return idx, err
	}

	repoCharts, err := GetRepoChartsFromOci(parsedURL, cred)
	if err != nil {
		return idx, err
	}
	if len(repoCharts) == 0 {
		return idx, nil
	}

	ociRegistry, err := newOCIRegistry(u, cred)
	if err != nil {
		return idx, err
	}

	index := helmrepo.NewIndexFile()
	for _, repoChart := range repoCharts {
		tags, err := getOCITags(context.Background(), ociRegistry, repoChart)
		if err != nil {
			klog.Errorf("An error occurred to load tags from repository: %s/%s,err:%v", parsedURL.Host, repoChart, err)
			continue
		}
		if len(tags) == 0 {
			klog.Errorf("Unable to locate any tags in provided repository: %s/%s,err:%v", parsedURL.Host, repoChart, err)
			continue
		}

		for _, tag := range tags {
			if isAuxiliaryOCITag(tag) {
				continue
			}
			if cachedVersion, found := cached[ociCacheKey(parsedURL.Host, repoChart, tag)]; found && cachedVersion.Metadata != nil {
				index.Entries[cachedVersion.Name] = append(index.Entries[cachedVersion.Name], cachedVersion)
				continue
			}

			metadata, chartDigest, err := loadOCIChartMetadata(context.Background(), ociRegistry, repoChart, tag)
			if err != nil {
				if errors.Is(err, errNotHelmChartArtifact) {
					continue
				}
				return idx, fmt.Errorf("load OCI chart metadata for %s/%s:%s: %w", parsedURL.Host, repoChart, tag, err)
			}

			pullRef := fmt.Sprintf("%s/%s:%s", parsedURL.Host, repoChart, tag)
			baseURL := fmt.Sprintf("%s://%s", registry.OCIScheme, pullRef)
			hash := strings.TrimPrefix(chartDigest, "sha256:")
			if err := index.MustAdd(metadata, "", baseURL, hash); err != nil {
				return idx, fmt.Errorf("add OCI chart metadata for %s: %w", pullRef, err)
			}
		}
	}

	index.SortEntries()

	return *index, nil
}

// SyncOCIIndexPage adds at most batchSize uncached OCI chart tags to state.
// A non-positive batchSize scans all currently uncached tags.
func SyncOCIIndexPage(u string, cred appv2.RepoCredential, state *OCIIndexState, batchSize int) (bool, error) {
	if state == nil {
		return false, errors.New("missing OCI index state")
	}
	if state.Source != "" && state.Source != u {
		*state = *NewOCIIndexState(u)
	}
	if state.Index.Entries == nil {
		state.Index = *helmrepo.NewIndexFile()
	}
	if state.IgnoredTags == nil {
		state.IgnoredTags = make(map[string]struct{})
	}

	parsedURL, err := url.Parse(u)
	if err != nil || !registry.IsOCI(u) {
		return false, fmt.Errorf("invalid oci URL format: %s", u)
	}
	repoCharts, err := GetRepoChartsFromOci(parsedURL, cred)
	if err != nil {
		return false, err
	}
	if len(repoCharts) == 0 {
		return false, fmt.Errorf("no OCI chart repositories found at %s", u)
	}
	ociRegistry, err := newOCIRegistry(u, cred)
	if err != nil {
		return false, err
	}

	cached := ociChartVersionCacheFromIndex(&state.Index)
	index := helmrepo.NewIndexFile()
	currentTags := make(map[string]struct{})
	type candidate struct {
		repository string
		tag        string
	}
	var candidates []candidate
	for _, repoChart := range repoCharts {
		tags, err := getOCITags(context.Background(), ociRegistry, repoChart)
		if err != nil {
			return false, fmt.Errorf("load OCI tags for %s/%s: %w", parsedURL.Host, repoChart, err)
		}
		for _, tag := range tags {
			key := ociCacheKey(parsedURL.Host, repoChart, tag)
			currentTags[key] = struct{}{}
			if isAuxiliaryOCITag(tag) {
				state.IgnoredTags[key] = struct{}{}
				continue
			}
			if cachedVersion, found := cached[key]; found && cachedVersion.Metadata != nil {
				index.Entries[cachedVersion.Name] = append(index.Entries[cachedVersion.Name], cachedVersion)
				continue
			}
			if _, ignored := state.IgnoredTags[key]; ignored {
				continue
			}
			candidates = append(candidates, candidate{repository: repoChart, tag: tag})
		}
	}
	for key := range state.IgnoredTags {
		if _, found := currentTags[key]; !found {
			delete(state.IgnoredTags, key)
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		return ociTagNewer(candidates[i].tag, candidates[j].tag)
	})
	processed := 0
	for _, item := range candidates {
		if batchSize > 0 && processed >= batchSize {
			break
		}
		processed++
		metadata, chartDigest, err := loadOCIChartMetadata(context.Background(), ociRegistry, item.repository, item.tag)
		if err != nil {
			if errors.Is(err, errNotHelmChartArtifact) {
				state.IgnoredTags[ociCacheKey(parsedURL.Host, item.repository, item.tag)] = struct{}{}
				continue
			}
			return false, fmt.Errorf("load OCI chart metadata for %s/%s:%s: %w", parsedURL.Host, item.repository, item.tag, err)
		}
		pullRef := fmt.Sprintf("%s/%s:%s", parsedURL.Host, item.repository, item.tag)
		if err := index.MustAdd(metadata, "", fmt.Sprintf("%s://%s", registry.OCIScheme, pullRef), strings.TrimPrefix(chartDigest, "sha256:")); err != nil {
			return false, fmt.Errorf("add OCI chart metadata for %s: %w", pullRef, err)
		}
	}
	index.SortEntries()
	state.Source = u
	state.Index = *index
	return len(candidates) <= processed, nil
}

func ociChartVersionCacheFromIndex(index *helmrepo.IndexFile) OCIChartVersionCache {
	cache := make(OCIChartVersionCache)
	if index == nil {
		return cache
	}
	for _, versions := range index.Entries {
		for _, version := range versions {
			for _, pullURL := range version.URLs {
				host, repository, tag, found := ociReferenceFromPullURL(pullURL)
				if found {
					cache[ociCacheKey(host, repository, tag)] = version
				}
			}
		}
	}
	return cache
}

func ociTagNewer(left, right string) bool {
	leftVersion, leftErr := semver.NewVersion(strings.ReplaceAll(left, "_", "+"))
	rightVersion, rightErr := semver.NewVersion(strings.ReplaceAll(right, "_", "+"))
	if leftErr == nil && rightErr == nil {
		return leftVersion.GreaterThan(rightVersion)
	}
	if leftErr == nil {
		return true
	}
	if rightErr == nil {
		return false
	}
	return left > right
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

var errNotHelmChartArtifact = errors.New("not a Helm chart artifact")

func isAuxiliaryOCITag(tag string) bool {
	return strings.HasSuffix(tag, "-metadata")
}

func loadOCIChartMetadata(ctx context.Context, reg *oci.Registry, repository, tag string) (*chart.Metadata, string, error) {
	manifest, err := reg.FetchManifest(ctx, repository, tag)
	if err != nil {
		return nil, "", err
	}
	if manifest.Config.MediaType != registry.ConfigMediaType {
		return nil, "", errNotHelmChartArtifact
	}

	var chartLayer *ocispec.Descriptor
	for i := range manifest.Layers {
		layer := &manifest.Layers[i]
		if layer.MediaType == registry.ChartLayerMediaType || layer.MediaType == registry.LegacyChartLayerMediaType {
			chartLayer = layer
			break
		}
	}
	if chartLayer == nil {
		return nil, "", errNotHelmChartArtifact
	}

	config, err := reg.FetchBlob(ctx, repository, manifest.Config)
	if err != nil {
		return nil, "", err
	}
	metadata := &chart.Metadata{}
	if err := json.Unmarshal(config, metadata); err != nil {
		return nil, "", err
	}
	return metadata, chartLayer.Digest.String(), nil
}

func GetRepoChartsFromOci(parsedURL *url.URL, cred appv2.RepoCredential) ([]string, error) {
	if parsedURL == nil {
		return nil, errors.New("missing parsedURL")
	}

	skipTLS := true
	if cred.InsecureSkipTLSVerify != nil && !*cred.InsecureSkipTLSVerify {
		skipTLS = false
	}

	options := []oci.RegistryOption{
		oci.WithTimeout(ociRequestTimeout),
		oci.WithBasicAuth(cred.Username, cred.Password),
		oci.WithInsecureSkipVerifyTLS(skipTLS),
	}
	if cred.PlainHTTP {
		options = append(options, oci.WithPlainHTTP())
	}

	reg, err := oci.NewRegistry(parsedURL.Host, options...)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()

	repoPath := strings.TrimSuffix(parsedURL.Path, "/")
	repoPath = strings.TrimPrefix(repoPath, "/")
	if repoPath != "" {
		repo, err := reg.Repository(ctx, repoPath)
		if err == nil {
			var tags []string
			err = repo.Tags(ctx, func(ts []string) error {
				tags = append(tags, ts...)
				return nil
			})
			if err == nil {
				if len(tags) == 0 {
					return nil, nil
				}
				return []string{repoPath}, nil
			}
			if !isOCIRepositoryNotFound(err) {
				return nil, err
			}
		}
	}

	var repoCharts []string
	err = reg.Repositories(ctx, "", func(repos []string) error {
		if repoPath == "" {
			repoCharts = append(repoCharts, repos...)
			return nil
		}

		cutPrefix := repoPath + "/"
		for _, repo := range repos {
			if subRepo, found := strings.CutPrefix(repo, cutPrefix); found && subRepo != "" {
				if !strings.Contains(subRepo, "/") {
					repoCharts = append(repoCharts, fmt.Sprintf("%s/%s", repoPath, subRepo))
				}
			}
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	return repoCharts, nil
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
		klog.Errorf("invalid oci repo URL format: %s, err:%v", u, err)
		return nil, err
	}

	reg, err := newOCIRegistry(u, cred)
	if err != nil {
		return nil, err
	}
	skipTLS := true
	if cred.InsecureSkipTLSVerify != nil && !*cred.InsecureSkipTLSVerify {
		skipTLS = false
	}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: skipTLS},
		Proxy:           http.ProxyFromEnvironment,
	}

	opts := []registry.ClientOption{registry.ClientOptHTTPClient(&http.Client{
		Transport: transport,
		Timeout:   5 * time.Second,
	})}

	if reg.PlainHTTP {
		opts = append(opts, registry.ClientOptPlainHTTP())
	}

	client, err := registry.NewClient(opts...)

	if err != nil {
		return nil, err
	}

	if cred.Username != "" || cred.Password != "" {
		err = client.Login(parsedURL.Host,
			registry.LoginOptBasicAuth(cred.Username, cred.Password),
			registry.LoginOptInsecure(reg.PlainHTTP))

		if err != nil {
			return nil, err
		}
	}
	return client, nil
}

func newOCIRegistry(u string, cred appv2.RepoCredential) (*oci.Registry, error) {
	parsedURL, err := url.Parse(u)
	if err != nil {
		return nil, err
	}

	skipTLS := true
	if cred.InsecureSkipTLSVerify != nil && !*cred.InsecureSkipTLSVerify {
		skipTLS = false
	}

	options := []oci.RegistryOption{
		oci.WithTimeout(ociRequestTimeout),
		oci.WithBasicAuth(cred.Username, cred.Password),
		oci.WithInsecureSkipVerifyTLS(skipTLS),
	}
	if cred.PlainHTTP {
		options = append(options, oci.WithPlainHTTP())
	}

	return oci.NewRegistry(parsedURL.Host, options...)
}
