package application

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	appv2 "kubesphere.io/api/application/v2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestRepoReconcilerMarksRepoFailedWhenIndexLoadFails(t *testing.T) {
	repo := &appv2.Repo{
		ObjectMeta: metav1.ObjectMeta{Name: "unreachable-oci-repo"},
		Spec: appv2.RepoSpec{
			Url:        "oci://127.0.0.1:1/charts",
			SyncPeriod: ptr.To(0),
		},
		Status: appv2.RepoStatus{State: appv2.StatusManualTrigger},
	}
	scheme := runtime.NewScheme()
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("add application API to scheme: %v", err)
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&appv2.Repo{}).WithObjects(repo).Build()
	reconciler := &RepoReconciler{Client: client, recorder: record.NewFakeRecorder(1)}

	_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: repo.Name},
	})
	if err == nil {
		t.Fatal("Reconcile() error = nil, want an OCI index load error")
	}

	updated := &appv2.Repo{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: repo.Name}, updated); err != nil {
		t.Fatalf("get repo: %v", err)
	}
	if updated.Status.State != appv2.StatusFailed {
		t.Fatalf("repo status = %q, want %q", updated.Status.State, appv2.StatusFailed)
	}
}

func TestRepoReconcilerReusesCachedOCIChartTag(t *testing.T) {
	const chartRepo = "charts/demo"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/", "/v2":
			w.WriteHeader(http.StatusOK)
		case "/v2/" + chartRepo + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0"}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	repo := &appv2.Repo{
		ObjectMeta: metav1.ObjectMeta{Name: "cached-oci-repo"},
		Spec: appv2.RepoSpec{
			Url:        fmt.Sprintf("oci://%s/%s", server.Listener.Addr(), chartRepo),
			Credential: appv2.RepoCredential{PlainHTTP: true},
			SyncPeriod: ptr.To(0),
		},
		Status: appv2.RepoStatus{State: appv2.StatusManualTrigger},
	}
	app := &appv2.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "cached-oci-repo-demo",
			Labels: map[string]string{appv2.RepoIDLabelKey: repo.Name},
			Annotations: map[string]string{
				appv2.AppOriginalNameLabelKey: "demo",
			},
		},
	}
	version := &appv2.ApplicationVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cached-oci-repo-demo-1-0-0",
			Labels: map[string]string{
				appv2.RepoIDLabelKey: repo.Name,
				appv2.AppIDLabelKey:  app.Name,
			},
		},
		Spec: appv2.ApplicationVersionSpec{
			VersionName: "1.0.0",
			Digest:      "cached-digest",
			PullUrl:     fmt.Sprintf("oci://%s/%s:1.0.0", server.Listener.Addr(), chartRepo),
		},
	}
	scheme := runtime.NewScheme()
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("add application API to scheme: %v", err)
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&appv2.Repo{}).WithObjects(repo, app, version).Build()
	reconciler := &RepoReconciler{Client: client, recorder: record.NewFakeRecorder(1)}

	_, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: repo.Name}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	updated := &appv2.Repo{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: repo.Name}, updated); err != nil {
		t.Fatalf("get repo: %v", err)
	}
	if updated.Status.State != appv2.StatusSuccessful {
		t.Fatalf("repo status = %q, want %q", updated.Status.State, appv2.StatusSuccessful)
	}
}
