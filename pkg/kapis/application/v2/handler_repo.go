/*
 * Copyright 2024 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 * https://github.com/kubesphere/kubesphere/blob/master/LICENSE
 */

package v2

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	k8suitl "kubesphere.io/kubesphere/pkg/utils/k8sutil"

	"kubesphere.io/kubesphere/pkg/simple/client/application"

	"kubesphere.io/kubesphere/pkg/api"

	"github.com/emicklei/go-restful/v3"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/klog/v2"
	appv2 "kubesphere.io/api/application/v2"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"kubesphere.io/kubesphere/pkg/constants"
	"kubesphere.io/kubesphere/pkg/server/errors"
	"kubesphere.io/kubesphere/pkg/utils/stringutils"
)

func (h *appHandler) CreateOrUpdateRepo(req *restful.Request, resp *restful.Response) {

	repoRequest := &appv2.Repo{}
	err := req.ReadEntity(repoRequest)
	if requestDone(err, resp) {
		return
	}
	if repoRequest.Name == appv2.UploadRepoKey {
		api.HandleBadRequest(resp, req, fmt.Errorf("repo name %s is not allowed", appv2.UploadRepoKey))
		return
	}
	repoId := req.PathParameter("repo")
	if repoId == "" {
		repoId = repoRequest.Name
	}

	parsedUrl, err := url.Parse(repoRequest.Spec.Url)
	if requestDone(err, resp) {
		return
	}

	if parsedUrl.User != nil {
		repoRequest.Spec.Credential.Username = parsedUrl.User.Username()
		repoRequest.Spec.Credential.Password, _ = parsedUrl.User.Password()
	}

	credential := repoRequest.Spec.Credential
	if err = h.loadRepoCredentialSecret(req.Request.Context(), repoRequest.Spec.CredentialSecretRef, &credential); requestDone(err, resp) {
		return
	}

	_, err = application.LoadRepoIndex(repoRequest.Spec.Url, credential)
	if requestDone(err, resp) {
		return
	}

	if req.QueryParameter("validate") != "" {
		data := map[string]any{"ok": true}
		resp.WriteAsJson(data)
		return
	}

	repo := &appv2.Repo{}
	repo.Name = repoId
	if h.conflictedDone(req, resp, "repo", repo) {
		return
	}

	mutateFn := func() error {
		repo.Spec = appv2.RepoSpec{
			Url:                 parsedUrl.String(),
			Credential:          repoRequest.Spec.Credential,
			CredentialSecretRef: repoRequest.Spec.CredentialSecretRef,
			SyncPeriod:          repoRequest.Spec.SyncPeriod,
			Description:         stringutils.ShortenString(repoRequest.Spec.Description, 512),
		}
		if parsedUrl.User != nil {
			repo.Spec.Credential.Username = parsedUrl.User.Username()
			repo.Spec.Credential.Password, _ = parsedUrl.User.Password()
		}
		if repo.GetLabels() == nil {
			repo.SetLabels(map[string]string{})
		}
		repo.Labels[constants.WorkspaceLabelKey] = repoRequest.Labels[constants.WorkspaceLabelKey]

		if repo.GetAnnotations() == nil {
			repo.SetAnnotations(map[string]string{})
		}
		ant := repoRequest.GetAnnotations()
		repo.Annotations[constants.DisplayNameAnnotationKey] = ant[constants.DisplayNameAnnotationKey]

		return nil
	}

	_, err = controllerutil.CreateOrUpdate(req.Request.Context(), h.client, repo, mutateFn)
	if requestDone(err, resp) {
		return
	}
	data := map[string]interface{}{"repo_id": repoId}

	resp.WriteAsJson(data)
}

func (h *appHandler) loadRepoCredentialSecret(ctx context.Context, ref *v1.SecretReference, credential *appv2.RepoCredential) error {
	if ref == nil {
		return nil
	}
	if ref.Name == "" {
		return fmt.Errorf("credentialSecretRef.name is required")
	}

	namespace := ref.Namespace
	if namespace == "" {
		namespace = constants.KubeSphereNamespace
	}

	secret := &v1.Secret{}
	if err := h.client.Get(ctx, runtimeclient.ObjectKey{Namespace: namespace, Name: ref.Name}, secret); err != nil {
		return err
	}

	setStringFromSecret(secret, "username", &credential.Username)
	setStringFromSecret(secret, "password", &credential.Password)
	setStringFromSecret(secret, "certFile", &credential.CertFile)
	setStringFromSecret(secret, "keyFile", &credential.KeyFile)
	setStringFromSecret(secret, "caFile", &credential.CAFile)
	if err := setBoolPtrFromSecret(secret, "insecureSkipTLSVerify", &credential.InsecureSkipTLSVerify); err != nil {
		return err
	}
	if err := setBoolFromSecret(secret, "plainHTTP", &credential.PlainHTTP); err != nil {
		return err
	}

	return nil
}

func setStringFromSecret(secret *v1.Secret, key string, dst *string) {
	if value, ok := secret.Data[key]; ok {
		*dst = string(value)
	}
}

func setBoolPtrFromSecret(secret *v1.Secret, key string, dst **bool) error {
	if value, ok := secret.Data[key]; ok {
		parsed, err := parseSecretBool(key, value)
		if err != nil {
			return err
		}
		*dst = &parsed
	}
	return nil
}

func setBoolFromSecret(secret *v1.Secret, key string, dst *bool) error {
	if value, ok := secret.Data[key]; ok {
		parsed, err := parseSecretBool(key, value)
		if err != nil {
			return err
		}
		*dst = parsed
	}
	return nil
}

func parseSecretBool(key string, value []byte) (bool, error) {
	parsed, err := strconv.ParseBool(strings.TrimSpace(string(value)))
	if err != nil {
		return false, fmt.Errorf("invalid boolean value for secret key %q: %w", key, err)
	}
	return parsed, nil
}

func (h *appHandler) DeleteRepo(req *restful.Request, resp *restful.Response) {
	repoId := req.PathParameter("repo")

	err := h.client.Delete(req.Request.Context(), &appv2.Repo{ObjectMeta: metav1.ObjectMeta{Name: repoId}})
	if requestDone(err, resp) {
		return
	}
	klog.V(4).Info("delete repo: ", repoId)

	resp.WriteEntity(errors.None)
}

func (h *appHandler) ManualSync(req *restful.Request, resp *restful.Response) {
	repoId := req.PathParameter("repo")

	key := runtimeclient.ObjectKey{Name: repoId}
	repo := &appv2.Repo{}
	err := h.client.Get(req.Request.Context(), key, repo)
	if requestDone(err, resp) {
		return
	}
	repo.Status.State = appv2.StatusManualTrigger
	err = h.client.Status().Update(req.Request.Context(), repo)
	if err != nil {
		api.HandleInternalError(resp, nil, err)
		return
	}
	resp.WriteEntity(errors.None)
}

func (h *appHandler) DescribeRepo(req *restful.Request, resp *restful.Response) {
	repoId := req.PathParameter("repo")

	key := runtimeclient.ObjectKey{Name: repoId}
	repo := &appv2.Repo{}
	err := h.client.Get(req.Request.Context(), key, repo)
	if requestDone(err, resp) {
		return
	}
	repo.SetManagedFields(nil)

	resp.WriteEntity(repo)
}

func (h *appHandler) ListRepos(req *restful.Request, resp *restful.Response) {

	helmRepoList := &appv2.RepoList{}
	err := h.client.List(req.Request.Context(), helmRepoList)
	if requestDone(err, resp) {
		return
	}
	workspace := req.PathParameter("workspace")
	filteredList := &appv2.RepoList{}
	for _, repo := range helmRepoList.Items {
		allowList := []string{appv2.SystemWorkspace, workspace}
		if !stringutils.StringIn(repo.Labels[constants.WorkspaceLabelKey], allowList) {
			continue
		}
		filteredList.Items = append(filteredList.Items, repo)
	}

	resp.WriteEntity(k8suitl.ConvertToListResult(filteredList, req))
}

func (h *appHandler) ListRepoEvents(req *restful.Request, resp *restful.Response) {
	repoId := req.PathParameter("repo")

	list := v1.EventList{}
	selector := fields.SelectorFromSet(fields.Set{
		"involvedObject.name": repoId,
	})

	opt := &runtimeclient.ListOptions{FieldSelector: selector}
	err := h.client.List(req.Request.Context(), &list, opt)
	if requestDone(err, resp) {
		return
	}

	resp.WriteEntity(k8suitl.ConvertToListResult(&list, req))
}
