// Package triggers holds the deterministic Kubernetes-side pre-filter rules:
// storage symptoms visible in a node's Kubernetes state that SMART cannot
// see, such as a failing cable, a corrupt filesystem or a full disk.
//
// It mirrors pkg/host/prefilter. Rules are pure functions over a
// schema.K8sCapture, the same raw API JSON a fixture stores, so the eval
// replays them exactly as the live sweep runs them. The package decodes the
// core API types but has no client: it reads nothing from a cluster.
//
// A rule firing means "look at this node", never "storage broke this
// workload". The reasoning layer decides what, if anything, the signal
// means. And like every trigger, a rule ID never reaches the model: the
// evidence that fired it is already in the capture the model reads.
//
// Matching runs in-process on the raw capture, before redaction. Nothing
// here leaves the process.
//
// Deliberately not triggers: crash loops, OOM kills, exit 137, image pull
// failures and probe failures on their own. They are common and mostly not
// storage, and raising on them would send a large share of all nodes to a
// model that leans toward "causal". See docs/proposals/0001.
package triggers

import (
	"encoding/json"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/00Webbo/tropis/pkg/schema"
)

// Rule IDs. These appear in Verdict.TriggeredBy, so they are part of the
// output contract.
const (
	// RuleDiskPressure fires on the kubelet's own DiskPressure condition.
	RuleDiskPressure = "k8s.disk_pressure"
	// RuleStorageEviction fires on an eviction or reclaim failure caused by
	// node storage.
	RuleStorageEviction = "k8s.storage_eviction"
	// RuleStorageError fires on a storage errno text from a failing
	// container, or in a Warning event for the node or its pods.
	RuleStorageError = "k8s.storage_error"
)

// StorageErrorSignatures are the C library's strerror texts for the errnos a
// storage fault produces, lower-cased. They are matched case-insensitively.
//
// They come from errno semantics, not from any corpus: Go, Java, Python,
// PostgreSQL and the shell all print the libc text, so the same signature
// holds across languages. Changing this list changes results, like a prompt
// change, and must be recorded as such.
var StorageErrorSignatures = []string{
	"input/output error",       // EIO
	"read-only file system",    // EROFS
	"no space left on device",  // ENOSPC
	"structure needs cleaning", // EUCLEAN
	"disk quota exceeded",      // EDQUOT
}

// storageEvictionNodeReasons are node event reasons the kubelet emits only
// for disk: image and container garbage collection failing to free space.
var storageEvictionNodeReasons = map[string]bool{
	"FreeDiskSpaceFailed": true,
	"ImageGCFailed":       true,
}

// storageResources are the words the kubelet uses for storage in eviction
// and reclaim messages ("The node was low on resource: ephemeral-storage",
// "Attempting to reclaim ephemeral-storage", "Usage of EmptyDir volume ...
// exceeds the limit", "Pod ephemeral local storage usage exceeds ...").
// EvictionThresholdMet is also emitted for memory and PIDs, which these
// rules ignore.
var storageResources = []string{
	"ephemeral-storage",
	"ephemeral local storage",
	"emptydir",
	"nodefs",
	"imagefs",
	"containerfs",
	"inodes",
}

// Finding is one rule firing.
type Finding struct {
	// RuleID identifies the rule.
	RuleID string `json:"ruleId"`
	// Subject is what the rule fired on: "node", "pod ns/name",
	// "container ns/pod/name" or "event ns/name".
	Subject string `json:"subject"`
	// Detail says which signal matched, such as the condition or the
	// signature. It never quotes log content.
	Detail string `json:"detail"`
}

// Evaluate runs every Kubernetes-side rule over a capture. Findings are in a
// stable order: by rule, then subject, then detail.
//
// Objects that do not decode are skipped rather than failing the sweep: a
// trigger is a cheap hint, and one malformed object must not hide the rest.
func Evaluate(kc schema.K8sCapture) []Finding {
	var out []Finding
	var node *corev1.Node
	if len(kc.NodeJSON) > 0 {
		var n corev1.Node
		if json.Unmarshal(kc.NodeJSON, &n) == nil {
			node = &n
		}
	}
	pods := decodePods(kc.Pods)
	events := decodeEvents(kc.Events)

	out = append(out, diskPressure(node)...)
	out = append(out, storageEvictions(pods, events)...)
	out = append(out, storageErrors(pods, events, kc.Logs)...)

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.RuleID != b.RuleID {
			return a.RuleID < b.RuleID
		}
		if a.Subject != b.Subject {
			return a.Subject < b.Subject
		}
		return a.Detail < b.Detail
	})
	return out
}

// RuleIDs returns the distinct rule IDs among findings, sorted.
func RuleIDs(findings []Finding) []string {
	seen := map[string]bool{}
	var ids []string
	for _, f := range findings {
		if !seen[f.RuleID] {
			seen[f.RuleID] = true
			ids = append(ids, f.RuleID)
		}
	}
	sort.Strings(ids)
	return ids
}

// diskPressure fires on the kubelet's DiskPressure condition.
//
// It reads only the Node object's conditions. The collector moves every
// condition the kubelet does not own — in practice node-problem-detector's —
// out of the Node object into K8sCapture.NPDConditions, so an NPD condition
// can never raise this rule, whatever it is called.
func diskPressure(node *corev1.Node) []Finding {
	if node == nil {
		return nil
	}
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeDiskPressure && c.Status == corev1.ConditionTrue {
			return []Finding{{RuleID: RuleDiskPressure, Subject: "node", Detail: "DiskPressure=True"}}
		}
	}
	return nil
}

// storageEvictions fires on pods evicted for storage, on the node's
// EvictionThresholdMet events for a storage resource, and on image or
// container garbage collection failing to free disk.
func storageEvictions(pods []corev1.Pod, events []corev1.Event) []Finding {
	var out []Finding
	for i := range pods {
		p := &pods[i]
		if p.Status.Reason == "Evicted" && mentionsStorage(p.Status.Message) {
			out = append(out, Finding{RuleID: RuleStorageEviction, Subject: "pod " + p.Namespace + "/" + p.Name, Detail: "evicted for storage"})
		}
	}
	for i := range events {
		e := &events[i]
		subject := "event " + e.Namespace + "/" + e.Name
		switch {
		case e.InvolvedObject.Kind == "Node" && storageEvictionNodeReasons[e.Reason]:
			out = append(out, Finding{RuleID: RuleStorageEviction, Subject: subject, Detail: e.Reason})
		case e.InvolvedObject.Kind == "Node" && e.Reason == "EvictionThresholdMet" && mentionsStorage(e.Message):
			out = append(out, Finding{RuleID: RuleStorageEviction, Subject: subject, Detail: e.Reason})
		case e.InvolvedObject.Kind == "Pod" && e.Reason == "Evicted" && mentionsStorage(e.Message):
			out = append(out, Finding{RuleID: RuleStorageEviction, Subject: subject, Detail: "Evicted"})
		}
	}
	return out
}

// storageErrors fires on a storage error signature in the captured log or
// termination message of a failing container, or in a Warning event for the
// node or its pods.
//
// Only failing containers count. A healthy container that logs "no space
// left on device" and carries on has handled the error; raising on it would
// raise every node whose applications log their retries.
func storageErrors(pods []corev1.Pod, events []corev1.Event, logs map[string]string) []Finding {
	var out []Finding
	failing := map[string]bool{}
	for i := range pods {
		p := &pods[i]
		statuses := append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...)
		for _, s := range statuses {
			if !Failing(s) {
				continue
			}
			key := p.Namespace + "/" + p.Name + "/" + s.Name
			failing[key] = true
			for _, t := range []*corev1.ContainerStateTerminated{s.State.Terminated, s.LastTerminationState.Terminated} {
				if t == nil {
					continue
				}
				if sig := signature(t.Message); sig != "" {
					out = append(out, Finding{RuleID: RuleStorageError, Subject: "container " + key, Detail: "termination message: " + sig})
				}
			}
		}
	}

	keys := make([]string, 0, len(logs))
	for k := range logs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		container := strings.TrimSuffix(k, "/previous")
		if !failing[container] {
			continue
		}
		if sig := signature(logs[k]); sig != "" {
			out = append(out, Finding{RuleID: RuleStorageError, Subject: "container " + container, Detail: "log: " + sig})
		}
	}

	for i := range events {
		e := &events[i]
		if e.Type != corev1.EventTypeWarning {
			continue
		}
		if e.InvolvedObject.Kind != "Node" && e.InvolvedObject.Kind != "Pod" {
			continue
		}
		if sig := signature(e.Message); sig != "" {
			out = append(out, Finding{RuleID: RuleStorageError, Subject: "event " + e.Namespace + "/" + e.Name, Detail: e.Reason + ": " + sig})
		}
	}
	return out
}

// Failing reports whether a container is failing: restarting, or terminated
// with a non-zero exit code.
//
// It is narrower than pkg/k8s.Unhealthy, which also reads the logs of
// containers that are merely not ready or still waiting to start. Both agree
// that a container which ran to completion with exit 0 is healthy. Every
// container Failing accepts is one Unhealthy accepts, so its logs are in
// the capture.
func Failing(s corev1.ContainerStatus) bool {
	if s.RestartCount > 0 {
		return true
	}
	if t := s.State.Terminated; t != nil && t.ExitCode != 0 {
		return true
	}
	return false
}

// signature returns the first storage error signature in text, or "".
func signature(text string) string {
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	for _, sig := range StorageErrorSignatures {
		if strings.Contains(lower, sig) {
			return sig
		}
	}
	return ""
}

func mentionsStorage(text string) bool {
	lower := strings.ToLower(text)
	for _, r := range storageResources {
		if strings.Contains(lower, r) {
			return true
		}
	}
	return false
}

func decodePods(raw []schema.RawJSON) []corev1.Pod {
	out := make([]corev1.Pod, 0, len(raw))
	for _, r := range raw {
		var p corev1.Pod
		if json.Unmarshal(r, &p) == nil {
			out = append(out, p)
		}
	}
	return out
}

func decodeEvents(raw []schema.RawJSON) []corev1.Event {
	out := make([]corev1.Event, 0, len(raw))
	for _, r := range raw {
		var e corev1.Event
		if json.Unmarshal(r, &e) == nil {
			out = append(out, e)
		}
	}
	return out
}
