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
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/registry"
	helmrepo "helm.sh/helm/v3/pkg/repo"
	"k8s.io/klog/v2"
	appv2 "kubesphere.io/api/application/v2"

	"kubesphere.io/kubesphere/pkg/simple/client/oci"
)

const ociRequestTimeout = 30 * time.Second

// ErrNotHelmOCIArtifact indicates that an OCI manifest is not a Helm chart.
// Callers can safely skip this artifact without treating it as a repository failure.
var ErrNotHelmOCIArtifact = errors.New("OCI artifact is not a Helm chart")

// OCIIndexWarning identifies one repository or tag that could not be indexed.
// LoadOCIRepoIndex returns these warnings alongside any usable chart entries.
type OCIIndexWarning struct {
	Repository string
	Tag        string
	Err        error
}

func (w *OCIIndexWarning) Error() string {
	if w.Tag == "" {
		return fmt.Sprintf("index OCI repository %s: %v", w.Repository, w.Err)
	}
	return fmt.Sprintf("index OCI artifact %s:%s: %v", w.Repository, w.Tag, w.Err)
}

func (w *OCIIndexWarning) Unwrap() error { return w.Err }

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

func LoadRepoIndexFromOci(u string, cred appv2.RepoCredential) (idx helmrepo.IndexFile, err error) {
	idx, warnings, err := LoadOCIRepoIndex(context.Background(), u, cred)
	if err != nil {
		return idx, err
	}
	if len(idx.Entries) == 0 {
		if len(warnings) > 0 {
			return idx, fmt.Errorf("no valid OCI Helm charts found at %s: %w", u, errors.Join(warnings...))
		}
		return idx, fmt.Errorf("no valid OCI Helm charts found at %s", u)
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
	if !registry.IsOCI(u) {
		return idx, nil, fmt.Errorf("invalid oci URL format: %s", u)
	}
	parsedURL, err := url.Parse(u)
	if err != nil {
		return idx, nil, err
	}
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
		for _, tag := range tags {
			if isAuxiliaryOCITag(tag) {
				continue
			}
			version := strings.ReplaceAll(tag, "_", "+")
			if _, err := semver.StrictNewVersion(version); err != nil {
				continue
			}
			chartVersion, err := inspectOCIChart(ctx, ociRegistry, repoChart, tag)
			if errors.Is(err, ErrNotHelmOCIArtifact) {
				continue
			}
			if err != nil {
				warnings = append(warnings, &OCIIndexWarning{Repository: repoChart, Tag: tag, Err: err})
				continue
			}
			if err := index.MustAdd(chartVersion.Metadata, "", chartVersion.URLs[0], chartVersion.Digest); err != nil {
				warnings = append(warnings, &OCIIndexWarning{Repository: repoChart, Tag: tag, Err: err})
			}
		}
	}
	index.SortEntries()
	return *index, warnings, nil
}

// inspectOCIChart validates Helm media types and reads only the config blob
// containing chart metadata; chart layers are intentionally not downloaded.
func inspectOCIChart(ctx context.Context, reg *oci.Registry, repository, tag string) (*helmrepo.ChartVersion, error) {
	manifest, digest, err := reg.FetchManifestDescriptor(ctx, repository, tag)
	if err != nil {
		return nil, err
	}
	if manifest.Config.MediaType != registry.ConfigMediaType {
		return nil, ErrNotHelmOCIArtifact
	}
	hasChartLayer := false
	for _, layer := range manifest.Layers {
		if layer.MediaType == registry.ChartLayerMediaType || layer.MediaType == registry.LegacyChartLayerMediaType {
			hasChartLayer = true
			break
		}
	}
	if !hasChartLayer {
		return nil, ErrNotHelmOCIArtifact
	}
	config, err := reg.FetchBlob(ctx, repository, manifest.Config)
	if err != nil {
		return nil, err
	}
	metadata := new(chart.Metadata)
	if err := json.Unmarshal(config, metadata); err != nil {
		return nil, fmt.Errorf("decode chart metadata: %w", err)
	}
	metadata.Version = strings.ReplaceAll(tag, "_", "+")
	pullURL := fmt.Sprintf("%s://%s/%s:%s", registry.OCIScheme, reg.Reference.Host(), repository, tag)
	return &helmrepo.ChartVersion{Metadata: metadata, URLs: []string{pullURL}, Digest: digest}, nil
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
	return fmt.Errorf("no valid OCI Helm charts found at %s", u)
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
