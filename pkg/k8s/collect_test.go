package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/nathanwebb/tropis/pkg/schema"
)

var now = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

func node(extra ...corev1.NodeCondition) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-02",
			Annotations: map[string]string{
				corev1.LastAppliedConfigAnnotation: `{"huge":"blob"}`,
				"keep.me/annotation":               "yes",
			},
			ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "kubelet"}},
		},
		Status: corev1.NodeStatus{
			Conditions: append([]corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
				{Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse},
			}, extra...),
		},
	}
}

func pod(ns, name, nodeName string, statuses ...corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{{
				Name: "app",
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{corev1.ResourceMemory: resourceQty("512Mi")},
				},
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: statuses},
	}
}

func healthy(name string) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
}

func crashLooping(name string) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name:         name,
		RestartCount: 14,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
			Reason: "CrashLoopBackOff",
		}},
	}
}

func event(ns, name, kind, obj string, at time.Time, msg string) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: ns, Name: name},
		InvolvedObject: corev1.ObjectReference{Kind: kind, Namespace: ns, Name: obj},
		LastTimestamp:  metav1.NewTime(at),
		Message:        msg,
	}
}

func collector(objs ...runtime.Object) (*Collector, *fake.Clientset) {
	cs := fake.NewClientset(objs...)
	return &Collector{Client: cs, Now: func() time.Time { return now }}, cs
}

func TestCollect(t *testing.T) {
	c, _ := collector(
		node(),
		pod("db", "postgres-0", "worker-02", crashLooping("app")),
		pod("web", "frontend-1", "worker-02", healthy("app")),
		pod("web", "elsewhere", "worker-09", crashLooping("app")),
		event("db", "e1", "Pod", "postgres-0", now.Add(-time.Hour), "Back-off restarting failed container"),
		event("web", "e2", "Pod", "elsewhere", now.Add(-time.Hour), "not on this node"),
		event("default", "e3", "Node", "worker-02", now.Add(-2*time.Hour), "NodeHasDiskPressure"),
		event("db", "e4", "Pod", "postgres-0", now.Add(-48*time.Hour), "too old"),
	)

	capture, err := c.Collect(context.Background(), "worker-02")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if len(capture.Pods) != 2 {
		t.Errorf("captured %d pods, want the 2 on worker-02", len(capture.Pods))
	}
	for _, p := range capture.Pods {
		if strings.Contains(p.String(), "elsewhere") {
			t.Error("captured a pod from another node")
		}
	}
	if !strings.Contains(capture.Pods[0].String(), `"memory":"512Mi"`) {
		t.Errorf("resource limits should be captured: %s", capture.Pods[0])
	}

	var msgs []string
	for _, e := range capture.Events {
		var ev corev1.Event
		if err := json.Unmarshal(e, &ev); err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, ev.Message)
	}
	want := []string{"NodeHasDiskPressure", "Back-off restarting failed container"}
	if strings.Join(msgs, "|") != strings.Join(want, "|") {
		t.Errorf("events = %v, want %v (oldest first, this node only, within window)", msgs, want)
	}

	// Logs only for the unhealthy container, current and previous.
	if _, ok := capture.Logs["db/postgres-0/app"]; !ok {
		t.Errorf("missing current log for crash-looping container: %v", keys(capture.Logs))
	}
	if _, ok := capture.Logs["db/postgres-0/app/previous"]; !ok {
		t.Errorf("missing previous log for restarted container: %v", keys(capture.Logs))
	}
	if _, ok := capture.Logs["web/frontend-1/app"]; ok {
		t.Error("healthy container logs should not be collected")
	}

	if !capture.CollectedAt.Equal(now) {
		t.Errorf("collectedAt = %v", capture.CollectedAt)
	}
}

// Read-only, verified at the API call level rather than trusted to the RBAC
// manifest: every action the collector issues is a get or a list.
func TestCollectIsReadOnly(t *testing.T) {
	c, cs := collector(
		node(corev1.NodeCondition{Type: "KernelDeadlock", Status: corev1.ConditionFalse}),
		pod("db", "postgres-0", "worker-02", crashLooping("app")),
		event("db", "e1", "Pod", "postgres-0", now, "x"),
	)
	if _, err := c.Collect(context.Background(), "worker-02"); err != nil {
		t.Fatal(err)
	}
	if len(cs.Actions()) == 0 {
		t.Fatal("no API actions recorded")
	}
	for _, a := range cs.Actions() {
		if v := a.GetVerb(); v != "get" && v != "list" {
			t.Errorf("collector issued a %q on %s: it must be read-only", v, a.GetResource().Resource)
		}
	}
}

// NPD conditions are split off the node, so they can never reach the
// reasoning layer as evidence by riding along inside the node object.
func TestNPDConditionsAreSplitFromNode(t *testing.T) {
	c, _ := collector(node(
		corev1.NodeCondition{Type: "KernelDeadlock", Status: corev1.ConditionFalse, Reason: "KernelHasNoDeadlock"},
		corev1.NodeCondition{Type: "ReadonlyFilesystem", Status: corev1.ConditionTrue, Reason: "FilesystemIsReadOnly"},
	))

	capture, err := c.Collect(context.Background(), "worker-02")
	if err != nil {
		t.Fatal(err)
	}
	if len(capture.NPDConditions) != 2 {
		t.Fatalf("npdConditions = %d, want 2", len(capture.NPDConditions))
	}
	nodeJSON := capture.NodeJSON.String()
	for _, npd := range []string{"KernelDeadlock", "ReadonlyFilesystem"} {
		if strings.Contains(nodeJSON, npd) {
			t.Errorf("NPD condition %s leaked into nodeJson", npd)
		}
	}
	if !strings.Contains(nodeJSON, `"Ready"`) {
		t.Error("kubelet conditions must stay on the node")
	}
}

// NPD is optional. Without it, the capture is complete and carries no NPD
// conditions.
func TestCollectWithoutNPD(t *testing.T) {
	c, _ := collector(node(), pod("db", "postgres-0", "worker-02", healthy("app")))
	capture, err := c.Collect(context.Background(), "worker-02")
	if err != nil {
		t.Fatalf("Collect without NPD: %v", err)
	}
	if len(capture.NPDConditions) != 0 {
		t.Errorf("npdConditions = %v, want none", capture.NPDConditions)
	}
	if capture.NodeJSON.IsEmpty() || len(capture.Pods) != 1 {
		t.Error("capture without NPD should be otherwise complete")
	}
}

func TestCollectStripsNoise(t *testing.T) {
	c, _ := collector(node())
	capture, err := c.Collect(context.Background(), "worker-02")
	if err != nil {
		t.Fatal(err)
	}
	s := capture.NodeJSON.String()
	if strings.Contains(s, "managedFields") {
		t.Error("managedFields should be stripped")
	}
	if strings.Contains(s, corev1.LastAppliedConfigAnnotation) {
		t.Error("last-applied-configuration should be stripped")
	}
	if !strings.Contains(s, "keep.me/annotation") {
		t.Error("other annotations must be kept")
	}
}

func TestCollectNodeNotFound(t *testing.T) {
	c, _ := collector()
	if _, err := c.Collect(context.Background(), "ghost"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("want a not-found error, got %v", err)
	}
}

func TestUnhealthy(t *testing.T) {
	tests := []struct {
		name string
		s    corev1.ContainerStatus
		want bool
	}{
		{"running and ready", healthy("a"), false},
		{"crash looping", crashLooping("a"), true},
		{"not ready", corev1.ContainerStatus{Ready: false, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}, true},
		{"restarted once, now ready", corev1.ContainerStatus{Ready: true, RestartCount: 1}, true},
		{"completed successfully", corev1.ContainerStatus{Ready: true, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}, false},
		{"completed, not ready (every finished Job)", corev1.ContainerStatus{Ready: false, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed", ExitCode: 0}}}, false},
		{"completed after restarts", corev1.ContainerStatus{Ready: false, RestartCount: 3, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}, true},
		{"exited non-zero", corev1.ContainerStatus{Ready: true, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137}}}, true},
	}
	for _, tt := range tests {
		if got := Unhealthy(tt.s); got != tt.want {
			t.Errorf("%s: Unhealthy = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func collectorPod(name, node string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tropis-system", Name: name,
			Labels: map[string]string{"app.kubernetes.io/name": "tropis", "app.kubernetes.io/component": "collector"}},
		Spec:   corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func TestHostFetcher(t *testing.T) {
	var asked string
	f := &HostFetcher{
		Client:    fake.NewClientset(collectorPod("collector-abc", "worker-02", corev1.PodRunning), collectorPod("collector-xyz", "worker-03", corev1.PodRunning)),
		Namespace: "tropis-system",
		get: func(_ context.Context, ns, pod string, port int, path string) ([]byte, error) {
			asked = ns + "/" + pod + ":" + fmt.Sprint(port) + path
			return json.Marshal(schema.HostCapture{
				SMART:       map[string]schema.RawJSON{"/dev/sda": schema.RawJSON(`{"device":{"name":"/dev/sda"}}`)},
				CollectedAt: now,
			})
		},
	}
	hc, err := f.Fetch(context.Background(), "worker-02")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if asked != "tropis-system/collector-abc:9476/v1/smart" {
		t.Errorf("fetched %q", asked)
	}
	if _, ok := hc.SMART["/dev/sda"]; !ok {
		t.Errorf("capture = %+v", hc)
	}
}

func TestCollectorPod(t *testing.T) {
	f := &HostFetcher{
		Client: fake.NewClientset(
			collectorPod("pending", "worker-02", corev1.PodPending),
			collectorPod("running", "worker-02", corev1.PodRunning),
		),
		Namespace: "tropis-system",
	}
	name, err := f.CollectorPod(context.Background(), "worker-02")
	if err != nil || name != "running" {
		t.Errorf("CollectorPod = %q, %v", name, err)
	}
	if _, err := f.CollectorPod(context.Background(), "worker-99"); err == nil {
		t.Error("expected an error when no collector runs on the node")
	}
}

func TestNPDConditionsAndNodes(t *testing.T) {
	c, _ := collector(node(corev1.NodeCondition{Type: "ReadonlyFilesystem", Status: corev1.ConditionTrue}),
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "a-node"}})
	conds, err := c.NPDConditions(context.Background(), "worker-02")
	if err != nil || len(conds) != 1 || !strings.Contains(conds[0].String(), "ReadonlyFilesystem") {
		t.Errorf("NPDConditions = %v, %v", conds, err)
	}
	names, err := c.Nodes(context.Background())
	if err != nil || strings.Join(names, ",") != "a-node,worker-02" {
		t.Errorf("Nodes = %v, %v", names, err)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func resourceQty(s string) resource.Quantity { return resource.MustParse(s) }
