package v2

import (
	"encoding/json"
	"testing"
)

func TestRepoSyncStatusMarshalsZeroMetrics(t *testing.T) {
	data, err := json.Marshal(RepoSyncStatus{})
	if err != nil {
		t.Fatalf("marshal sync status: %v", err)
	}

	var status map[string]any
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatalf("unmarshal sync status: %v", err)
	}
	for _, field := range []string{
		"durationSeconds",
		"validChartVersionCount",
		"remoteTagCount",
		"skippedArtifactCount",
		"failedTagCount",
		"requestCount",
		"cacheHitCount",
	} {
		if got, ok := status[field]; !ok || got != float64(0) {
			t.Errorf("%s = %v, %t; want 0, true", field, got, ok)
		}
	}
}
