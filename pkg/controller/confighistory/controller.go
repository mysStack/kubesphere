/*
 * Copyright 2024 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 * https://github.com/kubesphere/kubesphere/blob/master/LICENSE
 */

package confighistory

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kscontroller "kubesphere.io/kubesphere/pkg/controller"
)

const (
	// ConfigMapControllerName and SecretControllerName are the names this feature registers.
	//
	// There are two controllers rather than one because a ctrl.Request only carries a
	// namespace and a name: it cannot tell a ConfigMap from a Secret, and two objects can
	// share a name. Watching both from one reconciler would conflate them.
	ConfigMapControllerName = "config-history-configmap"
	SecretControllerName    = "config-history-secret"

	// historyDataKey is the key under which the encoded records live.
	historyDataKey = "records"
)

// Store reads and writes an object's history Secret. The history lives in the same
// namespace as the source object, so the project's RBAC boundary applies unchanged.
type Store struct {
	Client client.Client

	// Now is injectable so tests can pin timestamps.
	Now func() time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Load returns the records for a source object, newest first. A missing history is not an
// error: it just means nothing has been recorded yet.
func (s *Store) Load(ctx context.Context, namespace, name string) ([]Record, error) {
	var secret v1.Secret
	err := s.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: HistoryName(name)}, &secret)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return Decode(secret.Data[historyDataKey])
}

// Append records the content when it differs from the newest record, and reports whether a
// record was written.
//
// Writing the history Secret is itself a Secret write, so this is exactly where a
// self-triggering loop could start. It is stopped on the read side: the Secret reconciler
// skips history objects via IsExcluded before doing any work.
func (s *Store) Append(ctx context.Context, source client.Object, content map[string]string) ([]Record, bool, error) {
	records, err := s.Load(ctx, source.GetNamespace(), source.GetName())
	if err != nil {
		return nil, false, err
	}
	if !ShouldAppend(records, content) {
		return records, false, nil
	}
	records = Append(records, NewRecord(records, content, source.GetAnnotations(), s.now()))
	if err := s.save(ctx, source, records); err != nil {
		return nil, false, err
	}
	return records, true, nil
}

func (s *Store) save(ctx context.Context, source client.Object, records []Record) error {
	payload, err := Encode(records)
	if err != nil {
		return err
	}
	desired := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      HistoryName(source.GetName()),
			Namespace: source.GetNamespace(),
			Labels:    map[string]string{HistoryLabelKey: HistoryLabelValue},
		},
		Type: v1.SecretTypeOpaque,
		Data: map[string][]byte{historyDataKey: payload},
	}

	existing := &v1.Secret{}
	err = s.Client.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if apierrors.IsNotFound(err) {
		return s.Client.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	existing.Labels[HistoryLabelKey] = HistoryLabelValue
	existing.Type = v1.SecretTypeOpaque
	existing.Data = desired.Data
	return s.Client.Update(ctx, existing)
}

// ensureNamespaceInScope keeps the controllers from recording outside KubeSphere projects.
// The check reads the namespace from the manager's cache, so it costs no API round trip per
// reconcile. Returning nil means "in scope"; a non-nil error means the reconcile failed.
func ensureNamespaceInScope(ctx context.Context, c client.Client, namespace string) (bool, error) {
	if namespace == "" {
		return false, nil
	}
	ns := &v1.Namespace{}
	if err := c.Get(ctx, client.ObjectKey{Name: namespace}, ns); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return IsManagedNamespace(ns.Labels), nil
}

// ConfigMapReconciler records ConfigMap changes.
type ConfigMapReconciler struct {
	client.Client
	Logger        logr.Logger
	EventRecorder record.EventRecorder
	Store         Store
}

var _ kscontroller.Controller = &ConfigMapReconciler{}
var _ reconcile.Reconciler = &ConfigMapReconciler{}

// Name implements kscontroller.Controller.
func (r *ConfigMapReconciler) Name() string { return ConfigMapControllerName }

// SetupWithManager implements kscontroller.Controller.
func (r *ConfigMapReconciler) SetupWithManager(mgr *kscontroller.Manager) error {
	r.Client = mgr.GetClient()
	r.Logger = ctrl.Log.WithName("controllers").WithName(ConfigMapControllerName)
	r.EventRecorder = mgr.GetEventRecorderFor(ConfigMapControllerName)
	r.Store = Store{Client: mgr.GetClient()}
	return builder.ControllerManagedBy(mgr).
		For(&v1.ConfigMap{}).
		WithEventFilter(predicate.ResourceVersionChangedPredicate{}).
		Named(r.Name()).
		Complete(r)
}

// Reconcile implements reconcile.Reconciler.

func (r *ConfigMapReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if IsExcluded("ConfigMap", req.Name) {
		return ctrl.Result{}, nil
	}
	inScope, err := ensureNamespaceInScope(ctx, r.Client, req.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !inScope {
		return ctrl.Result{}, nil
	}
	configMap := &v1.ConfigMap{}
	if err := r.Get(ctx, req.NamespacedName, configMap); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	records, appended, err := r.Store.Append(ctx, configMap, ContentFromConfigMap(configMap.Data, configMap.BinaryData))
	if err != nil {
		if r.Logger.Enabled() {
			r.Logger.Error(err, "failed to record configmap change", "namespace", req.Namespace, "name", req.Name)
		}
		return ctrl.Result{}, err
	}
	if appended && r.EventRecorder != nil {
		r.EventRecorder.Eventf(configMap, v1.EventTypeNormal, "HistoryRecorded",
			"recorded revision %d", records[0].Revision)
	}
	return ctrl.Result{}, nil
}

// SecretReconciler records Secret changes, except Helm's release state and its own history
// objects.
type SecretReconciler struct {
	client.Client
	Logger        logr.Logger
	EventRecorder record.EventRecorder
	Store         Store
}

var _ kscontroller.Controller = &SecretReconciler{}
var _ reconcile.Reconciler = &SecretReconciler{}

// Name implements kscontroller.Controller.
func (r *SecretReconciler) Name() string { return SecretControllerName }

// SetupWithManager implements kscontroller.Controller.
func (r *SecretReconciler) SetupWithManager(mgr *kscontroller.Manager) error {
	r.Client = mgr.GetClient()
	r.Logger = ctrl.Log.WithName("controllers").WithName(SecretControllerName)
	r.EventRecorder = mgr.GetEventRecorderFor(SecretControllerName)
	r.Store = Store{Client: mgr.GetClient()}
	return builder.ControllerManagedBy(mgr).
		For(&v1.Secret{}).
		WithEventFilter(predicate.ResourceVersionChangedPredicate{}).
		Named(r.Name()).
		Complete(r)
}

// Reconcile implements reconcile.Reconciler.

func (r *SecretReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// This is the guard that stops the loop: the history Secret we write is itself a
	// Secret, so its own write would otherwise enqueue another reconcile forever.
	if IsExcluded("Secret", req.Name) {
		return ctrl.Result{}, nil
	}
	inScope, err := ensureNamespaceInScope(ctx, r.Client, req.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !inScope {
		return ctrl.Result{}, nil
	}
	secret := &v1.Secret{}
	if err := r.Get(ctx, req.NamespacedName, secret); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	records, appended, err := r.Store.Append(ctx, secret, ContentFromSecret(secret.Data))
	if err != nil {
		if r.Logger.Enabled() {
			r.Logger.Error(err, "failed to record secret change", "namespace", req.Namespace, "name", req.Name)
		}
		return ctrl.Result{}, err
	}
	if appended && r.EventRecorder != nil {
		r.EventRecorder.Eventf(secret, v1.EventTypeNormal, "HistoryRecorded",
			"recorded revision %d", records[0].Revision)
	}
	return ctrl.Result{}, nil
}
