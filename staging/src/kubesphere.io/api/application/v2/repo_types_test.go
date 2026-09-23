package v2

import (
	"encoding/json"
	"testing"
)

func TestRepoSpecPreservesOCISecretReferenceAndPlainHTTP(t *testing.T) {
	var repo Repo
	if err := json.Unmarshal([]byte(`{
		"spec": {
			"url": "oci://registry.example.com/team/chart",
			"credential": {"plainHTTP": true},
			"credentialSecretRef": {"name": "registry-credential"}
		}
	}`), &repo); err != nil {
		t.Fatalf("unmarshal repo: %v", err)
	}

	data, err := json.Marshal(repo)
	if err != nil {
		t.Fatalf("marshal repo: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal marshalled repo: %v", err)
	}
	spec := raw["spec"].(map[string]any)
	credential := spec["credential"].(map[string]any)
	if got, ok := credential["plainHTTP"].(bool); !ok || !got {
		t.Fatalf("plainHTTP was not preserved: %#v", credential)
	}
	secretRef, ok := spec["credentialSecretRef"].(map[string]any)
	if !ok || secretRef["name"] != "registry-credential" {
		t.Fatalf("credentialSecretRef was not preserved: %#v", spec["credentialSecretRef"])
	}
}
