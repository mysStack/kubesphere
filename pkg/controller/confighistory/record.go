/*
 * Copyright 2024 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 * https://github.com/kubesphere/kubesphere/blob/master/LICENSE
 */

// Package confighistory implements modification history for ConfigMaps and Secrets.
//
// The records are produced by watching the objects, not by stamping them on write. That
// is deliberate: most changes in a real cluster come from Helm, kubectl and CI, which do
// not go through the console, so a write-time stamp would stay empty.
//
// This file holds the pure logic only -- no Kubernetes types, no client. The store and
// the reconciler live next to it and build on these functions.
package confighistory

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

const (
	// MaxRecords is how many records are kept per object. It matches what the workload
	// revision page documents, so both views look consistent to a user.
	MaxRecords = 10

	// MaxContentBytes caps a single record's content. A Secret in this cluster can be
	// 1.28MB, so an unlimited history would be unbounded; when over the cap we keep the
	// record (time and hash) but omit the content.
	MaxContentBytes = 32 * 1024

	// HistorySecretSuffix is appended to the source object name to name its history Secret.
	HistorySecretSuffix = "-history"

	// ManagedBy* are the three values that can be derived reliably from annotations.
	ManagedByHelm       = "helm"
	ManagedByReplicator = "replicator"
	ManagedByDirect     = "direct"

	// HistoryLabelKey marks the objects this feature owns, so it never records itself.
	HistoryLabelKey = "config-history.kubesphere.io/for"

	// HistoryLabelValue is the value of HistoryLabelKey.
	HistoryLabelValue = "true"

	// replicatedFromVersionAnnotation is where kubernetes-replicator records which source
	// version it copied from.
	replicatedFromVersionAnnotation = "replicator.v1.mittwald.de/replicated-from-version"

	// helmReleaseSecretPrefix marks Helm's own release state. Recording those would be
	// pointless (they change on every Helm operation) and they are by far the largest
	// objects in the cluster.
	helmReleaseSecretPrefix = "sh.helm.release.v1."
)

// Record is one entry of an object's modification history.
type Record struct {
	Revision       int               `json:"revision"`
	CreatedAt      time.Time         `json:"createdAt"`
	ManagedBy      string            `json:"managedBy"`
	ManagedByRef   string            `json:"managedByRef,omitempty"`
	ContentHash    string            `json:"contentHash"`
	Content        map[string]string `json:"content,omitempty"`
	ContentOmitted bool              `json:"contentOmitted,omitempty"`
}

// ManagedBy derives the "managed by" classification from an object's annotations.
//
// This is deliberately not "who changed it". metadata.managedFields would be the only
// field carrying the caller, and it is empty in this cluster (393 ConfigMaps and 309
// Secrets, zero managedFields entries), so the caller cannot be derived at all.
// Pinpointing one specific change would need apiserver audit logs, which this feature
// does not require.
func ManagedBy(annotations map[string]string) (kind, ref string) {
	if annotations == nil {
		return ManagedByDirect, ""
	}
	if release := annotations["meta.helm.sh/release-name"]; release != "" {
		return ManagedByHelm, release
	}
	for k := range annotations {
		if strings.HasPrefix(k, "replicator.v1.mittwald.de/") {
			return ManagedByReplicator, annotations[replicatedFromVersionAnnotation]
		}
	}
	return ManagedByDirect, ""
}

// IsExcluded reports whether an object should not be recorded at all.
// kind is "ConfigMap" or "Secret".
func IsExcluded(kind, name string) bool {
	if kind == "Secret" && strings.HasPrefix(name, helmReleaseSecretPrefix) {
		return true
	}
	// Our own record objects must never be recorded, otherwise every write would enqueue
	// another reconcile and the controller would feed itself.
	return IsHistoryObject(kind, name, nil)
}

// IsHistoryObject reports whether this object is one of our own record holders.
func IsHistoryObject(kind, name string, labels map[string]string) bool {
	if labels[HistoryLabelKey] == HistoryLabelValue {
		return true
	}
	return kind == "Secret" && strings.HasSuffix(name, HistorySecretSuffix)
}

// HistoryName returns the name of the history Secret for a source object.
func HistoryName(sourceName string) string {
	return sourceName + HistorySecretSuffix
}

// SourceName returns the source object name for a history Secret name, and whether the
// name looks like a history Secret at all.
func SourceName(historyName string) (string, bool) {
	if !strings.HasSuffix(historyName, HistorySecretSuffix) {
		return "", false
	}
	return strings.TrimSuffix(historyName, HistorySecretSuffix), true
}

// WorkspaceLabelKey marks a KubeSphere project namespace. Only namespaces carrying it are
// recorded.
//
// The scope is not optional. Without it the controllers watch and record cluster-wide: on
// their first deployment they created 516 history Secrets in 15 minutes, including inside
// kubesphere-system, kubesphere-monitoring-system and alerting. Those are not project
// resources, and the object count is the real cost of this feature, not the payload size.
const WorkspaceLabelKey = "kubesphere.io/workspace"

// SystemWorkspace is the workspace KubeSphere keeps its own namespaces in. Everything there is
// platform machinery (default, kube-system, kubesphere-system, extension-*), not a project.
const SystemWorkspace = "system-workspace"

// IsManagedNamespace reports whether a namespace is in scope for recording.
//
// The workspace label alone is NOT a discriminator: every system namespace carries it too,
// because KubeSphere puts them all in system-workspace. Measured on this cluster: 25
// namespaces in system-workspace against 13 in dev-workspace, 15 in test-workspace and 6 in
// public-workspace. Using the label by itself selected 59 of 61 namespaces, including
// kube-system and kubesphere-system, and produced 513 history objects in a single run.
//
// So a namespace is in scope when it belongs to a workspace and that workspace is not
// system-workspace. The two namespaces without any workspace label (argo-events,
// kubesphere-reloader) are also out of scope, which is correct: they are extension-managed.
func IsManagedNamespace(namespaceLabels map[string]string) bool {
	workspace := namespaceLabels[WorkspaceLabelKey]
	return workspace != "" && workspace != SystemWorkspace
}

// ContentHash fingerprints content so an unchanged object does not produce a record.
// Keys are sorted, so the value is stable across map iteration order.
func ContentHash(content map[string]string) string {
	keys := make([]string, 0, len(content))
	for k := range content {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s\x00%s\x00", k, content[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ShouldAppend reports whether content differs from the newest record. Records are kept
// newest first.
func ShouldAppend(records []Record, content map[string]string) bool {
	if len(records) == 0 {
		return true
	}
	return records[0].ContentHash != ContentHash(content)
}

// NextRevision returns the revision number for a new record.
func NextRevision(records []Record) int {
	max := 0
	for _, r := range records {
		if r.Revision > max {
			max = r.Revision
		}
	}
	return max + 1
}

// Append adds a record at the front (newest first) and trims to MaxRecords.
func Append(records []Record, rec Record) []Record {
	out := make([]Record, 0, len(records)+1)
	out = append(out, rec)
	out = append(out, records...)
	if len(out) > MaxRecords {
		out = out[:MaxRecords]
	}
	return out
}

// NewRecord builds a record for the given content, omitting the content when it exceeds
// MaxContentBytes.
func NewRecord(records []Record, content map[string]string, annotations map[string]string, now time.Time) Record {
	kind, ref := ManagedBy(annotations)
	rec := Record{
		Revision:     NextRevision(records),
		CreatedAt:    now.UTC(),
		ManagedBy:    kind,
		ManagedByRef: ref,
		ContentHash:  ContentHash(content),
	}
	if encodedSize(content) > MaxContentBytes {
		rec.ContentOmitted = true
		return rec
	}
	rec.Content = content
	return rec
}

func encodedSize(content map[string]string) int {
	n := 0
	for k, v := range content {
		n += len(k) + len(v)
	}
	return n
}

// ContentFromSecret decodes a Secret's data into comparable text. Values that are not
// text keep their base64 form, so nothing is silently mangled.
func ContentFromSecret(data map[string][]byte) map[string]string {
	out := make(map[string]string, len(data))
	for k, v := range data {
		if isText(v) {
			out[k] = string(v)
			continue
		}
		out[k] = base64.StdEncoding.EncodeToString(v)
	}
	return out
}

// ContentFromConfigMap merges data and binaryData; binary values are base64 encoded.
func ContentFromConfigMap(data map[string]string, binaryData map[string][]byte) map[string]string {
	out := make(map[string]string, len(data)+len(binaryData))
	for k, v := range data {
		out[k] = v
	}
	for k, v := range binaryData {
		out[k] = base64.StdEncoding.EncodeToString(v)
	}
	return out
}

func isText(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return false
		}
	}
	return true
}

// Encode serialises records as gzip+base64 -- the same shape Helm uses for its release
// payloads -- to keep the history Secret small.
func Encode(records []Record) ([]byte, error) {
	raw, err := json.Marshal(records)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return []byte(base64.StdEncoding.EncodeToString(buf.Bytes())), nil
}

// Decode reverses Encode. An empty payload decodes to no records.
func Decode(raw []byte) ([]Record, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, nil
	}
	compressed, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return nil, fmt.Errorf("history payload is not base64: %w", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("history payload is not gzip: %w", err)
	}
	defer zr.Close()
	data, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	var records []Record
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	return records, nil
}
