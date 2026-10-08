/*
 * Copyright 2024 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 * https://github.com/kubesphere/kubesphere/blob/master/LICENSE
 */

package confighistory

import (
	"context"
	"fmt"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func fixedNow() time.Time { return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) }

func newClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	// Only project namespaces are in scope, so the shared test namespace carries the label.
	// A test that needs an out-of-scope namespace builds its own client.
	project := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "dev-wes",
		Labels: map[string]string{WorkspaceLabelKey: "dev-workspace"},
	}}
	return fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(append([]client.Object{project}, objects...)...).
		Build()
}

func loadSecret(t *testing.T, c client.Client, namespace, name string) *v1.Secret {
	t.Helper()
	secret := &v1.Secret{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, secret); err != nil {
		t.Fatalf("get %s/%s: %v", namespace, name, err)
	}
	return secret
}

func TestStoreLoadReturnsNothingWhenThereIsNoHistory(t *testing.T) {
	store := &Store{Client: newClient(t)}
	records, err := store.Load(context.Background(), "dev-wes", "nothing-here")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if records != nil {
		t.Fatalf("Load() = %v, want nil", records)
	}
}

func TestStoreAppendCreatesTheHistorySecret(t *testing.T) {
	c := newClient(t)
	store := &Store{Client: c, Now: fixedNow}
	source := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "dev-wes",
			Name:        "ewms-postgres-wes-config",
			Annotations: map[string]string{"meta.helm.sh/release-name": "wes-server"},
		},
		Data: map[string]string{"DB_USERNAME": "wes_reader"},
	}

	records, appended, err := store.Append(context.Background(), source, ContentFromConfigMap(source.Data, nil))
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if !appended || len(records) != 1 {
		t.Fatalf("Append() = (%d records, appended=%v), want (1, true)", len(records), appended)
	}
	if records[0].Revision != 1 || records[0].ManagedBy != ManagedByHelm || records[0].ManagedByRef != "wes-server" {
		t.Fatalf("unexpected first record: %+v", records[0])
	}
	if !records[0].CreatedAt.Equal(fixedNow()) {
		t.Fatalf("record time = %v, want %v", records[0].CreatedAt, fixedNow())
	}

	history := loadSecret(t, c, "dev-wes", "ewms-postgres-wes-config-history")
	if history.Labels[HistoryLabelKey] != HistoryLabelValue {
		t.Fatalf("history Secret must carry the ownership label, got labels %v", history.Labels)
	}
	if len(history.Data[historyDataKey]) == 0 {
		t.Fatal("history Secret has no payload")
	}
}

func TestStoreAppendSkipsUnchangedContent(t *testing.T) {
	c := newClient(t)
	store := &Store{Client: c, Now: fixedNow}
	source := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev-wes", Name: "app-secret"},
		Data:       map[string][]byte{"TOKEN": []byte("s1")},
	}
	content := ContentFromSecret(source.Data)

	if _, appended, err := store.Append(context.Background(), source, content); err != nil || !appended {
		t.Fatalf("first Append() = (appended=%v, err=%v), want (true, nil)", appended, err)
	}
	before := loadSecret(t, c, "dev-wes", "app-secret-history")

	if records, appended, err := store.Append(context.Background(), source, content); err != nil || appended {
		t.Fatalf("second Append() = (appended=%v, err=%v), want (false, nil)", appended, err)
	} else if len(records) != 1 {
		t.Fatalf("unchanged content produced %d records, want 1", len(records))
	}

	after := loadSecret(t, c, "dev-wes", "app-secret-history")
	if before.ResourceVersion != after.ResourceVersion {
		t.Fatal("unchanged content must not write the history Secret again")
	}
}

func TestStoreAppendKeepsNewestFirstAndCapsAtMaxRecords(t *testing.T) {
	c := newClient(t)
	store := &Store{Client: c, Now: fixedNow}
	source := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev-wes", Name: "app-secret"},
		Data:       map[string][]byte{"TOKEN": []byte("v0")},
	}
	for i := 1; i <= MaxRecords+2; i++ {
		source.Data["TOKEN"] = []byte(string(rune('a' + i)))
		if _, appended, err := store.Append(context.Background(), source, ContentFromSecret(source.Data)); err != nil || !appended {
			t.Fatalf("append %d = (appended=%v, err=%v)", i, appended, err)
		}
	}
	records, err := store.Load(context.Background(), "dev-wes", "app-secret")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(records) != MaxRecords {
		t.Fatalf("kept %d records, want %d", len(records), MaxRecords)
	}
	if records[0].Revision != MaxRecords+2 {
		t.Fatalf("newest revision = %d, want %d", records[0].Revision, MaxRecords+2)
	}
	if records[len(records)-1].Revision != 3 {
		t.Fatalf("oldest kept revision = %d, want 3", records[len(records)-1].Revision)
	}
}

// This is the guard against the feature feeding itself: the history Secret is itself a
// Secret, so its own write triggers the Secret reconciler. It must do nothing.
func TestSecretReconcilerIgnoresHistoryObjects(t *testing.T) {
	history := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "dev-wes",
			Name:      "app-secret-history",
			Labels:    map[string]string{HistoryLabelKey: HistoryLabelValue},
		},
		Data: map[string][]byte{historyDataKey: []byte("payload")},
	}
	c := newClient(t, history)
	r := &SecretReconciler{Client: c, Store: Store{Client: c, Now: fixedNow}}

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(history),
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Fatalf("Reconcile() requested a requeue: %+v", result)
	}

	after := loadSecret(t, c, "dev-wes", "app-secret-history")
	if string(after.Data[historyDataKey]) != "payload" {
		t.Fatal("the reconciler must not touch its own history object")
	}
	// And no nested history of the history.
	if err := c.Get(context.Background(), client.ObjectKey{
		Namespace: "dev-wes", Name: "app-secret-history-history",
	}, &v1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("a history object must never get a history of its own (err = %v)", err)
	}
}

func TestSecretReconcilerIgnoresHelmReleaseSecrets(t *testing.T) {
	helm := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev-wes", Name: "sh.helm.release.v1.wes-server.v2"},
		Data:       map[string][]byte{"release": []byte("huge")},
	}
	c := newClient(t, helm)
	r := &SecretReconciler{Client: c, Store: Store{Client: c, Now: fixedNow}}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(helm),
	}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{
		Namespace: "dev-wes", Name: "sh.helm.release.v1.wes-server.v2-history",
	}, &v1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("Helm release secrets must not be recorded (err = %v)", err)
	}
}

func TestSecretReconcilerRecordsAChange(t *testing.T) {
	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "dev-wes",
			Name:        "app-secret",
			Annotations: map[string]string{"replicator.v1.mittwald.de/replicated-from-version": "102574264"},
		},
		Data: map[string][]byte{"TOKEN": []byte("s1")},
	}
	c := newClient(t, secret)
	r := &SecretReconciler{Client: c, Store: Store{Client: c, Now: fixedNow}}

	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(secret),
	}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	records, err := (&Store{Client: c}).Load(context.Background(), "dev-wes", "app-secret")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	if records[0].ManagedBy != ManagedByReplicator || records[0].ManagedByRef != "102574264" {
		t.Fatalf("unexpected record: %+v", records[0])
	}
	if records[0].Content["TOKEN"] != "s1" {
		t.Fatalf("content not recorded: %+v", records[0].Content)
	}

	// Changed content appends; unchanged content does not.
	secret.Data["TOKEN"] = []byte("s2")
	if err := c.Update(context.Background(), secret); err != nil {
		t.Fatalf("update source secret: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(secret),
	}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	records, _ = (&Store{Client: c}).Load(context.Background(), "dev-wes", "app-secret")
	if len(records) != 2 || records[0].Content["TOKEN"] != "s2" {
		t.Fatalf("second revision not recorded correctly: %+v", records)
	}
}

func TestConfigMapReconcilerRecordsBinaryDataToo(t *testing.T) {
	configMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev-wes", Name: "app-config"},
		Data:       map[string]string{"DB_PORT": "5432"},
		BinaryData: map[string][]byte{"CERT": []byte("pem")},
	}
	c := newClient(t, configMap)
	r := &ConfigMapReconciler{Client: c, Store: Store{Client: c, Now: fixedNow}}

	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(configMap),
	}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	records, err := (&Store{Client: c}).Load(context.Background(), "dev-wes", "app-config")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(records) != 1 || records[0].Content["DB_PORT"] != "5432" || records[0].Content["CERT"] != "cGVt" {
		t.Fatalf("unexpected records: %+v", records)
	}
	if records[0].ManagedBy != ManagedByDirect {
		t.Fatalf("managedBy = %q, want %q", records[0].ManagedBy, ManagedByDirect)
	}
}

func TestReconcileOfADeletedObjectIsNotAnError(t *testing.T) {
	c := newClient(t)
	r := &ConfigMapReconciler{Client: c, Store: Store{Client: c, Now: fixedNow}}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKey{Namespace: "dev-wes", Name: "gone"},
	}); err != nil {
		t.Fatalf("a deleted object should be ignored, got error %v", err)
	}
}

// The scope rule, and the reason it exists: the first deployment of these controllers
// recorded cluster-wide and created 516 history Secrets in 15 minutes, including inside
// system namespaces.
func TestReconcilersSkipNamespacesThatAreNotProjects(t *testing.T) {
	systemNS := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}}
	configMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "coredns"},
		Data:       map[string]string{"A": "1"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(systemNS, configMap).Build()
	r := &ConfigMapReconciler{Client: c, Store: Store{Client: c, Now: fixedNow}}

	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(configMap),
	}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{
		Namespace: "kube-system", Name: "coredns-history",
	}, &v1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("a namespace that is not a project must not be recorded (err = %v)", err)
	}
}

// The second scope incident: namespaces in system-workspace carry the workspace label just
// like projects do, so a label-only rule recorded kube-system, kubesphere-system and the rest.
// 513 objects appeared in one run before this was caught.
func TestReconcilersSkipSystemWorkspace(t *testing.T) {
	systemNS := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "kubesphere-monitoring-system",
		Labels: map[string]string{WorkspaceLabelKey: SystemWorkspace},
	}}
	configMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kubesphere-monitoring-system", Name: "probe"},
		Data:       map[string]string{"A": "1"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(systemNS, configMap).Build()
	r := &ConfigMapReconciler{Client: c, Store: Store{Client: c, Now: fixedNow}}

	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(configMap),
	}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{
		Namespace: "kubesphere-monitoring-system", Name: "probe-history",
	}, &v1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("a system-workspace namespace must not be recorded (err = %v)", err)
	}
}

// The incident, as a test: at startup the informer enumerates every existing object. That
// must not create history for any of them -- it created 516, 513 and then 376 objects on an
// idle cluster across three deployments.
func TestStartupEnumerationRecordsNothing(t *testing.T) {
	project := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "dev-wes", Labels: map[string]string{WorkspaceLabelKey: "dev-workspace"},
	}}
	var objects []client.Object
	objects = append(objects, project)
	for i := 0; i < 5; i++ {
		objects = append(objects,
			&v1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: "dev-wes", Name: fmt.Sprintf("cm-%d", i)},
				Data:       map[string]string{"A": "1"},
			},
			&v1.Secret{
				ObjectMeta: metav1.ObjectMeta{Namespace: "dev-wes", Name: fmt.Sprintf("sec-%d", i)},
				Data:       map[string][]byte{"K": []byte("v")},
			})
	}
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(objects...).Build()
	cmr := &ConfigMapReconciler{Client: c, Store: Store{Client: c, Now: fixedNow}, Baseline: newBaseline()}
	sr := &SecretReconciler{Client: c, Store: Store{Client: c, Now: fixedNow}, Baseline: newBaseline()}

	for _, o := range objects[1:] {
		r := cmr
		req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(o)}
		if _, isSecret := o.(*v1.Secret); isSecret {
			if _, err := sr.Reconcile(context.Background(), req); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			continue
		}
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
	}

	secrets := &v1.SecretList{}
	if err := c.List(context.Background(), secrets, client.InNamespace("dev-wes")); err != nil {
		t.Fatalf("list secrets: %v", err)
	}
	if len(secrets.Items) != 5 {
		t.Fatalf("startup enumeration created %d extra objects, want 0 (history Secrets are the only extra ones)", len(secrets.Items)-5)
	}
}

// And once content really changes, the history starts -- with no predecessor to compare to.
func TestFirstChangeAfterBaselineStartsTheHistory(t *testing.T) {
	configMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev-wes", Name: "app-config"},
		Data:       map[string]string{"A": "1"},
	}
	c := newClient(t, configMap)
	r := &ConfigMapReconciler{Client: c, Store: Store{Client: c, Now: fixedNow}, Baseline: newBaseline()}
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(configMap)}

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("baseline reconcile error = %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{
		Namespace: "dev-wes", Name: "app-config-history",
	}, &v1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the baseline must not write anything (err = %v)", err)
	}

	configMap.Data["A"] = "2"
	if err := c.Update(context.Background(), configMap); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("change reconcile error = %v", err)
	}
	records, err := (&Store{Client: c}).Load(context.Background(), "dev-wes", "app-config")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(records) != 1 || records[0].Content["A"] != "2" {
		t.Fatalf("the first real change should have started the history: %+v", records)
	}
}
