/*
 * Copyright 2024 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 * https://github.com/kubesphere/kubesphere/blob/master/LICENSE
 */

package v2

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/emicklei/go-restful/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/authentication/user"
	appv2 "kubesphere.io/api/application/v2"
	"kubesphere.io/api/constants"
	"kubesphere.io/kubesphere/pkg/apiserver/request"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestApplyAppReleaseAuditAnnotationsOnCreate(t *testing.T) {
	requested := &appv2.ApplicationRelease{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
			"example.com/custom":               "keep",
			constants.CreatorAnnotationKey:     "spoofed",
			constants.LastUpdaterAnnotationKey: "spoofed",
		}},
	}

	applyAppReleaseAuditAnnotations(&appv2.ApplicationRelease{}, requested, "alice")

	if got := requested.Annotations[constants.CreatorAnnotationKey]; got != "alice" {
		t.Fatalf("creator = %q, want alice", got)
	}
	if _, ok := requested.Annotations[constants.LastUpdaterAnnotationKey]; ok {
		t.Fatal("last updater must not be set on create")
	}
	if got := requested.Annotations["example.com/custom"]; got != "keep" {
		t.Fatalf("custom annotation = %q, want keep", got)
	}
}

func TestCreateOrUpdateAppReleaseRecordsCurrentUserWithoutTrustingAuditAnnotations(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("add application API scheme: %v", err)
	}
	existing := &appv2.ApplicationRelease{ObjectMeta: metav1.ObjectMeta{
		Name: "audit-release",
		Annotations: map[string]string{
			constants.CreatorAnnotationKey: "alice",
		},
	}}
	h := &appHandler{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()}
	ws := new(restful.WebService)
	ws.Path("/").Consumes(restful.MIME_JSON).Produces(restful.MIME_JSON)
	ws.Route(ws.POST("/applications/{application}").To(h.CreateOrUpdateAppRls))
	container := restful.NewContainer()
	container.Add(ws)

	body, err := json.Marshal(&appv2.ApplicationRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name: "audit-release",
			Labels: map[string]string{
				appv2.AppIDLabelKey:           "demo",
				constants.ClusterNameLabelKey: "host",
				constants.WorkspaceLabelKey:   "dev-workspace",
				constants.NamespaceLabelKey:   "dev-wes",
			},
			Annotations: map[string]string{
				constants.CreatorAnnotationKey:     "spoofed",
				constants.LastUpdaterAnnotationKey: "spoofed",
			},
		},
		Spec: appv2.ApplicationReleaseSpec{AppType: appv2.AppTypeHelm, AppVersionID: "demo-1"},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/applications/audit-release", bytes.NewReader(body))
	req.Header.Set("Content-Type", restful.MIME_JSON)
	req = req.WithContext(request.WithUser(req.Context(), &user.DefaultInfo{Name: "bob"}))
	recorder := httptest.NewRecorder()
	container.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	stored := &appv2.ApplicationRelease{}
	if err := h.client.Get(context.Background(), runtimeclient.ObjectKey{Name: "audit-release"}, stored); err != nil {
		t.Fatalf("get updated release: %v", err)
	}
	if got := stored.Annotations[constants.CreatorAnnotationKey]; got != "alice" {
		t.Fatalf("creator = %q, want alice", got)
	}
	if got := stored.Annotations[constants.LastUpdaterAnnotationKey]; got != "bob" {
		t.Fatalf("last updater = %q, want bob", got)
	}
}

func TestApplyAppReleaseAuditAnnotationsOnUpdate(t *testing.T) {
	current := &appv2.ApplicationRelease{ObjectMeta: metav1.ObjectMeta{
		ResourceVersion: "7",
		Annotations: map[string]string{
			constants.CreatorAnnotationKey:     "alice",
			constants.LastUpdaterAnnotationKey: "previous",
		},
	}}
	requested := &appv2.ApplicationRelease{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		constants.CreatorAnnotationKey: "spoofed",
	}}}

	applyAppReleaseAuditAnnotations(current, requested, "bob")

	if got := requested.Annotations[constants.CreatorAnnotationKey]; got != "alice" {
		t.Fatalf("creator = %q, want alice", got)
	}
	if got := requested.Annotations[constants.LastUpdaterAnnotationKey]; got != "bob" {
		t.Fatalf("last updater = %q, want bob", got)
	}
}

func TestApplyAppReleaseAuditAnnotationsPreservesCreatorWhenLegacyObjectHasNone(t *testing.T) {
	current := &appv2.ApplicationRelease{ObjectMeta: metav1.ObjectMeta{ResourceVersion: "7"}}
	requested := &appv2.ApplicationRelease{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		constants.CreatorAnnotationKey: "spoofed",
	}}}

	applyAppReleaseAuditAnnotations(current, requested, "bob")

	if _, ok := requested.Annotations[constants.CreatorAnnotationKey]; ok {
		t.Fatal("creator must not be introduced from the update payload")
	}
	if got := requested.Annotations[constants.LastUpdaterAnnotationKey]; got != "bob" {
		t.Fatalf("last updater = %q, want bob", got)
	}
}
