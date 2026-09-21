/*
 * Copyright 2026 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 */

package v2

import (
	"fmt"

	"github.com/emicklei/go-restful/v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	appv2 "kubesphere.io/api/application/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"kubesphere.io/kubesphere/pkg/api"
	"kubesphere.io/kubesphere/pkg/constants"
	"kubesphere.io/kubesphere/pkg/simple/client/application"
)

// repoCredentialPayload is deliberately not a Kubernetes Secret: responses must never expose Secret data.
type repoCredentialPayload struct {
	Metadata   metav1.ObjectMeta    `json:"metadata,omitempty"`
	Credential appv2.RepoCredential `json:"credential,omitempty"`
}

func repoCredentialWorkspace(req *restful.Request) string {
	return application.NormalizeRepoCredentialWorkspace(req.PathParameter("workspace"))
}

func repoCredentialMetadata(secret corev1.Secret) repoCredentialPayload {
	return repoCredentialPayload{Metadata: metav1.ObjectMeta{
		Name:              secret.Name,
		CreationTimestamp: secret.CreationTimestamp,
		Labels: map[string]string{
			constants.WorkspaceLabelKey: secret.Labels[constants.WorkspaceLabelKey],
		},
	}}
}

func (h *appHandler) ListRepoCredentials(req *restful.Request, resp *restful.Response) {
	workspace := repoCredentialWorkspace(req)
	list := &corev1.SecretList{}
	selector := labels.SelectorFromSet(labels.Set{
		application.RepoCredentialLabelKey: "true",
		constants.WorkspaceLabelKey:        workspace,
	})
	if err := h.client.List(req.Request.Context(), list, &client.ListOptions{Namespace: constants.KubeSphereNamespace, LabelSelector: selector}); requestDone(err, resp) {
		return
	}
	items := make([]repoCredentialPayload, 0, len(list.Items))
	for _, secret := range list.Items {
		items = append(items, repoCredentialMetadata(secret))
	}
	resp.WriteEntity(map[string]any{"items": items})
}

func (h *appHandler) CreateRepoCredential(req *restful.Request, resp *restful.Response) {
	payload := &repoCredentialPayload{}
	if err := req.ReadEntity(payload); requestDone(err, resp) {
		return
	}
	if payload.Metadata.Name == "" {
		api.HandleBadRequest(resp, req, fmt.Errorf("credential name is required"))
		return
	}
	if payload.Credential.Password == "" {
		api.HandleBadRequest(resp, req, fmt.Errorf("credential password is required"))
		return
	}
	workspace := repoCredentialWorkspace(req)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: payload.Metadata.Name, Namespace: constants.KubeSphereNamespace,
			Labels: map[string]string{application.RepoCredentialLabelKey: "true", constants.WorkspaceLabelKey: workspace},
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{"username": payload.Credential.Username, "password": payload.Credential.Password},
	}
	if err := h.client.Create(req.Request.Context(), secret); err != nil {
		if apierrors.IsAlreadyExists(err) {
			api.HandleConflict(resp, req, fmt.Errorf("repository credential %q already exists", payload.Metadata.Name))
			return
		}
		requestDone(err, resp)
		return
	}
	resp.WriteEntity(repoCredentialMetadata(*secret))
}

func (h *appHandler) DeleteRepoCredential(req *restful.Request, resp *restful.Response) {
	workspace, name := repoCredentialWorkspace(req), req.PathParameter("credential")
	repos := &appv2.RepoList{}
	if err := h.client.List(req.Request.Context(), repos); requestDone(err, resp) {
		return
	}
	for _, repo := range repos.Items {
		if application.NormalizeRepoCredentialWorkspace(repo.GetWorkspace()) == workspace && repo.Spec.CredentialSecretRef != nil && repo.Spec.CredentialSecretRef.Name == name {
			api.HandleConflict(resp, req, fmt.Errorf("repository credential %q is still used by repository %q", name, repo.Name))
			return
		}
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: constants.KubeSphereNamespace}}
	if err := application.ValidateRepoCredentialSecretRef(req.Request.Context(), h.client, workspace, &corev1.SecretReference{Name: name}); requestDone(err, resp) {
		return
	}
	if err := h.client.Delete(req.Request.Context(), secret); requestDone(err, resp) {
		return
	}
	resp.WriteEntity(map[string]any{})
}
