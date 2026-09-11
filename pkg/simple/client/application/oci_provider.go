/*
 * Copyright 2024 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 * https://github.com/kubesphere/kubesphere/blob/master/LICENSE
 */

package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	appv2 "kubesphere.io/api/application/v2"

	"kubesphere.io/kubesphere/pkg/simple/client/oci"
)

// OCIRepositoryProvider discovers OCI repositories from a source URL.
type OCIRepositoryProvider interface {
	Discover(ctx context.Context, source *url.URL, cred appv2.RepoCredential) ([]string, error)
}

type singleChartProvider struct{}

func (singleChartProvider) Discover(ctx context.Context, source *url.URL, cred appv2.RepoCredential) ([]string, error) {
	repository := ociRepositoryPath(source)
	if repository == "" {
		return nil, errors.New("repository name not known")
	}
	registry, err := newOCIRegistry(source.String(), cred)
	if err != nil {
		return nil, err
	}
	tags, err := getOCITags(ctx, registry, repository)
	if err != nil {
		return nil, err
	}
	if len(tags) == 0 {
		return nil, nil
	}
	return []string{repository}, nil
}

type distributionCatalogProvider struct{}

func (distributionCatalogProvider) Discover(ctx context.Context, source *url.URL, cred appv2.RepoCredential) ([]string, error) {
	registry, err := newOCIRegistry(source.String(), cred)
	if err != nil {
		return nil, err
	}

	prefix := ociRepositoryPath(source)
	repositories := make(map[string]struct{})
	err = registry.Repositories(ctx, "", func(page []string) error {
		for _, repository := range page {
			if prefix == "" {
				repositories[repository] = struct{}{}
				continue
			}
			child, found := strings.CutPrefix(repository, prefix+"/")
			if found && child != "" && !strings.Contains(child, "/") {
				repositories[repository] = struct{}{}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	result := make([]string, 0, len(repositories))
	for repository := range repositories {
		result = append(result, repository)
	}
	sort.Strings(result)
	return result, nil
}

var errHarborNotFound = errors.New("Harbor endpoint not found")

type harborProjectProvider struct{}

func (harborProjectProvider) Discover(ctx context.Context, source *url.URL, cred appv2.RepoCredential) ([]string, error) {
	registry, err := newOCIRegistry(source.String(), cred)
	if err != nil {
		return nil, err
	}
	project := strings.Split(ociRepositoryPath(source), "/")[0]

	response, err := harborRequest(ctx, registry, source, cred, http.MethodGet, "/api/v2.0/ping", nil)
	if err != nil {
		return nil, err
	}
	response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, errHarborNotFound
	}
	if response.StatusCode != http.StatusOK {
		return nil, unexpectedHarborStatus(response.StatusCode, "ping")
	}

	response, err = harborRequest(ctx, registry, source, cred, http.MethodHead, "/api/v2.0/projects", url.Values{"project_name": {project}})
	if err != nil {
		return nil, err
	}
	response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, errHarborNotFound
	}
	if response.StatusCode != http.StatusOK {
		return nil, unexpectedHarborStatus(response.StatusCode, "project validation")
	}

	repositories := make(map[string]struct{})
	for page := 1; ; page++ {
		response, err = harborRequest(ctx, registry, source, cred, http.MethodGet, fmt.Sprintf("/api/v2.0/projects/%s/repositories", url.PathEscape(project)), url.Values{
			"page":      {fmt.Sprint(page)},
			"page_size": {"100"},
		})
		if err != nil {
			return nil, err
		}
		if response.StatusCode == http.StatusNotFound {
			response.Body.Close()
			return nil, errHarborNotFound
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return nil, unexpectedHarborStatus(response.StatusCode, "repository list")
		}
		var items []struct {
			Name string `json:"name"`
		}
		err = json.NewDecoder(response.Body).Decode(&items)
		response.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode Harbor repository list: %w", err)
		}
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			if strings.HasPrefix(item.Name, project+"/") {
				repositories[item.Name] = struct{}{}
			}
		}
	}

	result := make([]string, 0, len(repositories))
	for repository := range repositories {
		result = append(result, repository)
	}
	sort.Strings(result)
	return result, nil
}

// DiscoverOCIRepositories discovers a direct chart or child repositories beneath a source URL.
func DiscoverOCIRepositories(ctx context.Context, source *url.URL, cred appv2.RepoCredential) ([]string, error) {
	if source == nil {
		return nil, errors.New("missing source")
	}

	if ociRepositoryPath(source) != "" {
		repositories, err := (singleChartProvider{}).Discover(ctx, source, cred)
		if err == nil {
			return repositories, nil
		}
		if !isOCIRepositoryNotFound(err) {
			return nil, err
		}

		repositories, err = (harborProjectProvider{}).Discover(ctx, source, cred)
		if err == nil {
			return repositories, nil
		}
		if !errors.Is(err, errHarborNotFound) {
			return nil, err
		}
	}

	return (distributionCatalogProvider{}).Discover(ctx, source, cred)
}

func ociRepositoryPath(source *url.URL) string {
	return strings.Trim(strings.TrimSuffix(source.Path, "/"), "/")
}

// GetRepoChartsFromOci is retained for callers using the legacy API.
func GetRepoChartsFromOci(parsedURL *url.URL, cred appv2.RepoCredential) ([]string, error) {
	return DiscoverOCIRepositories(context.Background(), parsedURL, cred)
}

func unexpectedHarborStatus(responseStatus int, endpoint string) error {
	return fmt.Errorf("Harbor %s returned status %d", endpoint, responseStatus)
}

func harborRequest(ctx context.Context, registry *oci.Registry, source *url.URL, cred appv2.RepoCredential, method, endpoint string, query url.Values) (*http.Response, error) {
	scheme := "https"
	if registry.PlainHTTP {
		scheme = "http"
	}
	requestURL := &url.URL{Scheme: scheme, Host: source.Host, Path: endpoint, RawQuery: query.Encode()}
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), nil)
	if err != nil {
		return nil, err
	}
	if cred.Username != "" || cred.Password != "" {
		request.SetBasicAuth(cred.Username, cred.Password)
	}
	return registry.Client.Do(request)
}
