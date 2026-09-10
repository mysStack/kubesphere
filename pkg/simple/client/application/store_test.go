package application

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	appv2 "kubesphere.io/api/application/v2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"kubesphere.io/kubesphere/pkg/constants"
)

func TestDownLoadChartUsesRepoCredentialSecret(t *testing.T) {
	const username = "repo-user"
	const password = "repo-password"
	usedSecretCredential := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" && r.Method == http.MethodGet {
			gotUsername, gotPassword, ok := r.BasicAuth()
			usedSecretCredential = ok && gotUsername == username && gotPassword == password
			if !usedSecretCredential {
				w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core API to scheme: %v", err)
	}
	if err := appv2.AddToScheme(scheme); err != nil {
		t.Fatalf("add application API to scheme: %v", err)
	}
	repo := &appv2.Repo{
		ObjectMeta: metav1.ObjectMeta{Name: "private-oci"},
		Spec: appv2.RepoSpec{
			CredentialSecretRef: &corev1.SecretReference{Name: "repo-cred"},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: constants.KubeSphereNamespace, Name: "repo-cred"},
		Data: map[string][]byte{
			"username":  []byte(username),
			"password":  []byte(password),
			"plainHTTP": []byte("true"),
		},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(repo, secret).Build()

	_, _ = DownLoadChart(client, fmt.Sprintf("oci://%s/charts/demo:1.0.0", server.Listener.Addr()), repo.Name)
	if !usedSecretCredential {
		t.Fatal("OCI chart download did not use credentialSecretRef")
	}
}
