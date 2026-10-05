package triggers

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/00Webbo/tropis/pkg/schema"
)

func raw(t *testing.T, v any) schema.RawJSON {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func nodeWith(t *testing.T, conds ...corev1.NodeCondition) schema.RawJSON {
	return raw(t, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-01"}, Status: corev1.NodeStatus{Conditions: conds}})
}

func podWith(t *testing.T, status corev1.PodStatus, containers ...corev1.ContainerStatus) schema.RawJSON {
	status.ContainerStatuses = containers
	return raw(t, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "web-0"}, Status: status})
}

func event(t *testing.T, kind, typ, reason, message string) schema.RawJSON {
	name := "web-0"
	ns := "app"
	if kind == "Node" {
		name, ns = "worker-01", "default"
	}
	return raw(t, &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: ns, Name: name + ".1"},
		InvolvedObject: corev1.ObjectReference{Kind: kind, Name: name, Namespace: ns},
		Type:           typ,
		Reason:         reason,
		Message:        message,
	})
}

// Container states.
var (
	crashLooping = corev1.ContainerStatus{Name: "app", RestartCount: 5,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}
	terminatedNonZero = corev1.ContainerStatus{Name: "app",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error"}}}
	running = corev1.ContainerStatus{Name: "app", Ready: true,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
	completed = corev1.ContainerStatus{Name: "app",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}}
	notReady = corev1.ContainerStatus{Name: "app", Ready: false,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
)

func withTermination(s corev1.ContainerStatus, exit int32, reason, msg string) corev1.ContainerStatus {
	s.LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: exit, Reason: reason, Message: msg}}
	return s
}

func ids(kc schema.K8sCapture) []string { return RuleIDs(Evaluate(kc)) }

func TestRules(t *testing.T) {
	cases := []struct {
		name string
		kc   func(t *testing.T) schema.K8sCapture
		want []string
	}{
		{"empty capture", func(*testing.T) schema.K8sCapture { return schema.K8sCapture{} }, nil},

		// k8s.disk_pressure
		{"kubelet DiskPressure true", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{NodeJSON: nodeWith(t, corev1.NodeCondition{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue})}
		}, []string{RuleDiskPressure}},
		{"kubelet DiskPressure false", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{NodeJSON: nodeWith(t, corev1.NodeCondition{Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse})}
		}, nil},
		{"other kubelet pressure", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{NodeJSON: nodeWith(t, corev1.NodeCondition{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue})}
		}, nil},
		// NPD conditions are triggers in their own right (npd.*) and never
		// stand in for the kubelet's condition.
		{"DiskPressure from an NPD condition", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{
				NodeJSON:      nodeWith(t),
				NPDConditions: []schema.RawJSON{raw(t, corev1.NodeCondition{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue})},
			}
		}, nil},
		{"NPD condition carrying a storage message", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{
				NPDConditions: []schema.RawJSON{raw(t, corev1.NodeCondition{Type: "ReadonlyFilesystem", Status: corev1.ConditionTrue, Message: "Read-only file system"})},
			}
		}, nil},

		// k8s.storage_eviction
		{"pod evicted for ephemeral storage", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{Pods: []schema.RawJSON{podWith(t, corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted",
				Message: "The node was low on resource: ephemeral-storage. Threshold quantity: 10%, available: 0."})}}
		}, []string{RuleStorageEviction}},
		{"pod evicted for an emptyDir limit", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{Pods: []schema.RawJSON{podWith(t, corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted",
				Message: `Usage of EmptyDir volume "scratch" exceeds the limit "1Gi".`})}}
		}, []string{RuleStorageEviction}},
		{"pod evicted for memory", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{Pods: []schema.RawJSON{podWith(t, corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted",
				Message: "The node was low on resource: memory."})}}
		}, nil},
		{"Evicted pod event for ephemeral storage", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{Events: []schema.RawJSON{event(t, "Pod", "Warning", "Evicted", "The node was low on resource: ephemeral-storage.")}}
		}, []string{RuleStorageEviction}},
		{"EvictionThresholdMet for storage", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{Events: []schema.RawJSON{event(t, "Node", "Warning", "EvictionThresholdMet", "Attempting to reclaim ephemeral-storage")}}
		}, []string{RuleStorageEviction}},
		{"EvictionThresholdMet for nodefs inodes", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{Events: []schema.RawJSON{event(t, "Node", "Warning", "EvictionThresholdMet", "Attempting to reclaim inodes")}}
		}, []string{RuleStorageEviction}},
		{"EvictionThresholdMet for memory", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{Events: []schema.RawJSON{event(t, "Node", "Warning", "EvictionThresholdMet", "Attempting to reclaim memory")}}
		}, nil},
		{"FreeDiskSpaceFailed", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{Events: []schema.RawJSON{event(t, "Node", "Warning", "FreeDiskSpaceFailed", "failed to garbage collect required amount of images")}}
		}, []string{RuleStorageEviction}},
		{"ImageGCFailed", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{Events: []schema.RawJSON{event(t, "Node", "Warning", "ImageGCFailed", "wanted to free 1Gi bytes, but freed 0 bytes")}}
		}, []string{RuleStorageEviction}},

		// Deliberately not triggers: failing workloads without a storage
		// signature.
		{"crash loop without a storage error", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{
				Pods: []schema.RawJSON{podWith(t, corev1.PodStatus{}, withTermination(crashLooping, 1, "Error", "panic: invalid configuration"))},
				Logs: map[string]string{"app/web-0/app/previous": "panic: invalid configuration key \"listen\""},
				Events: []schema.RawJSON{
					event(t, "Pod", "Warning", "BackOff", "Back-off restarting failed container app in pod web-0"),
				},
			}
		}, nil},
		{"OOMKilled", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{Pods: []schema.RawJSON{podWith(t, corev1.PodStatus{}, withTermination(crashLooping, 137, "OOMKilled", ""))}}
		}, nil},
		{"exit 137 with no reason", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{Pods: []schema.RawJSON{podWith(t, corev1.PodStatus{}, withTermination(crashLooping, 137, "Error", ""))}}
		}, nil},
		{"probe timeouts", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{
				Pods: []schema.RawJSON{podWith(t, corev1.PodStatus{}, withTermination(crashLooping, 137, "Error", ""))},
				Events: []schema.RawJSON{
					event(t, "Pod", "Warning", "Unhealthy", "Liveness probe failed: command timed out after 3s"),
					event(t, "Pod", "Warning", "Unhealthy", "Readiness probe failed: context deadline exceeded"),
				},
			}
		}, nil},
		{"image pull failure", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{Events: []schema.RawJSON{event(t, "Pod", "Warning", "Failed", "Failed to pull image \"web:1.2\": not found")}}
		}, nil},

		// Healthy containers never raise, whatever they log.
		{"storage error in a healthy container's log", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{
				Pods: []schema.RawJSON{podWith(t, corev1.PodStatus{}, running)},
				Logs: map[string]string{"app/web-0/app": "write /data/x: no space left on device; retrying"},
			}
		}, nil},
		{"storage error from a container that completed", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{
				Pods: []schema.RawJSON{podWith(t, corev1.PodStatus{}, completed)},
				Logs: map[string]string{"app/web-0/app": "warning: Input/output error on cache, skipped"},
			}
		}, nil},
		{"storage error from a container that is only not ready", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{
				Pods: []schema.RawJSON{podWith(t, corev1.PodStatus{}, notReady)},
				Logs: map[string]string{"app/web-0/app": "open /data: read-only file system"},
			}
		}, nil},
		{"storage error in a log with no matching pod", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{Logs: map[string]string{"app/other-0/app": "Input/output error"}}
		}, nil},
		{"storage error in a Normal event", func(t *testing.T) schema.K8sCapture {
			return schema.K8sCapture{Events: []schema.RawJSON{event(t, "Pod", "Normal", "Pulled", "recovered from read-only file system")}}
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ids(tc.kc(t)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("rules = %v, want %v", got, tc.want)
			}
		})
	}
}

// Every signature raises k8s.storage_error in each place it can appear, in
// any case.
func TestStorageErrorSignatures(t *testing.T) {
	type place struct {
		name string
		kc   func(t *testing.T, text string) schema.K8sCapture
	}
	places := []place{
		{"current log of a crash-looping container", func(t *testing.T, text string) schema.K8sCapture {
			return schema.K8sCapture{
				Pods: []schema.RawJSON{podWith(t, corev1.PodStatus{}, crashLooping)},
				Logs: map[string]string{"app/web-0/app": "2026-10-05T10:00:00Z starting\n2026-10-05T10:00:01Z FATAL: " + text + "\n"},
			}
		}},
		{"previous log of a crash-looping container", func(t *testing.T, text string) schema.K8sCapture {
			return schema.K8sCapture{
				Pods: []schema.RawJSON{podWith(t, corev1.PodStatus{}, crashLooping)},
				Logs: map[string]string{"app/web-0/app/previous": "error: " + text},
			}
		}},
		{"log of a container terminated non-zero", func(t *testing.T, text string) schema.K8sCapture {
			return schema.K8sCapture{
				Pods: []schema.RawJSON{podWith(t, corev1.PodStatus{}, terminatedNonZero)},
				Logs: map[string]string{"app/web-0/app": text},
			}
		}},
		{"last termination message", func(t *testing.T, text string) schema.K8sCapture {
			return schema.K8sCapture{Pods: []schema.RawJSON{podWith(t, corev1.PodStatus{}, withTermination(crashLooping, 1, "Error", "open /var/lib/data: "+text))}}
		}},
		{"current termination message", func(t *testing.T, text string) schema.K8sCapture {
			s := terminatedNonZero
			s.State.Terminated = &corev1.ContainerStateTerminated{ExitCode: 2, Reason: "Error", Message: text}
			return schema.K8sCapture{Pods: []schema.RawJSON{podWith(t, corev1.PodStatus{}, s)}}
		}},
		{"Warning event for a pod", func(t *testing.T, text string) schema.K8sCapture {
			return schema.K8sCapture{Events: []schema.RawJSON{event(t, "Pod", "Warning", "FailedMount", "MountVolume.SetUp failed: "+text)}}
		}},
		{"Warning event for the node", func(t *testing.T, text string) schema.K8sCapture {
			return schema.K8sCapture{Events: []schema.RawJSON{event(t, "Node", "Warning", "ContainerGCFailed", "rpc error: "+text)}}
		}},
	}
	for _, sig := range StorageErrorSignatures {
		for _, variant := range []string{sig, strings.ToUpper(sig), strings.ToUpper(sig[:1]) + sig[1:]} {
			for _, p := range places {
				t.Run(p.name+"/"+variant, func(t *testing.T) {
					got := ids(p.kc(t, variant))
					if !reflect.DeepEqual(got, []string{RuleStorageError}) {
						t.Errorf("rules = %v, want [%s]", got, RuleStorageError)
					}
				})
			}
		}
	}
}

// The signatures are the libc strerror texts for these errnos and nothing
// else. Changing them changes results; this test makes that deliberate.
func TestSignaturesAreLibcStrerror(t *testing.T) {
	want := []string{
		"input/output error",       // EIO
		"read-only file system",    // EROFS
		"no space left on device",  // ENOSPC
		"structure needs cleaning", // EUCLEAN
		"disk quota exceeded",      // EDQUOT
	}
	if !reflect.DeepEqual(StorageErrorSignatures, want) {
		t.Errorf("signatures = %q, want %q", StorageErrorSignatures, want)
	}
}

func TestMalformedObjectsAreSkipped(t *testing.T) {
	kc := schema.K8sCapture{
		NodeJSON: schema.RawJSON(`{not json`),
		Pods:     []schema.RawJSON{schema.RawJSON(`[]`), podWith(t, corev1.PodStatus{}, crashLooping)},
		Events:   []schema.RawJSON{schema.RawJSON(`"x"`)},
		Logs:     map[string]string{"app/web-0/app": "Input/output error"},
	}
	if got := ids(kc); !reflect.DeepEqual(got, []string{RuleStorageError}) {
		t.Errorf("rules = %v", got)
	}
}

func TestFindingsAreStableAndDescribeWithoutQuoting(t *testing.T) {
	secret := "password=hunter2 Input/output error"
	kc := schema.K8sCapture{
		NodeJSON: nodeWith(t, corev1.NodeCondition{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue}),
		Pods:     []schema.RawJSON{podWith(t, corev1.PodStatus{}, crashLooping)},
		Logs:     map[string]string{"app/web-0/app": secret, "app/web-0/app/previous": secret},
	}
	first := Evaluate(kc)
	for i := 0; i < 5; i++ {
		if again := Evaluate(kc); !reflect.DeepEqual(first, again) {
			t.Fatalf("findings not stable: %v vs %v", first, again)
		}
	}
	for _, f := range first {
		if strings.Contains(f.Detail, "hunter2") || strings.Contains(f.Subject, "hunter2") {
			t.Errorf("finding quotes log content: %+v", f)
		}
	}
	if got := RuleIDs(first); !reflect.DeepEqual(got, []string{RuleDiskPressure, RuleStorageError}) {
		t.Errorf("rule IDs = %v", got)
	}
}

func TestFailing(t *testing.T) {
	cases := []struct {
		name string
		s    corev1.ContainerStatus
		want bool
	}{
		{"crash looping", crashLooping, true},
		{"terminated non-zero", terminatedNonZero, true},
		{"running and ready", running, false},
		{"completed with exit 0", completed, false},
		{"running but not ready", notReady, false},
		{"waiting to start", corev1.ContainerStatus{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}}, false},
	}
	for _, tc := range cases {
		if got := Failing(tc.s); got != tc.want {
			t.Errorf("%s: Failing = %v, want %v", tc.name, got, tc.want)
		}
	}
}
