package application

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"helm.sh/helm/v3/pkg/registry"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	appv2 "kubesphere.io/api/application/v2"
	appclient "kubesphere.io/kubesphere/pkg/simple/client/application"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type ociControllerChartFixture struct {
	repository string
	tag        string
	digest     string
	config     string
}

func newOCIControllerServer(t *testing.T, fixtures []ociControllerChartFixture) *httptest.Server {
	t.Helper()
	repositories := make([]string, 0, len(fixtures))
	for _, fixture := range fixtures {
		repositories = append(repositories, fixture.repository)
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/", "/v2":
			w.WriteHeader(http.StatusOK)
		case "/v2/charts/tags/list":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"code":"NAME_UNKNOWN","message":"repository name not known"}]}`))
		case "/api/v2.0/ping":
			w.WriteHeader(http.StatusNotFound)
		case "/v2/_catalog":
			_ = json.NewEncoder(w).Encode(map[string][]string{"repositories": repositories})
		default:
			for _, fixture := range fixtures {
				configDigest := digest.FromString(fixture.repository + fixture.tag)
				switch r.URL.Path {
				case "/v2/" + fixture.repository + "/tags/list":
					_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {fixture.tag}})
					return
				case "/v2/" + fixture.repository + "/manifests/" + fixture.tag:
					w.Header().Set("Docker-Content-Digest", fixture.digest)
					_ = json.NewEncoder(w).Encode(ocispec.Manifest{
						Config: ocispec.Descriptor{MediaType: registry.ConfigMediaType, Digest: configDigest},
						Layers: []ocispec.Descriptor{{MediaType: registry.ChartLayerMediaType}},
					})
					return
				case "/v2/" + fixture.repository + "/blobs/" + configDigest.String():
					_, _ = w.Write([]byte(fixture.config))
					return
				}
			}
			t.Errorf("unexpected OCI request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func newRepoReconcilerTestClient(t *testing.T, objects ...runtime.Object) (*RepoReconciler, *record.FakeRecorder) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core API to scheme: %v", err)
	}
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("add application API to scheme: %v", err)
	}
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&appv2.Repo{}, &appv2.Application{}, &appv2.ApplicationVersion{}).
		WithRuntimeObjects(objects...).Build()
	recorder := record.NewFakeRecorder(10)
	return &RepoReconciler{Client: client, recorder: recorder}, recorder
}

func TestRepoReconcilerCreatesApplicationsForMultipleOCICharts(t *testing.T) {
	server := newOCIControllerServer(t, []ociControllerChartFixture{
		{
			repository: "charts/alpha", tag: "1.0.0", digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			config: `{"apiVersion":"v2","name":"alpha","version":"9.9.9","description":"Alpha chart","home":"https://alpha.example.test","icon":"https://alpha.example.test/icon.svg","maintainers":[{"name":"Alice"}]}`,
		},
		{
			repository: "charts/beta", tag: "2.0.0", digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			config: `{"apiVersion":"v2","name":"beta","version":"9.9.9","description":"Beta chart","home":"https://beta.example.test","icon":"https://beta.example.test/icon.svg","maintainers":[{"name":"Bob"}]}`,
		},
		{
			repository: "charts/broken", tag: "3.0.0", digest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			config: `{"name":`,
		},
	})
	defer server.Close()

	repo := &appv2.Repo{
		ObjectMeta: metav1.ObjectMeta{Name: "multi-oci-repo", UID: types.UID("repo-uid")},
		Spec: appv2.RepoSpec{
			Url:        fmt.Sprintf("oci://%s/charts", server.Listener.Addr()),
			Credential: appv2.RepoCredential{PlainHTTP: true, Username: "fixture-user", Password: "fixture-password"},
			SyncPeriod: ptr.To(0),
		},
		Status: appv2.RepoStatus{State: appv2.StatusManualTrigger},
	}
	reconciler, recorder := newRepoReconcilerTestClient(t, repo)

	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: repo.Name}}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	apps := &appv2.ApplicationList{}
	if err := reconciler.List(context.Background(), apps); err != nil {
		t.Fatalf("list applications: %v", err)
	}
	if got := len(apps.Items); got != 2 {
		t.Fatalf("application count = %d, want 2", got)
	}
	for _, app := range apps.Items {
		if app.Labels[appv2.RepoIDLabelKey] != repo.Name {
			t.Errorf("application %s repo label = %q", app.Name, app.Labels[appv2.RepoIDLabelKey])
		}
		originalName := app.Annotations[appv2.AppOriginalNameLabelKey]
		if app.Annotations["kubesphere.io/description"] != strings.Title(originalName)+" chart" {
			t.Errorf("application %s metadata = %#v", app.Name, app.Annotations)
		}
	}
	versions := &appv2.ApplicationVersionList{}
	if err := reconciler.List(context.Background(), versions); err != nil {
		t.Fatalf("list application versions: %v", err)
	}
	if got := len(versions.Items); got != 2 {
		t.Fatalf("application version count = %d, want 2", got)
	}
	for _, version := range versions.Items {
		if version.Spec.Digest == "" {
			t.Errorf("application version %s has empty digest", version.Name)
		}
		if version.Spec.AppHome == "" || version.Spec.Icon == "" || len(version.Spec.Maintainer) != 1 {
			t.Errorf("application version %s metadata = %#v", version.Name, version.Spec)
		}
	}
	updatedRepo := &appv2.Repo{}
	if err := reconciler.Get(context.Background(), types.NamespacedName{Name: repo.Name}, updatedRepo); err != nil {
		t.Fatalf("get repo: %v", err)
	}
	if updatedRepo.Status.State != appv2.StatusSuccessful {
		t.Fatalf("repo status = %q, want %q", updatedRepo.Status.State, appv2.StatusSuccessful)
	}
	repos := &appv2.RepoList{}
	if err := reconciler.List(context.Background(), repos); err != nil {
		t.Fatalf("list repos: %v", err)
	}
	if got := len(repos.Items); got != 1 {
		t.Fatalf("repo count = %d, want 1", got)
	}
	select {
	case event := <-recorder.Events:
		if !strings.Contains(event, "Warning") || !strings.Contains(event, "charts/broken:3.0.0") {
			t.Fatalf("warning event = %q", event)
		}
		if strings.Contains(event, repo.Spec.Credential.Username) || strings.Contains(event, repo.Spec.Credential.Password) {
			t.Fatalf("warning event exposes credentials: %q", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for OCI warning event")
	}
}

func TestRepoReconcilerDeletesApplicationsAndVersionsRemovedFromOCIRepo(t *testing.T) {
	const chartRepo = "charts/keep"
	server := newOCIControllerServer(t, []ociControllerChartFixture{{
		repository: chartRepo,
		tag:        "1.0.0",
		digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		config:     `{"apiVersion":"v2","name":"keep","version":"1.0.0"}`,
	}})
	defer server.Close()

	repo := &appv2.Repo{
		ObjectMeta: metav1.ObjectMeta{Name: "deletion-oci-repo", UID: types.UID("repo-uid")},
		Spec: appv2.RepoSpec{
			Url:        fmt.Sprintf("oci://%s", server.Listener.Addr()),
			Credential: appv2.RepoCredential{PlainHTTP: true},
			SyncPeriod: ptr.To(0),
		},
		Status: appv2.RepoStatus{State: appv2.StatusManualTrigger},
	}
	keepAppName := repo.Name + "-" + appclient.GenerateShortNameMD5Hash("keep")
	removeAppName := repo.Name + "-" + appclient.GenerateShortNameMD5Hash("remove")
	keepApp := &appv2.Application{ObjectMeta: metav1.ObjectMeta{Name: keepAppName, Labels: map[string]string{appv2.RepoIDLabelKey: repo.Name}}}
	removeApp := &appv2.Application{ObjectMeta: metav1.ObjectMeta{Name: removeAppName, Labels: map[string]string{appv2.RepoIDLabelKey: repo.Name}}}
	removeVersion := &appv2.ApplicationVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name:   removeAppName + "-1.0.0",
			Labels: map[string]string{appv2.RepoIDLabelKey: repo.Name, appv2.AppIDLabelKey: removeAppName},
		},
		Spec: appv2.ApplicationVersionSpec{VersionName: "1.0.0"},
	}
	reconciler, _ := newRepoReconcilerTestClient(t, repo, keepApp, removeApp, removeVersion)

	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: repo.Name}}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if err := reconciler.Get(context.Background(), types.NamespacedName{Name: removeAppName}, &appv2.Application{}); err == nil {
		t.Fatalf("removed application %q still exists", removeAppName)
	}
	if err := reconciler.Get(context.Background(), types.NamespacedName{Name: removeVersion.Name}, &appv2.ApplicationVersion{}); err == nil {
		t.Fatalf("removed application version %q still exists", removeVersion.Name)
	}
}

func TestRepoReconcilerPreservesApplicationsWhenOCIIndexIsPartial(t *testing.T) {
	server := newOCIControllerServer(t, []ociControllerChartFixture{
		{
			repository: "charts/valid",
			tag:        "1.0.0",
			digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			config:     `{"apiVersion":"v2","name":"valid","version":"1.0.0"}`,
		},
		{
			repository: "charts/warned",
			tag:        "1.0.0",
			digest:     "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			config:     `{"name":`,
		},
	})
	defer server.Close()

	repo := &appv2.Repo{
		ObjectMeta: metav1.ObjectMeta{Name: "partial-oci-repo", UID: types.UID("repo-uid")},
		Spec: appv2.RepoSpec{
			Url:        fmt.Sprintf("oci://%s", server.Listener.Addr()),
			Credential: appv2.RepoCredential{PlainHTTP: true},
			SyncPeriod: ptr.To(0),
		},
		Status: appv2.RepoStatus{State: appv2.StatusManualTrigger},
	}
	warnedAppName := repo.Name + "-" + appclient.GenerateShortNameMD5Hash("warned")
	warnedApp := &appv2.Application{ObjectMeta: metav1.ObjectMeta{
		Name:   warnedAppName,
		Labels: map[string]string{appv2.RepoIDLabelKey: repo.Name},
	}}
	warnedVersion := &appv2.ApplicationVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name:   warnedAppName + "-1.0.0",
			Labels: map[string]string{appv2.RepoIDLabelKey: repo.Name, appv2.AppIDLabelKey: warnedAppName},
		},
		Spec: appv2.ApplicationVersionSpec{VersionName: "1.0.0", Digest: "sha256:old"},
	}
	validAppName := repo.Name + "-" + appclient.GenerateShortNameMD5Hash("valid")
	validApp := &appv2.Application{ObjectMeta: metav1.ObjectMeta{
		Name:   validAppName,
		Labels: map[string]string{appv2.RepoIDLabelKey: repo.Name},
	}}
	staleValidVersion := &appv2.ApplicationVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name:   validAppName + "-0.9.0",
			Labels: map[string]string{appv2.RepoIDLabelKey: repo.Name, appv2.AppIDLabelKey: validAppName},
		},
		Spec: appv2.ApplicationVersionSpec{VersionName: "0.9.0", Digest: "sha256:old-valid"},
	}
	reconciler, _ := newRepoReconcilerTestClient(t, repo, warnedApp, warnedVersion, validApp, staleValidVersion)

	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: repo.Name}}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if err := reconciler.Get(context.Background(), types.NamespacedName{Name: warnedApp.Name}, &appv2.Application{}); err != nil {
		t.Fatalf("warned application was removed: %v", err)
	}
	if err := reconciler.Get(context.Background(), types.NamespacedName{Name: warnedVersion.Name}, &appv2.ApplicationVersion{}); err != nil {
		t.Fatalf("warned application version was removed: %v", err)
	}
	if err := reconciler.Get(context.Background(), types.NamespacedName{Name: staleValidVersion.Name}, &appv2.ApplicationVersion{}); err != nil {
		t.Fatalf("version omitted from partial index was removed: %v", err)
	}
	if err := reconciler.Get(context.Background(), types.NamespacedName{Name: validAppName}, &appv2.Application{}); err != nil {
		t.Fatalf("valid application was not synchronized: %v", err)
	}
}

func TestRepoReconcilerUpdatesOCIChartWhenDigestChanges(t *testing.T) {
	const newDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	server := newOCIControllerServer(t, []ociControllerChartFixture{{
		repository: "charts/demo",
		tag:        "1.0.0",
		digest:     newDigest,
		config:     `{"apiVersion":"v2","name":"demo","version":"1.0.0","description":"updated"}`,
	}})
	defer server.Close()

	repo := &appv2.Repo{
		ObjectMeta: metav1.ObjectMeta{Name: "changed-digest-repo", UID: types.UID("repo-uid")},
		Spec: appv2.RepoSpec{
			Url:        fmt.Sprintf("oci://%s", server.Listener.Addr()),
			Credential: appv2.RepoCredential{PlainHTTP: true},
			SyncPeriod: ptr.To(0),
		},
		Status: appv2.RepoStatus{State: appv2.StatusManualTrigger},
	}
	appName := repo.Name + "-" + appclient.GenerateShortNameMD5Hash("demo")
	app := &appv2.Application{ObjectMeta: metav1.ObjectMeta{Name: appName, Labels: map[string]string{appv2.RepoIDLabelKey: repo.Name}}}
	oldCreated := &metav1.Time{Time: time.Unix(123, 0)}
	version := &appv2.ApplicationVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name:   appName + "-1.0.0",
			Labels: map[string]string{appv2.RepoIDLabelKey: repo.Name, appv2.AppIDLabelKey: appName},
		},
		Spec: appv2.ApplicationVersionSpec{VersionName: "1.0.0", Digest: "sha256:old", Created: oldCreated},
	}
	reconciler, _ := newRepoReconcilerTestClient(t, repo, app, version)

	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: repo.Name}}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	updated := &appv2.ApplicationVersion{}
	if err := reconciler.Get(context.Background(), types.NamespacedName{Name: version.Name}, updated); err != nil {
		t.Fatalf("get application version: %v", err)
	}
	if updated.Spec.Digest != newDigest {
		t.Fatalf("application version digest = %q, want %q", updated.Spec.Digest, newDigest)
	}
	if updated.Spec.Created.Equal(oldCreated) {
		t.Fatalf("application version Created = %v, want update after digest change", updated.Spec.Created)
	}
}

func TestRepoReconcilerMarksOCIRepoFailedWhenOnlyInvalidArtifactsAreFound(t *testing.T) {
	server := newOCIControllerServer(t, []ociControllerChartFixture{{
		repository: "charts/invalid",
		tag:        "1.0.0",
		digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		config:     `{"name":`,
	}})
	defer server.Close()

	repo := &appv2.Repo{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid-artifacts-repo"},
		Spec: appv2.RepoSpec{
			Url:        fmt.Sprintf("oci://%s", server.Listener.Addr()),
			Credential: appv2.RepoCredential{PlainHTTP: true},
			SyncPeriod: ptr.To(0),
		},
		Status: appv2.RepoStatus{State: appv2.StatusManualTrigger},
	}
	reconciler, _ := newRepoReconcilerTestClient(t, repo)

	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: repo.Name}}); err == nil {
		t.Fatal("Reconcile() error = nil, want no valid OCI Helm charts error")
	}
	updated := &appv2.Repo{}
	if err := reconciler.Get(context.Background(), types.NamespacedName{Name: repo.Name}, updated); err != nil {
		t.Fatalf("get repo: %v", err)
	}
	if updated.Status.State != appv2.StatusFailed {
		t.Fatalf("repo status = %q, want %q", updated.Status.State, appv2.StatusFailed)
	}
}

func TestRepoReconcilerEmitsOneWarningEventPerOCIIndexWarning(t *testing.T) {
	server := newOCIControllerServer(t, []ociControllerChartFixture{
		{
			repository: "charts/valid",
			tag:        "1.0.0",
			digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			config:     `{"apiVersion":"v2","name":"valid","version":"1.0.0"}`,
		},
		{
			repository: "charts/broken-one",
			tag:        "1.0.0",
			digest:     "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			config:     `{"name":`,
		},
		{
			repository: "charts/broken-two",
			tag:        "2.0.0",
			digest:     "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			config:     `{"name":`,
		},
	})
	defer server.Close()

	repo := &appv2.Repo{
		ObjectMeta: metav1.ObjectMeta{Name: "warning-events-repo", UID: types.UID("repo-uid")},
		Spec: appv2.RepoSpec{
			Url:        fmt.Sprintf("oci://%s", server.Listener.Addr()),
			Credential: appv2.RepoCredential{PlainHTTP: true},
			SyncPeriod: ptr.To(0),
		},
		Status: appv2.RepoStatus{State: appv2.StatusManualTrigger},
	}
	reconciler, recorder := newRepoReconcilerTestClient(t, repo)

	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: repo.Name}}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	warningEvents := 0
	warnedRepositories := map[string]bool{}
	for queued := len(recorder.Events); queued > 0; queued-- {
		event := <-recorder.Events
		if !strings.HasPrefix(event, corev1.EventTypeWarning+" OCIIndexWarning ") {
			continue
		}
		warningEvents++
		for _, repository := range []string{"charts/broken-one", "charts/broken-two"} {
			if strings.Contains(event, repository) {
				warnedRepositories[repository] = true
			}
		}
	}
	if warningEvents != 2 {
		t.Fatalf("warning event count = %d, want 2", warningEvents)
	}
	if !warnedRepositories["charts/broken-one"] || !warnedRepositories["charts/broken-two"] {
		t.Fatalf("warning events covered repositories = %v", warnedRepositories)
	}
}

func TestRepoReconcilerMarksOCIRepoFailedWhenTagsCannotBeLoaded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" || r.URL.Path == "/v2" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	repo := &appv2.Repo{
		ObjectMeta: metav1.ObjectMeta{Name: "unreachable-oci-repo"},
		Spec: appv2.RepoSpec{
			Url:        "oci://" + server.Listener.Addr().String() + "/charts",
			Credential: appv2.RepoCredential{PlainHTTP: true},
			SyncPeriod: ptr.To(0),
		},
		Status: appv2.RepoStatus{State: appv2.StatusManualTrigger},
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core API to scheme: %v", err)
	}
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("add application API to scheme: %v", err)
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&appv2.Repo{}).WithObjects(repo).Build()
	if err := client.Status().Update(context.Background(), repo); err != nil {
		t.Fatalf("set repo status: %v", err)
	}
	stored := &appv2.Repo{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: repo.Name}, stored); err != nil {
		t.Fatalf("get repo after status update: %v", err)
	}
	if stored.Status.State != appv2.StatusManualTrigger {
		t.Fatalf("repo status = %q, want %q", stored.Status.State, appv2.StatusManualTrigger)
	}
	reconciler := &RepoReconciler{Client: client, recorder: record.NewFakeRecorder(1)}

	result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: repo.Name},
	})
	if err == nil {
		t.Fatal("Reconcile() error = nil, want tag loading error")
	}

	updated := &appv2.Repo{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: repo.Name}, updated); err != nil {
		t.Fatalf("get repo: %v", err)
	}
	if updated.Status.State != appv2.StatusFailed {
		t.Fatalf("repo status = %q, want %q", updated.Status.State, appv2.StatusFailed)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("requeue after = %s, want 0", result.RequeueAfter)
	}
}

func TestRepoReconcilerReusesCachedOCIChartTag(t *testing.T) {
	const chartRepo = "charts/demo"
	const manifestDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/", "/v2":
			w.WriteHeader(http.StatusOK)
		case "/v2/" + chartRepo + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0"}})
		case "/v2/" + chartRepo + "/manifests/1.0.0":
			w.Header().Set("Docker-Content-Digest", manifestDigest)
			_ = json.NewEncoder(w).Encode(ocispec.Manifest{Config: ocispec.Descriptor{MediaType: registry.ConfigMediaType, Digest: digest.FromString("cached-config")}, Layers: []ocispec.Descriptor{{MediaType: registry.ChartLayerMediaType}}})
		case "/v2/" + chartRepo + "/blobs/" + digest.FromString("cached-config").String():
			_, _ = w.Write([]byte(`{"apiVersion":"v2","name":"demo","version":"1.0.0"}`))
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
	appName := repo.Name + "-" + appclient.GenerateShortNameMD5Hash("demo")
	app := &appv2.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name:   appName,
			Labels: map[string]string{appv2.RepoIDLabelKey: repo.Name},
			Annotations: map[string]string{
				appv2.AppOriginalNameLabelKey: "demo",
			},
		},
	}
	version := &appv2.ApplicationVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name: appName + "-1.0.0",
			Labels: map[string]string{
				appv2.RepoIDLabelKey: repo.Name,
				appv2.AppIDLabelKey:  appName,
			},
		},
		Spec: appv2.ApplicationVersionSpec{
			VersionName: "1.0.0",
			Digest:      manifestDigest,
			Created:     &metav1.Time{Time: time.Unix(123, 0)},
			PullUrl:     fmt.Sprintf("oci://%s/%s:1.0.0", server.Listener.Addr(), chartRepo),
		},
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core API to scheme: %v", err)
	}
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("add application API to scheme: %v", err)
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&appv2.Repo{}, &appv2.Application{}, &appv2.ApplicationVersion{}).WithObjects(repo, app, version).Build()
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
	updatedVersion := &appv2.ApplicationVersion{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: version.Name}, updatedVersion); err != nil {
		t.Fatalf("get application version: %v", err)
	}
	if updatedVersion.Spec.Digest != manifestDigest {
		t.Fatalf("application version digest = %q, want %s", updatedVersion.Spec.Digest, manifestDigest)
	}
	if !updatedVersion.Spec.Created.Equal(version.Spec.Created) {
		t.Fatalf("application version Created changed from %v to %v", version.Spec.Created, updatedVersion.Spec.Created)
	}
}

func TestRepoReconcilerSyncsNewRepoWhenPeriodicSyncIsDisabled(t *testing.T) {
	const chartRepo = "charts/demo"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/", "/v2":
			w.WriteHeader(http.StatusOK)
		case "/v2/" + chartRepo + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0"}})
		case "/v2/" + chartRepo + "/manifests/1.0.0":
			w.Header().Set("Docker-Content-Digest", "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
			_ = json.NewEncoder(w).Encode(ocispec.Manifest{Config: ocispec.Descriptor{MediaType: registry.ConfigMediaType, Digest: digest.FromString("new-config")}, Layers: []ocispec.Descriptor{{MediaType: registry.ChartLayerMediaType}}})
		case "/v2/" + chartRepo + "/blobs/" + digest.FromString("new-config").String():
			_, _ = w.Write([]byte(`{"apiVersion":"v2","name":"demo","version":"1.0.0"}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	repo := &appv2.Repo{
		ObjectMeta: metav1.ObjectMeta{Name: "new-oci-repo"},
		Spec: appv2.RepoSpec{
			Url:        fmt.Sprintf("oci://%s/%s", server.Listener.Addr(), chartRepo),
			Credential: appv2.RepoCredential{PlainHTTP: true},
			SyncPeriod: ptr.To(0),
		},
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core API to scheme: %v", err)
	}
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("add application API to scheme: %v", err)
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&appv2.Repo{}, &appv2.Application{}, &appv2.ApplicationVersion{}).WithObjects(repo).Build()
	reconciler := &RepoReconciler{Client: client, recorder: record.NewFakeRecorder(1)}

	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: repo.Name}}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	createdApp := &appv2.Application{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: repo.Name + "-demo"}, createdApp); err != nil {
		t.Fatalf("get synchronized application: %v", err)
	}
}

func TestRepoReconcilerCreatesAllInspectedOCIChartVersions(t *testing.T) {
	const chartRepo = "charts/demo"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/", "/v2":
			w.WriteHeader(http.StatusOK)
		case "/v2/" + chartRepo + "/tags/list":
			_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"1.0.0", "2.0.0", "3.0.0"}})
		case "/v2/" + chartRepo + "/manifests/1.0.0", "/v2/" + chartRepo + "/manifests/2.0.0", "/v2/" + chartRepo + "/manifests/3.0.0":
			w.Header().Set("Docker-Content-Digest", "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
			_ = json.NewEncoder(w).Encode(ocispec.Manifest{Config: ocispec.Descriptor{MediaType: registry.ConfigMediaType, Digest: digest.FromString("all-config")}, Layers: []ocispec.Descriptor{{MediaType: registry.ChartLayerMediaType}}})
		case "/v2/" + chartRepo + "/blobs/" + digest.FromString("all-config").String():
			_, _ = w.Write([]byte(`{"apiVersion":"v2","name":"demo","version":"1.0.0"}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	repo := &appv2.Repo{
		ObjectMeta: metav1.ObjectMeta{Name: "all-versions-oci-repo"},
		Spec: appv2.RepoSpec{
			Url:        fmt.Sprintf("oci://%s/%s", server.Listener.Addr(), chartRepo),
			Credential: appv2.RepoCredential{PlainHTTP: true},
			SyncPeriod: ptr.To(0),
		},
		Status: appv2.RepoStatus{State: appv2.StatusManualTrigger},
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core API to scheme: %v", err)
	}
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("add application API to scheme: %v", err)
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&appv2.Repo{}, &appv2.Application{}, &appv2.ApplicationVersion{}).WithObjects(repo).Build()
	reconciler := &RepoReconciler{Client: client, recorder: record.NewFakeRecorder(1)}

	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: repo.Name}}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	versions := &appv2.ApplicationVersionList{}
	if err := client.List(context.Background(), versions); err != nil {
		t.Fatalf("list synchronized application versions: %v", err)
	}
	if got := len(versions.Items); got != 3 {
		t.Fatalf("application version count = %d, want 3", got)
	}
	for _, version := range versions.Items {
		if version.Spec.Digest == "" {
			t.Errorf("application version %s has empty digest", version.Name)
		}
	}
}
