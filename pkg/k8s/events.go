package k8s

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/00Webbo/tropis/pkg/notify"
)

// EventNamespace is where events about Nodes live. Nodes are cluster-scoped,
// and Kubernetes records events about cluster-scoped objects in "default";
// it is where `kubectl describe node` looks.
const EventNamespace = metav1.NamespaceDefault

// EventNotifier records a verdict change as a Kubernetes Event on the Node.
//
// This is the one write Tropis can make beyond its own NodeHealthReports, and
// it is opt-in: the chart grants create on events, in the default namespace
// only, solely when events are enabled. An Event changes nothing about the
// node or its workloads — it is Kubernetes' own channel for reporting what a
// component saw — and it expires on the API server's event TTL.
type EventNotifier struct {
	Client kubernetes.Interface
	// Instance identifies the reporting process, e.g. the sweep pod's name.
	Instance string
	Now      func() time.Time
}

// Event reasons.
const (
	ReasonCausal  = "TropisCausalVerdict"
	ReasonCleared = "TropisVerdictCleared"
	ReasonChanged = "TropisVerdictChanged"
)

// Name identifies the notifier.
func (e *EventNotifier) Name() string { return "kubernetes-events" }

// Notify creates the event.
func (e *EventNotifier) Notify(ctx context.Context, c notify.Change) error {
	now := time.Now
	if e.Now != nil {
		now = e.Now
	}
	instance := e.Instance
	if instance == "" {
		instance, _ = os.Hostname()
	}

	reason, typ := ReasonChanged, corev1.EventTypeNormal
	switch {
	case c.Kind == notify.KindCleared:
		reason = ReasonCleared
	case c.Current.Relationship == "causal":
		reason, typ = ReasonCausal, corev1.EventTypeWarning
	}

	note := notify.Headline(c)
	if rc := c.Current.RootCause; rc != nil {
		note += ". " + rc.Description
	}
	note += ". Tropis has taken no action; see the NodeHealthReport."
	// The API server rejects notes over 1 KiB.
	if len(note) > 1024 {
		note = note[:1021] + "..."
	}

	ev := &eventsv1.Event{
		ObjectMeta: metav1.ObjectMeta{
			// Named here rather than through generateName, so the name is the
			// same however the API is served.
			Name:      fmt.Sprintf("%s.tropis.%x", strings.ToLower(c.Node), now().UnixNano()),
			Namespace: EventNamespace,
		},
		EventTime:           metav1.NewMicroTime(now()),
		ReportingController: "tropis.io/sweep",
		ReportingInstance:   instance,
		Action:              "Diagnose",
		Reason:              reason,
		Type:                typ,
		Note:                note,
		Regarding: corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Node",
			Name:       c.Node,
		},
	}
	if _, err := e.Client.EventsV1().Events(EventNamespace).Create(ctx, ev, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create event for node %s: %w", c.Node, err)
	}
	return nil
}
