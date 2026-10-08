/*
 * Copyright 2024 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 * https://github.com/kubesphere/kubesphere/blob/master/LICENSE
 */

package confighistory

import (
	"strings"
	"testing"
	"time"
)

func TestManagedBy(t *testing.T) {
	cases := []struct {
		name        string
		annotations map[string]string
		wantKind    string
		wantRef     string
	}{
		{
			name:        "helm managed object carries the release name",
			annotations: map[string]string{"meta.helm.sh/release-name": "wes-server", "meta.helm.sh/release-namespace": "dev-wes"},
			wantKind:    ManagedByHelm,
			wantRef:     "wes-server",
		},
		{
			name:        "replicator synced object carries the source version",
			annotations: map[string]string{"replicator.v1.mittwald.de/replicated-at": "2026-09-19T15:44:39Z", replicatedFromVersionAnnotation: "102574264"},
			wantKind:    ManagedByReplicator,
			wantRef:     "102574264",
		},
		{
			name:        "anything else is directly managed",
			annotations: map[string]string{"kubesphere.io/creator": "admin"},
			wantKind:    ManagedByDirect,
			wantRef:     "",
		},
		{
			name:        "no annotations at all",
			annotations: nil,
			wantKind:    ManagedByDirect,
			wantRef:     "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, ref := ManagedBy(c.annotations)
			if kind != c.wantKind || ref != c.wantRef {
				t.Fatalf("ManagedBy() = (%q, %q), want (%q, %q)", kind, ref, c.wantKind, c.wantRef)
			}
		})
	}
}

func TestIsExcluded(t *testing.T) {
	cases := []struct {
		kind string
		name string
		want bool
	}{
		{"ConfigMap", "ewms-postgres-wes-config", false},
		{"Secret", "aliyun-registry-secret", false},
		{"Secret", "sh.helm.release.v1.wes-server.v2", true},
		{"Secret", "some-config-history", true},
		{"ConfigMap", "some-config-history", false},
	}
	for _, c := range cases {
		if got := IsExcluded(c.kind, c.name); got != c.want {
			t.Errorf("IsExcluded(%q, %q) = %v, want %v", c.kind, c.name, got, c.want)
		}
	}
}

func TestContentHashIsStableAcrossMapOrder(t *testing.T) {
	a := map[string]string{"DB_USER": "wes", "DB_URL": "postgres://x", "DB_PORT": "5432"}
	b := map[string]string{"DB_PORT": "5432", "DB_USER": "wes", "DB_URL": "postgres://x"}
	if ContentHash(a) != ContentHash(b) {
		t.Fatal("ContentHash must not depend on iteration order")
	}
	c := map[string]string{"DB_USER": "wes_writer", "DB_URL": "postgres://x", "DB_PORT": "5432"}
	if ContentHash(a) == ContentHash(c) {
		t.Fatal("ContentHash must change when a value changes")
	}
}

func TestShouldAppendOnlyWhenContentChanges(t *testing.T) {
	content := map[string]string{"A": "1"}
	if !ShouldAppend(nil, content) {
		t.Fatal("an object with no history should get its first record")
	}
	records := []Record{{Revision: 1, ContentHash: ContentHash(content)}}
	if ShouldAppend(records, content) {
		t.Fatal("unchanged content must not produce a record")
	}
	if !ShouldAppend(records, map[string]string{"A": "2"}) {
		t.Fatal("changed content must produce a record")
	}
}

func TestAppendKeepsNewestFirstAndCapsAtMaxRecords(t *testing.T) {
	var records []Record
	for i := 1; i <= MaxRecords+3; i++ {
		records = Append(records, Record{Revision: i, CreatedAt: time.Unix(int64(i), 0).UTC()})
	}
	if len(records) != MaxRecords {
		t.Fatalf("len(records) = %d, want %d", len(records), MaxRecords)
	}
	if records[0].Revision != MaxRecords+3 {
		t.Fatalf("newest record = %d, want %d", records[0].Revision, MaxRecords+3)
	}
	if records[len(records)-1].Revision != 4 {
		t.Fatalf("oldest kept record = %d, want 4 (1..3 dropped)", records[len(records)-1].Revision)
	}
}

func TestNewRecordOmitsOversizedContent(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	small := NewRecord(nil, map[string]string{"A": "1"}, nil, now)
	if small.ContentOmitted || small.Content["A"] != "1" {
		t.Fatalf("small content should be stored: %+v", small)
	}
	if small.Revision != 1 || !small.CreatedAt.Equal(now) {
		t.Fatalf("unexpected revision or time: %+v", small)
	}

	big := NewRecord(nil, map[string]string{"A": strings.Repeat("x", MaxContentBytes+1)}, nil, now)
	if !big.ContentOmitted || big.Content != nil {
		t.Fatalf("oversized content must be omitted but the record kept: %+v", big)
	}
	if big.ContentHash == "" {
		t.Fatal("an omitted record still needs its hash, otherwise the next change is undetectable")
	}
}

func TestNewRecordDerivesManagedByAndRevision(t *testing.T) {
	now := time.Now()
	existing := []Record{{Revision: 4}, {Revision: 2}}
	rec := NewRecord(existing, map[string]string{"A": "1"}, map[string]string{"meta.helm.sh/release-name": "wes"}, now)
	if rec.Revision != 5 {
		t.Fatalf("revision = %d, want 5", rec.Revision)
	}
	if rec.ManagedBy != ManagedByHelm || rec.ManagedByRef != "wes" {
		t.Fatalf("managed by = (%q, %q), want (helm, wes)", rec.ManagedBy, rec.ManagedByRef)
	}
}

func TestContentExtraction(t *testing.T) {
	secret := ContentFromSecret(map[string][]byte{
		"DB_USERNAME": []byte("wes_reader"),
		"DB_PASSWORD": []byte("s3cr3t"),
		"BLOB":        {0x00, 0xff, 0x10},
	})
	if secret["DB_USERNAME"] != "wes_reader" {
		t.Fatalf("text value mangled: %q", secret["DB_USERNAME"])
	}
	if secret["BLOB"] == "" || strings.ContainsAny(secret["BLOB"], "\x00") {
		t.Fatalf("binary value must fall back to base64, got %q", secret["BLOB"])
	}

	cm := ContentFromConfigMap(
		map[string]string{"DB_PORT": "5432"},
		map[string][]byte{"CERT": []byte("pem")},
	)
	if cm["DB_PORT"] != "5432" || cm["CERT"] != "cGVt" {
		t.Fatalf("unexpected configmap content: %+v", cm)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	records := []Record{
		{Revision: 2, CreatedAt: time.Unix(2, 0).UTC(), ManagedBy: ManagedByHelm, ManagedByRef: "wes", ContentHash: "h2", Content: map[string]string{"A": "2"}},
		{Revision: 1, CreatedAt: time.Unix(1, 0).UTC(), ManagedBy: ManagedByDirect, ContentHash: "h1", Content: map[string]string{"A": "1"}},
	}
	encoded, err := Encode(records)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(decoded) != 2 || decoded[0].Revision != 2 || decoded[1].Content["A"] != "1" {
		t.Fatalf("round trip lost data: %+v", decoded)
	}
	if decoded[0].ManagedByRef != "wes" {
		t.Fatalf("round trip lost managedByRef: %+v", decoded[0])
	}
}

func TestDecodeEmptyPayload(t *testing.T) {
	records, err := Decode(nil)
	if err != nil || records != nil {
		t.Fatalf("Decode(nil) = (%v, %v), want (nil, nil)", records, err)
	}
	if _, err := Decode([]byte("not-base64!!")); err == nil {
		t.Fatal("Decode must reject a payload that is not base64")
	}
}

func TestHistoryNameRoundTrip(t *testing.T) {
	if got := HistoryName("ewms-postgres-wes-config"); got != "ewms-postgres-wes-config-history" {
		t.Fatalf("HistoryName() = %q", got)
	}
	name, ok := SourceName("ewms-postgres-wes-config-history")
	if !ok || name != "ewms-postgres-wes-config" {
		t.Fatalf("SourceName() = (%q, %v)", name, ok)
	}
	if _, ok := SourceName("plain-secret"); ok {
		t.Fatal("SourceName must reject names that are not history objects")
	}
}

func TestIsHistoryObject(t *testing.T) {
	if !IsHistoryObject("Secret", "x-history", nil) {
		t.Fatal("a -history Secret must be recognised")
	}
	if !IsHistoryObject("Secret", "whatever", map[string]string{HistoryLabelKey: HistoryLabelValue}) {
		t.Fatal("the ownership label must be enough on its own")
	}
	if IsHistoryObject("ConfigMap", "x-history", nil) {
		t.Fatal("a ConfigMap named -history is not ours")
	}
	if IsHistoryObject("Secret", "aliyun-registry-secret", nil) {
		t.Fatal("an ordinary Secret must not be treated as ours")
	}
}
