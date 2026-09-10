package application

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	reconciler := &RepoReconciler{Client: client}

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
