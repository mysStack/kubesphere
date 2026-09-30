/*
 * Copyright 2024 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 * https://github.com/kubesphere/kubesphere/blob/master/LICENSE
 */

package application

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	appv2 "kubesphere.io/api/application/v2"
	"kubesphere.io/api/constants"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestUpdateStatusDoesNotOverwriteLastUpdater(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("add application API scheme: %v", err)
	}

	release := &appv2.ApplicationRelease{ObjectMeta: metav1.ObjectMeta{
		Name: "audit-release",
		Annotations: map[string]string{
			constants.CreatorAnnotationKey:     "alice",
			constants.LastUpdaterAnnotationKey: "bob",
		},
	}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(release).WithObjects(release).Build()
	reconciler := &AppReleaseReconciler{Client: client}

	current := &appv2.ApplicationRelease{}
	if err := client.Get(context.Background(), runtimeclient.ObjectKeyFromObject(release), current); err != nil {
		t.Fatalf("get release: %v", err)
	}
	if err := reconciler.updateStatus(context.Background(), current, appv2.StatusActive, "ready"); err != nil {
		t.Fatalf("update status: %v", err)
	}

	stored := &appv2.ApplicationRelease{}
	if err := client.Get(context.Background(), runtimeclient.ObjectKeyFromObject(release), stored); err != nil {
		t.Fatalf("get stored release: %v", err)
	}
	if got := stored.Annotations[constants.LastUpdaterAnnotationKey]; got != "bob" {
		t.Fatalf("last updater = %q, want bob", got)
	}
}

func TestPatchAnnotationDoesNotOverwriteLastUpdater(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("add application API scheme: %v", err)
	}
	release := &appv2.ApplicationRelease{ObjectMeta: metav1.ObjectMeta{
		Name: "timeout-release",
		Annotations: map[string]string{
			constants.CreatorAnnotationKey:     "alice",
			constants.LastUpdaterAnnotationKey: "bob",
		},
	}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(release).Build()
	reconciler := &AppReleaseReconciler{Client: client}
	if err := reconciler.patchAnnotation(context.Background(), release, appv2.TimeoutRecheck, "1"); err != nil {
		t.Fatalf("patch annotation: %v", err)
	}

	stored := &appv2.ApplicationRelease{}
	if err := client.Get(context.Background(), runtimeclient.ObjectKeyFromObject(release), stored); err != nil {
		t.Fatalf("get stored release: %v", err)
	}
	if got := stored.Annotations[constants.LastUpdaterAnnotationKey]; got != "bob" {
		t.Fatalf("last updater = %q, want bob", got)
	}
	if got := stored.Annotations[appv2.TimeoutRecheck]; got != "1" {
		t.Fatalf("timeout recheck = %q, want 1", got)
	}
}
