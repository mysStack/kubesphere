/*
 * Copyright 2026 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 */

package application

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	appv2 "kubesphere.io/api/application/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"kubesphere.io/kubesphere/pkg/constants"
)

// LoadRepoCredentialSecret overlays a repo credential with values stored in its Secret reference.
func LoadRepoCredentialSecret(ctx context.Context, reader client.Reader, ref *corev1.SecretReference, credential *appv2.RepoCredential) error {
	if ref == nil {
		return nil
	}
	if ref.Name == "" {
		return fmt.Errorf("credentialSecretRef.name is required")
	}

	namespace := ref.Namespace
	if namespace == "" {
		namespace = constants.KubeSphereNamespace
	}

	secret := &corev1.Secret{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, secret); err != nil {
		return err
	}

	setStringFromSecret(secret, "username", &credential.Username)
	setStringFromSecret(secret, "password", &credential.Password)
	setStringFromSecret(secret, "certFile", &credential.CertFile)
	setStringFromSecret(secret, "keyFile", &credential.KeyFile)
	setStringFromSecret(secret, "caFile", &credential.CAFile)
	if err := setBoolPtrFromSecret(secret, "insecureSkipTLSVerify", &credential.InsecureSkipTLSVerify); err != nil {
		return err
	}
	return setBoolFromSecret(secret, "plainHTTP", &credential.PlainHTTP)
}

func setStringFromSecret(secret *corev1.Secret, key string, dst *string) {
	if value, ok := secret.Data[key]; ok {
		*dst = string(value)
	}
}

func setBoolPtrFromSecret(secret *corev1.Secret, key string, dst **bool) error {
	if value, ok := secret.Data[key]; ok {
		parsed, err := parseSecretBool(key, value)
		if err != nil {
			return err
		}
		*dst = &parsed
	}
	return nil
}

func setBoolFromSecret(secret *corev1.Secret, key string, dst *bool) error {
	if value, ok := secret.Data[key]; ok {
		parsed, err := parseSecretBool(key, value)
		if err != nil {
			return err
		}
		*dst = parsed
	}
	return nil
}

func parseSecretBool(key string, value []byte) (bool, error) {
	parsed, err := strconv.ParseBool(strings.TrimSpace(string(value)))
	if err != nil {
		return false, fmt.Errorf("invalid boolean value for secret key %q: %w", key, err)
	}
	return parsed, nil
}
