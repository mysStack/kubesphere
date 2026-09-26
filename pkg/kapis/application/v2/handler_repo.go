/*
 * Copyright 2024 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 * https://github.com/kubesphere/kubesphere/blob/master/LICENSE
 */

package v2

import (
	"context"
	stderrs "errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"helm.sh/helm/v3/pkg/registry"
	k8suitl "kubesphere.io/kubesphere/pkg/utils/k8sutil"

	"kubesphere.io/kubesphere/pkg/simple/client/application"

	"kubesphere.io/kubesphere/pkg/api"

	"github.com/emicklei/go-restful/v3"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	appv2 "kubesphere.io/api/application/v2"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"kubesphere.io/kubesphere/pkg/constants"
	"kubesphere.io/kubesphere/pkg/server/errors"
	"kubesphere.io/kubesphere/pkg/utils/stringutils"
)

var errFullRefreshRequiresOCI = stderrs.New("full refresh is only supported for OCI repositories")
var errManualSyncAlreadyRunning = stderrs.New("repository sync is already running")

type manualSyncRepoSnapshot struct {
	Name   string           `json:"name"`
	Status appv2.RepoStatus `json:"status"`
}

type manualSyncResponse struct {
	Accepted       bool                   `json:"accepted"`
	AlreadyRunning bool                   `json:"alreadyRunning"`
	Mode           string                 `json:"mode"`
	Repo           manualSyncRepoSnapshot `json:"repo"`
}

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
	if err != nil {
		if registry.IsOCI(repoRequest.Spec.Url) {
			requestDone(fmt.Errorf("invalid repository URL"), resp)
		} else {
			requestDone(err, resp)
		}
		return
	}

	if parsedUrl.User != nil {
		repoRequest.Spec.Credential.Username = parsedUrl.User.Username()
		repoRequest.Spec.Credential.Password, _ = parsedUrl.User.Password()
	}
	if registry.IsOCI(repoRequest.Spec.Url) {
		parsedUrl.User = nil
		repoRequest.Spec.Url = parsedUrl.String()
	}
	workspace := repoCredentialWorkspace(req)
	if repoRequest.Spec.CredentialSecretRef != nil {
		if hasInlineRepoCredential(repoRequest.Spec.Credential) {
			api.HandleBadRequest(resp, req, fmt.Errorf("credential and credentialSecretRef cannot be used together"))
			return
		}
		if err := application.ValidateRepoCredentialSecretRef(req.Request.Context(), h.client, workspace, repoRequest.Spec.CredentialSecretRef); requestDone(err, resp) {
			return
		}
		repoRequest.Spec.CredentialSecretRef.Namespace = constants.KubeSphereNamespace
	}

	if req.QueryParameter("validate") != "" {
		credential := repoRequest.Spec.Credential
		if err = h.loadRepoCredentialSecret(req.Request.Context(), repoRequest.Spec.CredentialSecretRef, &credential); requestDone(err, resp) {
			return
		}
		if registry.IsOCI(repoRequest.Spec.Url) {
			err = application.ValidateOCIRepository(repoRequest.Spec.Url, credential)
		} else {
			_, err = application.LoadRepoIndex(repoRequest.Spec.Url, credential)
		}
		if requestDone(err, resp) {
			return
		}
		data := map[string]any{"ok": true}
		resp.WriteAsJson(data)
		return
	}
	if !registry.IsOCI(repoRequest.Spec.Url) {
		credential := repoRequest.Spec.Credential
		if err = h.loadRepoCredentialSecret(req.Request.Context(), repoRequest.Spec.CredentialSecretRef, &credential); requestDone(err, resp) {
			return
		}
		if _, err = application.LoadRepoIndexFromHTTP(repoRequest.Spec.Url, credential); requestDone(err, resp) {
			return
		}
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
		if repo.GetLabels() == nil {
			repo.SetLabels(map[string]string{})
		}
		repo.Labels[constants.WorkspaceLabelKey] = workspace

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
	return application.LoadRepoCredentialSecret(ctx, h.client, ref, credential)
}

func hasInlineRepoCredential(credential appv2.RepoCredential) bool {
	return credential.Username != "" || credential.Password != "" || credential.CertFile != "" || credential.KeyFile != "" || credential.CAFile != "" || credential.InsecureSkipTLSVerify != nil || credential.PlainHTTP
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
	mode := req.QueryParameter("mode")
	fullRefresh := mode == "full"
	if mode != "" && mode != "incremental" && !fullRefresh {
		api.HandleBadRequest(resp, req, fmt.Errorf("unsupported sync mode %q", mode))
		return
	}
	if fullRefresh && !registry.IsOCI(repo.Spec.Url) {
		api.HandleBadRequest(resp, req, errFullRefreshRequiresOCI)
		return
	}
	if isRepoSyncInProgress(repo) {
		writeManualSyncResponse(resp, repo, true)
		return
	}
	trigger := strconv.FormatInt(time.Now().UnixNano(), 10)
	var acceptedRepo *appv2.Repo
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &appv2.Repo{}
		if err := h.client.Get(req.Request.Context(), key, current); err != nil {
			return err
		}
		if fullRefresh && !registry.IsOCI(current.Spec.Url) {
			return errFullRefreshRequiresOCI
		}
		if isRepoSyncInProgress(current) {
			acceptedRepo = current.DeepCopy()
			return errManualSyncAlreadyRunning
		}
		before := current.DeepCopy()
		if current.Annotations == nil {
			current.Annotations = map[string]string{}
		}
		current.Annotations[appv2.ManualSyncTriggerAnnotation] = trigger
		if fullRefresh {
			current.Annotations[appv2.FullRefreshTriggerAnnotation] = trigger
		}
		if err := h.client.Patch(req.Request.Context(), current, runtimeclient.MergeFromWithOptions(before, runtimeclient.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}
		acceptedRepo = current.DeepCopy()
		return nil
	})
	if err != nil {
		if stderrs.Is(err, errFullRefreshRequiresOCI) {
			api.HandleBadRequest(resp, req, err)
			return
		}
		if stderrs.Is(err, errManualSyncAlreadyRunning) {
			writeManualSyncResponse(resp, acceptedRepo, true)
			return
		}
		api.HandleInternalError(resp, nil, err)
		return
	}
	acceptedRepo.Status.State = appv2.StatusManualTrigger
	if err := h.client.Status().Update(req.Request.Context(), acceptedRepo); err != nil {
		klog.ErrorS(err, "update manual sync status failed", "repo", repoId)
	}
	writeManualSyncResponse(resp, acceptedRepo, false)
}

func isRepoSyncInProgress(repo *appv2.Repo) bool {
	if repo == nil {
		return false
	}
	if repo.Annotations[appv2.ManualSyncTriggerAnnotation] != "" {
		return true
	}
	return repo.Status.State == appv2.StatusManualTrigger || repo.Status.State == appv2.StatusSyncing
}

func writeManualSyncResponse(resp *restful.Response, repo *appv2.Repo, alreadyRunning bool) {
	mode := "incremental"
	if repo != nil && repo.Annotations[appv2.FullRefreshTriggerAnnotation] != "" {
		mode = "full"
	}
	result := manualSyncResponse{
		Accepted:       true,
		AlreadyRunning: alreadyRunning,
		Mode:           mode,
	}
	if repo != nil {
		result.Repo = manualSyncRepoSnapshot{Name: repo.Name, Status: repo.Status}
	}
	resp.WriteEntity(result)
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
	repo.Spec.Credential = appv2.RepoCredential{}

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
		repo.Spec.Credential = appv2.RepoCredential{}
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
