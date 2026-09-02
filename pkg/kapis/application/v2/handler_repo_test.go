/*
 * Copyright 2024 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 * https://github.com/kubesphere/kubesphere/blob/master/LICENSE
 */

package v2

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	appv2 "kubesphere.io/api/application/v2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"kubesphere.io/kubesphere/pkg/constants"
)

func TestLoadRepoCredentialSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	insecure := false
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: constants.KubeSphereNamespace,
			Name:      "repo-cred",
		},
		Data: map[string][]byte{
			"username":              []byte("admin"),
			"password":              []byte("password"),
			"certFile":              []byte("/etc/certs/tls.crt"),
			"keyFile":               []byte("/etc/certs/tls.key"),
			"caFile":                []byte("/etc/certs/ca.crt"),
			"insecureSkipTLSVerify": []byte("true"),
			"plainHTTP":             []byte("true"),
		},
	}
	h := &appHandler{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()}
	credential := appv2.RepoCredential{
		Username:              "old-user",
		Password:              "old-password",
		InsecureSkipTLSVerify: &insecure,
	}

	err := h.loadRepoCredentialSecret(context.Background(), &corev1.SecretReference{Name: "repo-cred"}, &credential)
	if err != nil {
		t.Fatalf("loadRepoCredentialSecret() error = %v", err)
	}

	if credential.Username != "admin" || credential.Password != "password" {
		t.Fatalf("unexpected basic auth: %#v", credential)
	}
	if credential.CertFile != "/etc/certs/tls.crt" || credential.KeyFile != "/etc/certs/tls.key" || credential.CAFile != "/etc/certs/ca.crt" {
		t.Fatalf("unexpected tls config: %#v", credential)
	}
	if credential.InsecureSkipTLSVerify == nil || !*credential.InsecureSkipTLSVerify {
		t.Fatalf("expected insecureSkipTLSVerify=true, got %#v", credential.InsecureSkipTLSVerify)
	}
	if !credential.PlainHTTP {
		t.Fatal("expected plainHTTP=true")
	}
}

func TestLoadRepoCredentialSecretInvalidBool(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "repo-secrets",
			Name:      "repo-cred",
		},
		Data: map[string][]byte{"insecureSkipTLSVerify": []byte("sometimes")},
	}
	h := &appHandler{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()}

	err := h.loadRepoCredentialSecret(context.Background(), &corev1.SecretReference{Namespace: "repo-secrets", Name: "repo-cred"}, &appv2.RepoCredential{})
	if err == nil {
		t.Fatal("expected invalid boolean error")
	}
}
