package reason

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nathanwebb/tropis/pkg/schema"
)

var (
	capturedAt = time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)
	earlier    = capturedAt.Add(-24 * time.Hour)
)

func mustJSON(t *testing.T, v any) schema.RawJSON {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sample(t *testing.T, name string) schema.RawJSON {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "host", "smart", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeStripeKey is assembled from two halves so the source never contains a
// string shaped like a live Stripe key, which secret scanners rightly reject.
const fakeStripeKey = "sk_" + "live_planted00000000000000000000"

// Secrets planted in every place a real capture can carry text. None may
// reach model input.
var planted = []string{
	"hunter2-db-password",
	"AKIAIOSFODNN7EXAMPLE",
	"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0cm9waXMtdGVzdCJ9.c2lnbmF0dXJlLXZhbHVl",
	"ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789",
	"MIIEowIBAAKCAQEAplantedkeymaterial",
	"s3cr3t-in-url",
	"oncall@example.com",
	"10.20.30.40",
	"Z4Z1KQPL", // the failing sample's drive serial
	fakeStripeKey,
	"xoxb-000000000000-plantedslack",
	"annotation-secret-value",
	"label-token-value-123",
}

// plantedRequest builds a capture of a failing disk and a crash-looping
// database, with credentials planted throughout.
func plantedRequest(t *testing.T) Request {
	t.Helper()

	node := corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "worker-03",
			Labels:      map[string]string{"access_token": "label-token-value-123"},
			Annotations: map[string]string{"note": "contact oncall@example.com"},
		},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionTrue, Message: "kubelet is posting ready status from 10.20.30.40"},
			// An NPD condition in a hand-assembled fixture: must not appear.
			{Type: "ReadonlyFilesystem", Status: corev1.ConditionTrue, Reason: "FilesystemIsReadOnly"},
		}},
	}

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "db",
			Name:        "postgres-0",
			Annotations: map[string]string{"api_key": "annotation-secret-value"},
		},
		Spec: corev1.PodSpec{
			NodeName: "worker-03",
			Containers: []corev1.Container{{
				Name: "postgres",
				Env: []corev1.EnvVar{
					{Name: "POSTGRES_PASSWORD", Value: "hunter2-db-password"},
					{Name: "AWS_ACCESS_KEY_ID", Value: "AKIAIOSFODNN7EXAMPLE"},
				},
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
				},
			}},
			Volumes: []corev1.Volume{{
				Name:         "data",
				VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/mnt/disks/sdb/pg"}},
			}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "postgres",
				RestartCount: 14,
				State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			}},
		},
	}

	event := corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: "db", Name: "postgres-0.1", UID: "ev-1"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "db", Name: "postgres-0"},
		Type:           "Warning",
		Reason:         "BackOff",
		Message:        "Back-off restarting failed container; notified oncall@example.com with token=ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789",
		LastTimestamp:  metav1.NewTime(capturedAt.Add(-time.Minute)),
	}

	logs := strings.Join([]string{
		`2026-09-20T07:58:00Z LOG: connecting to postgres://replicator:s3cr3t-in-url@10.20.30.40:5432/app`,
		`2026-09-20T07:58:01Z LOG: auth header Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0cm9waXMtdGVzdCJ9.c2lnbmF0dXJlLXZhbHVl`,
		`2026-09-20T07:58:02Z LOG: loaded key -----BEGIN RSA PRIVATE KEY-----`,
		`MIIEowIBAAKCAQEAplantedkeymaterial`,
		`-----END RSA PRIVATE KEY-----`,
		`2026-09-20T07:58:03Z LOG: billing ` + fakeStripeKey + ` slack xoxb-000000000000-plantedslack`,
		`2026-09-20T07:58:04Z LOG: drive Z4Z1KQPL reported by operator`,
		`2026-09-20T07:58:11Z PANIC: could not fsync file "base/16384/2619": Input/output error`,
	}, "\n")

	return Request{
		Node: "worker-03",
		Host: schema.HostCapture{
			SMART:       map[string]schema.RawJSON{"/dev/sdb": sample(t, "sata-failing.json")},
			CollectedAt: capturedAt,
			Previous: &schema.HostSnapshot{
				SMART:       map[string]schema.RawJSON{"/dev/sdb": sample(t, "sata-failing.json")},
				CollectedAt: earlier,
			},
		},
		Kubernetes: schema.K8sCapture{
			NodeJSON:    mustJSON(t, node),
			Pods:        []schema.RawJSON{mustJSON(t, pod)},
			Events:      []schema.RawJSON{mustJSON(t, event)},
			Logs:        map[string]string{"db/postgres-0/postgres/previous": logs},
			CollectedAt: capturedAt,
		},
		TriggeredBy: []string{"smart.pending_sectors", "smart.health_failed"},
	}
}

// The non-negotiable test: a capture with planted credentials produces model
// input containing none of them — neither in the document nor in the full
// user message a backend sends.
func TestPlantedCredentialsNeverReachModelInput(t *testing.T) {
	in, err := BuildInput(plantedRequest(t))
	if err != nil {
		t.Fatalf("BuildInput: %v", err)
	}
	msg, err := UserMessage(in)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := in.Text()

	for _, secret := range planted {
		for name, s := range map[string]string{"document": string(text), "user message": msg} {
			if strings.Contains(s, secret) {
				t.Errorf("planted secret %q reached the %s", secret, name)
			}
		}
	}

	// And the diagnostic content survived redaction.
	for _, keep := range []string{"could not fsync file", "Input/output error", "CrashLoopBackOff", "/mnt/disks/sdb/pg", "Reallocated_Sector_Ct"} {
		if !strings.Contains(msg, keep) {
			t.Errorf("diagnostic text %q should survive redaction", keep)
		}
	}
	if in.Redactions()["secret"] == 0 && in.Redactions()["url_credentials"] == 0 {
		t.Errorf("redaction summary shows nothing redacted: %v", in.Redactions())
	}
}

// NPD output is never evidence, even if a hand-assembled fixture leaves an
// NPD condition on the node or in npdConditions.
func TestNPDNeverReachesModelInput(t *testing.T) {
	req := plantedRequest(t)
	req.Kubernetes.NPDConditions = []schema.RawJSON{mustJSON(t, corev1.NodeCondition{Type: "KernelDeadlock", Status: "True"})}

	in, err := BuildInput(req)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := in.Text()
	for _, npd := range []string{"ReadonlyFilesystem", "KernelDeadlock"} {
		if strings.Contains(string(text), npd) {
			t.Errorf("NPD condition %s reached model input", npd)
		}
	}
}

func TestBuildInputIsDeterministic(t *testing.T) {
	a, err := BuildInput(plantedRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildInput(plantedRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest() != b.Digest() {
		t.Errorf("same capture produced different digests: %s vs %s", a.Digest(), b.Digest())
	}
	if !strings.HasPrefix(a.Digest(), "sha256:") {
		t.Errorf("digest = %q", a.Digest())
	}
}

func TestBuildInputContent(t *testing.T) {
	in, err := BuildInput(plantedRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := in.Document()
	if err != nil {
		t.Fatal(err)
	}

	if len(doc.Host.Devices) != 1 {
		t.Fatalf("devices = %d", len(doc.Host.Devices))
	}
	dev := doc.Host.Devices[0]
	if dev.Previous == nil || *dev.Previous.ReallocatedSectors != 1544 {
		t.Errorf("previous reading should be attached: %+v", dev.Previous)
	}
	if doc.Host.PreviousCollectedAt == nil || !doc.Host.PreviousCollectedAt.Equal(earlier) {
		t.Errorf("previousCollectedAt = %v", doc.Host.PreviousCollectedAt)
	}

	if len(doc.Kubernetes.Pods) != 1 {
		t.Fatalf("pods = %d", len(doc.Kubernetes.Pods))
	}
	p := doc.Kubernetes.Pods[0]
	if p.Containers[0].RestartCount != 14 || !strings.Contains(p.Containers[0].State, "CrashLoopBackOff") {
		t.Errorf("container = %+v", p.Containers[0])
	}
	if p.Containers[0].Limits["memory"] != "2Gi" {
		t.Errorf("limits = %v", p.Containers[0].Limits)
	}
	if len(p.Volumes) != 1 || p.Volumes[0].Kind != "hostPath" {
		t.Errorf("volumes = %+v", p.Volumes)
	}

	if doc.Node != "worker-03" || in.Node() != "worker-03" {
		t.Errorf("node = %q / %q", doc.Node, in.Node())
	}
	if !in.ObservedAt().Equal(capturedAt) {
		t.Errorf("observedAt = %v", in.ObservedAt())
	}
	if got := in.TriggeredBy(); len(got) != 2 || got[0] != "smart.health_failed" {
		t.Errorf("triggeredBy should be sorted: %v", got)
	}

	// Env vars and annotations are not in the document at all.
	text, _ := in.Text()
	for _, absent := range []string{"POSTGRES_PASSWORD", "AWS_ACCESS_KEY_ID", "annotations", "serialNumber"} {
		if strings.Contains(string(text), absent) {
			t.Errorf("%q should not be in the document", absent)
		}
	}
}

// Nodes named by IP keep their real name on the verdict; only the model's
// view is redacted.
func TestNodeNamedByIP(t *testing.T) {
	req := plantedRequest(t)
	req.Node = "10.0.0.5"
	in, err := BuildInput(req)
	if err != nil {
		t.Fatal(err)
	}
	if in.Node() != "10.0.0.5" {
		t.Errorf("Node() = %q, want the real name", in.Node())
	}
	msg, _ := UserMessage(in)
	if strings.Contains(msg, "10.0.0.5") {
		t.Error("the model's view should carry the redacted node name")
	}
}

func TestUnbuiltInputIsRejected(t *testing.T) {
	var in AnalysisInput
	if _, err := UserMessage(&in); !errors.Is(err, ErrUnbuiltInput) {
		t.Errorf("UserMessage on a zero input: %v", err)
	}
	if _, err := in.Text(); !errors.Is(err, ErrUnbuiltInput) {
		t.Errorf("Text on a zero input: %v", err)
	}
	if _, err := Finalize([]byte(`{}`), &in, schema.BackendInfo{}); !errors.Is(err, ErrUnbuiltInput) {
		t.Errorf("Finalize on a zero input: %v", err)
	}
}

func TestBuildInputRequiresNode(t *testing.T) {
	if _, err := BuildInput(Request{}); err == nil {
		t.Error("expected an error for a request without a node")
	}
}

func TestPromptIsVersionedAndStable(t *testing.T) {
	p := SystemPrompt()
	if len(p) < 1000 {
		t.Fatalf("system prompt looks empty or truncated: %d bytes", len(p))
	}
	for _, must := range []string{"coincidental", "insufficient_evidence", "causal", "read-only"} {
		if !strings.Contains(p, must) {
			t.Errorf("system prompt should mention %q", must)
		}
	}
	// The system prompt is the cacheable prefix: it must not vary per call.
	if SystemPrompt() != p {
		t.Error("system prompt is not stable")
	}
	if PromptVersion == "" {
		t.Error("prompt version must be set")
	}
}
