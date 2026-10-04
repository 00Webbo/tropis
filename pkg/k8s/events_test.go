package k8s

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/00Webbo/tropis/pkg/notify"
	tropis "github.com/00Webbo/tropis/pkg/schema"
)

func TestEventNotifier(t *testing.T) {
	cs := fake.NewClientset()
	tick := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	e := &EventNotifier{Client: cs, Instance: "sweep-abc", Now: func() time.Time { tick = tick.Add(time.Second); return tick }}

	prev := sampleVerdict(tropis.RelationshipCausal)
	causal := notify.PolicyCausal.Evaluate(nil, prev)
	cleared := notify.PolicyCausal.Evaluate(&prev, sampleVerdict(tropis.RelationshipCoincidental))

	for _, c := range []*notify.Change{causal, cleared} {
		if err := e.Notify(context.Background(), *c); err != nil {
			t.Fatal(err)
		}
	}
	// The notifier makes exactly one kind of write: creating events. Checked
	// before the test lists them, which the fake would also record.
	for _, a := range cs.Actions() {
		if a.GetVerb() != "create" || a.GetResource().Resource != "events" {
			t.Errorf("unexpected API action %s %s", a.GetVerb(), a.GetResource().Resource)
		}
	}

	list, err := cs.EventsV1().Events(EventNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("events = %d", len(list.Items))
	}
	byReason := map[string]string{}
	for _, ev := range list.Items {
		if ev.Regarding.Kind != "Node" || ev.Regarding.Name != "worker-02" {
			t.Errorf("event regards %+v, want the node", ev.Regarding)
		}
		if !strings.Contains(ev.Note, "Tropis has taken no action") {
			t.Errorf("note = %q", ev.Note)
		}
		byReason[ev.Reason] = ev.Type
	}
	if byReason[ReasonCausal] != corev1.EventTypeWarning || byReason[ReasonCleared] != corev1.EventTypeNormal {
		t.Errorf("events = %v", byReason)
	}
}
