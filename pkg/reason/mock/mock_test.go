package mock

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nathanwebb/tropis/pkg/reason"
	"github.com/nathanwebb/tropis/pkg/schema"
)

var at = time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)

// sata builds minimal smartctl output for a SATA disk.
func sata(path string, passed bool, realloc, pending uint64) schema.RawJSON {
	return schema.RawJSON(fmt.Sprintf(`{"device":{"name":%q,"type":"sat","protocol":"ATA"},"serial_number":"SN1",
	 "smart_status":{"passed":%v},"ata_smart_attributes":{"table":[
	  {"id":5,"name":"Reallocated_Sector_Ct","raw":{"value":%d,"string":"%d"}},
	  {"id":197,"name":"Current_Pending_Sector","raw":{"value":%d,"string":"%d"}}]}}`,
		path, passed, realloc, realloc, pending, pending))
}

func pod(ns, name, state string, restarts int, volume string) schema.RawJSON {
	return schema.RawJSON(fmt.Sprintf(`{"metadata":{"namespace":%q,"name":%q},
	 "spec":{"containers":[{"name":"app"}],"volumes":[{"name":"v",%s}]},
	 "status":{"phase":"Running","containerStatuses":[{"name":"app","ready":false,"restartCount":%d,"state":{"waiting":{"reason":%q}}}]}}`,
		ns, name, volume, restarts, state))
}

func analyze(t *testing.T, req reason.Request) schema.Verdict {
	t.Helper()
	req.Node = "worker-1"
	req.Host.CollectedAt, req.Kubernetes.CollectedAt = at, at
	in, err := reason.BuildInput(req)
	if err != nil {
		t.Fatal(err)
	}
	v, err := New().Analyze(context.Background(), in)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	return v
}

func TestMockCausalHost(t *testing.T) {
	v := analyze(t, reason.Request{
		Host: schema.HostCapture{SMART: map[string]schema.RawJSON{"/dev/sdb": sata("/dev/sdb", true, 20, 24)}},
		Kubernetes: schema.K8sCapture{
			Pods: []schema.RawJSON{pod("db", "pg-0", "CrashLoopBackOff", 5, `"hostPath":{"path":"/mnt/sdb"}`)},
			Logs: map[string]string{"db/pg-0/app/previous": "starting\nPANIC: could not fsync file: Input/output error"},
		},
	})
	if v.Relationship != schema.RelationshipCausal || v.RootCause.Layer != schema.LayerHost {
		t.Errorf("verdict = %+v", v)
	}
}

// The canonical negative control: an old, stable defect beside an unrelated
// crash.
func TestMockCoincidental(t *testing.T) {
	v := analyze(t, reason.Request{
		Host: schema.HostCapture{
			SMART:    map[string]schema.RawJSON{"/dev/sda": sata("/dev/sda", true, 112, 0)},
			Previous: &schema.HostSnapshot{SMART: map[string]schema.RawJSON{"/dev/sda": sata("/dev/sda", true, 112, 0)}, CollectedAt: at.Add(-24 * time.Hour)},
		},
		Kubernetes: schema.K8sCapture{
			Pods: []schema.RawJSON{pod("shop", "checkout", "CrashLoopBackOff", 9, `"configMap":{"name":"c"}`)},
			Logs: map[string]string{"shop/checkout/app/previous": "panic: required env PAYMENTS_URL is not set"},
		},
	})
	if v.Relationship != schema.RelationshipCoincidental || v.RootCause != nil {
		t.Errorf("verdict = %+v", v)
	}
}

// The same defect count, but growing, is no longer benign.
func TestMockGrowthIsActive(t *testing.T) {
	v := analyze(t, reason.Request{
		Host: schema.HostCapture{
			SMART:    map[string]schema.RawJSON{"/dev/sda": sata("/dev/sda", true, 112, 0)},
			Previous: &schema.HostSnapshot{SMART: map[string]schema.RawJSON{"/dev/sda": sata("/dev/sda", true, 40, 0)}, CollectedAt: at.Add(-24 * time.Hour)},
		},
		Kubernetes: schema.K8sCapture{
			Pods: []schema.RawJSON{pod("db", "pg-0", "CrashLoopBackOff", 3, `"hostPath":{"path":"/data"}`)},
			Logs: map[string]string{"db/pg-0/app": "ERROR: read failed: Input/output error"},
		},
	})
	if v.Relationship != schema.RelationshipCausal {
		t.Errorf("verdict = %+v", v)
	}
}

func TestMockCausalKubernetes(t *testing.T) {
	v := analyze(t, reason.Request{
		Host: schema.HostCapture{SMART: map[string]schema.RawJSON{"/dev/sda": sata("/dev/sda", true, 0, 0)}},
		Kubernetes: schema.K8sCapture{
			Events: []schema.RawJSON{schema.RawJSON(`{"metadata":{"uid":"e1","namespace":"web","name":"x"},
			 "involvedObject":{"kind":"Pod","namespace":"web","name":"frontend-2"},"type":"Warning","reason":"Evicted",
			 "message":"The node was low on resource: ephemeral-storage."}`)},
		},
	})
	if v.Relationship != schema.RelationshipCausal || v.RootCause.Layer != schema.LayerKubernetes {
		t.Errorf("verdict = %+v", v)
	}
}

func TestMockInsufficientEvidence(t *testing.T) {
	v := analyze(t, reason.Request{
		Host: schema.HostCapture{SMART: map[string]schema.RawJSON{"/dev/sda": sata("/dev/sda", true, 0, 0)}},
	})
	if v.Relationship != schema.RelationshipInsufficientEvidence || len(v.Evidence) == 0 {
		t.Errorf("verdict = %+v", v)
	}
}

// The T7 acceptance criterion: a mock backend satisfies the interface.
var _ reason.Backend = (*Backend)(nil)

// Regression: the kubelet's routine "NodeHasNoDiskPressure" event is not
// evidence of exhaustion. Found on the first kind install.
func TestMockIgnoresNoDiskPressureEvent(t *testing.T) {
	v := analyze(t, reason.Request{
		Host: schema.HostCapture{SMART: map[string]schema.RawJSON{"/dev/sda": sata("/dev/sda", true, 0, 0)}},
		Kubernetes: schema.K8sCapture{
			Events: []schema.RawJSON{schema.RawJSON(`{"metadata":{"uid":"e1","namespace":"default","name":"n"},
			 "involvedObject":{"kind":"Node","name":"worker-1"},"type":"Normal","reason":"NodeHasNoDiskPressure",
			 "message":"Node worker-1 status is now: NodeHasNoDiskPressure"}`)},
		},
	})
	if v.Relationship == schema.RelationshipCausal {
		t.Errorf("a routine NodeHasNoDiskPressure event produced a causal verdict: %+v", v)
	}
}

// Regression: a successfully completed Job is not a workload symptom. Found
// on the kind install, where Tropis's own finished Jobs were cited.
func TestMockIgnoresCompletedJobs(t *testing.T) {
	done := schema.RawJSON(`{"metadata":{"namespace":"tropis-system","name":"sweep-x"},"spec":{"containers":[{"name":"tropis"}]},
	 "status":{"phase":"Succeeded","containerStatuses":[{"name":"tropis","ready":false,"restartCount":0,
	 "state":{"terminated":{"reason":"Completed","exitCode":0}}}]}}`)
	v := analyze(t, reason.Request{
		Host:       schema.HostCapture{SMART: map[string]schema.RawJSON{"/dev/sda": sata("/dev/sda", true, 112, 0)}},
		Kubernetes: schema.K8sCapture{Pods: []schema.RawJSON{done}},
	})
	for _, e := range v.Evidence {
		if e.Ref == "pod:tropis-system/sweep-x" {
			t.Errorf("a completed Job was cited as a symptom: %+v", v)
		}
	}
	if v.Relationship == schema.RelationshipCoincidental {
		t.Errorf("a completed Job made the verdict coincidental: %+v", v)
	}
}
