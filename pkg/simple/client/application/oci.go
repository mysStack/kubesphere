/*
 * Copyright 2024 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 * https://github.com/kubesphere/kubesphere/blob/master/LICENSE
 */

package application

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"path"
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
	return LoadRepoIndexFromOciTags(u, cred)
}

// LoadRepoIndexFromOciTags builds a repository index from OCI tags without
// fetching individual chart manifests. The chart package is fetched on deploy.
func LoadRepoIndexFromOciTags(u string, cred appv2.RepoCredential) (idx helmrepo.IndexFile, err error) {
	if !registry.IsOCI(u) {
		return idx, fmt.Errorf("invalid oci URL format: %s", u)
	}
	parsedURL, err := url.Parse(u)
	if err != nil {
		return idx, err
	}
	repoCharts, err := DiscoverOCIRepositories(context.Background(), parsedURL, cred)
	if err != nil {
		return idx, err
	}
	ociRegistry, err := newOCIRegistry(u, cred)
	if err != nil {
		return idx, err
	}

	index := helmrepo.NewIndexFile()
	for _, repoChart := range repoCharts {
		tags, err := getOCITags(context.Background(), ociRegistry, repoChart)
		if err != nil {
			return idx, fmt.Errorf("load OCI tags for %s/%s: %w", parsedURL.Host, repoChart, err)
		}
		for _, tag := range tags {
			if isAuxiliaryOCITag(tag) {
				continue
			}
			version := strings.ReplaceAll(tag, "_", "+")
			if _, err := semver.NewVersion(version); err != nil {
				continue
			}
			metadata := &chart.Metadata{APIVersion: "v2", Name: path.Base(repoChart), Version: version}
			pullURL := fmt.Sprintf("%s://%s/%s:%s", registry.OCIScheme, parsedURL.Host, repoChart, tag)
			if err := index.MustAdd(metadata, "", pullURL, ""); err != nil {
				return idx, fmt.Errorf("add OCI tag %s: %w", pullURL, err)
			}
		}
	}
	index.SortEntries()
	return *index, nil
}

// ValidateOCIRepository checks connectivity and that the repository exposes at least one SemVer tag.
func ValidateOCIRepository(u string, cred appv2.RepoCredential) error {
	index, err := LoadRepoIndexFromOciTags(u, cred)
	if err != nil {
		return err
	}
	for _, versions := range index.Entries {
		if len(versions) > 0 {
			return nil
		}
	}
	return fmt.Errorf("no OCI chart tags found at %s", u)
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
