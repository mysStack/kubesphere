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

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/registry"
	helmrepo "helm.sh/helm/v3/pkg/repo"
	"k8s.io/klog/v2"
	appv2 "kubesphere.io/api/application/v2"

	"kubesphere.io/kubesphere/pkg/simple/client/oci"
)

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
		oci.WithTimeout(5 * time.Second),
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
		oci.WithTimeout(5 * time.Second),
		oci.WithBasicAuth(cred.Username, cred.Password),
		oci.WithInsecureSkipVerifyTLS(skipTLS),
	}
	if cred.PlainHTTP {
		options = append(options, oci.WithPlainHTTP())
	}

	return oci.NewRegistry(parsedURL.Host, options...)
}
