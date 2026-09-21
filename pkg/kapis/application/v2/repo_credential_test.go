/*
 * Copyright 2026 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 */

package v2

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emicklei/go-restful/v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	appv2 "kubesphere.io/api/application/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"kubesphere.io/kubesphere/pkg/constants"
	"kubesphere.io/kubesphere/pkg/simple/client/application"
)

func TestRepoCredentialAPIStoresSecretWithoutReturningPassword(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	h := &appHandler{client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	ws := new(restful.WebService)
	ws.Path("/").Consumes(restful.MIME_JSON).Produces(restful.MIME_JSON)
	ws.Route(ws.POST("/repo-credentials").To(h.CreateRepoCredential))
	ws.Route(ws.GET("/repo-credentials").To(h.ListRepoCredentials))
	container := restful.NewContainer()
	container.Add(ws)

	body := []byte(`{"metadata":{"name":"private-registry"},"credential":{"username":"robot","password":"secret-token"}}`)
	create := httptest.NewRecorder()
	createRequest := httptest.NewRequest(http.MethodPost, "/repo-credentials", bytes.NewReader(body))
	createRequest.Header.Set("Content-Type", restful.MIME_JSON)
	container.ServeHTTP(create, createRequest)
	if create.Code != http.StatusOK || strings.Contains(create.Body.String(), "secret-token") {
		t.Fatalf("create response = %d %s, want safe success response", create.Code, create.Body.String())
	}

	secret := &corev1.Secret{}
	if err := h.client.Get(context.Background(), client.ObjectKey{Namespace: constants.KubeSphereNamespace, Name: "private-registry"}, secret); err != nil {
		t.Fatalf("get credential secret: %v", err)
	}
	if secret.Type != corev1.SecretTypeOpaque || secret.Labels[application.RepoCredentialLabelKey] != "true" || secret.Labels[constants.WorkspaceLabelKey] != appv2.SystemWorkspace {
		t.Fatalf("credential secret metadata = %#v", secret)
	}
	if string(secret.Data["password"]) != "secret-token" && string(secret.StringData["password"]) != "secret-token" {
		t.Fatal("credential password was not persisted")
	}

	list := httptest.NewRecorder()
	container.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/repo-credentials", nil))
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "secret-token") {
		t.Fatalf("list response = %d %s, must not contain password", list.Code, list.Body.String())
	}
}

func TestValidateRepoCredentialSecretRefRejectsOtherWorkspace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: constants.KubeSphereNamespace, Name: "team-a", Labels: map[string]string{application.RepoCredentialLabelKey: "true", constants.WorkspaceLabelKey: "team-a"}}, Type: corev1.SecretTypeOpaque}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	if err := application.ValidateRepoCredentialSecretRef(context.Background(), reader, "team-b", &corev1.SecretReference{Name: "team-a"}); err == nil {
		t.Fatal("cross-workspace credential reference was accepted")
	}
}
