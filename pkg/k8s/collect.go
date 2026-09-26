// Package k8s collects the Kubernetes-layer state of one node: the node
// object, the pods scheduled to it, their events and — for containers that
// are unhealthy — their logs.
//
// It is read-only. Every call it makes is a get or a list; the RBAC it ships
// with grants nothing else on these resources.
//
// Output is schema.K8sCapture: raw API JSON, the same shape a fixture stores,
// so live analysis and fixture replay go through one path.
package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"

	"github.com/nathanwebb/tropis/pkg/schema"
)

// Defaults bounding how much log is read per container.
const (
	DefaultLogTailLines  = 200
	DefaultLogLimitBytes = 64 * 1024
	DefaultEventWindow   = 6 * time.Hour
)

// kubeletConditions are the node condition types the kubelet itself sets.
// Anything else on a node is set by an external agent — in practice
// node-problem-detector — and is split out of the node object at capture.
var kubeletConditions = map[corev1.NodeConditionType]bool{
	corev1.NodeReady:              true,
	corev1.NodeMemoryPressure:     true,
	corev1.NodeDiskPressure:       true,
	corev1.NodePIDPressure:        true,
	corev1.NodeNetworkUnavailable: true,
}

// Collector reads the Kubernetes-layer state for a node.
type Collector struct {
	Client kubernetes.Interface

	// LogTailLines and LogLimitBytes bound each log read. Zero uses defaults.
	LogTailLines  int64
	LogLimitBytes int64

	// EventWindow limits events to those seen within this window before
	// collection. Zero uses DefaultEventWindow.
	EventWindow time.Duration

	// Now is the clock; nil uses time.Now.
	Now func() time.Time
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Collect captures the Kubernetes-layer state of the named node.
//
// Missing pieces degrade rather than fail: a pod whose logs cannot be read
// still contributes its status and events. Only failing to read the node or
// its pods is an error, since without them there is nothing to analyse.
func (c *Collector) Collect(ctx context.Context, nodeName string) (*schema.K8sCapture, error) {
	node, err := c.Client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("node %q not found", nodeName)
		}
		return nil, fmt.Errorf("get node %q: %w", nodeName, err)
	}

	podList, err := c.Client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("spec.nodeName", nodeName).String(),
	})
	if err != nil {
		return nil, fmt.Errorf("list pods on node %q: %w", nodeName, err)
	}
	// The field selector filters server-side; filtering again here is cheap and
	// means a pod from another node can never enter the capture, whatever sits
	// between us and the API server.
	var pods []corev1.Pod
	for _, p := range podList.Items {
		if p.Spec.NodeName == nodeName {
			pods = append(pods, p)
		}
	}
	sort.Slice(pods, func(i, j int) bool {
		if pods[i].Namespace != pods[j].Namespace {
			return pods[i].Namespace < pods[j].Namespace
		}
		return pods[i].Name < pods[j].Name
	})

	capture := &schema.K8sCapture{CollectedAt: c.now().UTC()}

	kubelet, external := splitConditions(node.Status.Conditions)
	node = node.DeepCopy()
	node.Status.Conditions = kubelet
	if capture.NodeJSON, err = marshalClean(node); err != nil {
		return nil, err
	}
	for i := range external {
		raw, err := json.Marshal(external[i])
		if err != nil {
			return nil, err
		}
		capture.NPDConditions = append(capture.NPDConditions, raw)
	}

	for i := range pods {
		raw, err := marshalClean(&pods[i])
		if err != nil {
			return nil, err
		}
		capture.Pods = append(capture.Pods, raw)
	}

	events, err := c.events(ctx, node, pods)
	if err != nil {
		return nil, err
	}
	capture.Events = events

	capture.Logs = c.logs(ctx, pods)
	return capture, nil
}

// splitConditions separates kubelet conditions from externally set ones.
//
// This is where node-problem-detector's output is kept out of the evidence.
// NPD conditions sit on the Node object alongside the kubelet's; storing the
// node verbatim would hand them to the reasoning layer through the back door.
// Split here, they reach the pre-filter as triggers and nothing else.
func splitConditions(conds []corev1.NodeCondition) (kubelet, external []corev1.NodeCondition) {
	for _, c := range conds {
		if kubeletConditions[c.Type] {
			kubelet = append(kubelet, c)
		} else {
			external = append(external, c)
		}
	}
	return kubelet, external
}

// events returns events for the node and its pods within the event window,
// oldest first.
func (c *Collector) events(ctx context.Context, node *corev1.Node, pods []corev1.Pod) ([]schema.RawJSON, error) {
	window := c.EventWindow
	if window <= 0 {
		window = DefaultEventWindow
	}
	cutoff := c.now().Add(-window)

	// Events live in the namespace of their involved object; node events in
	// "default". Query only namespaces that matter rather than the cluster.
	namespaces := map[string]bool{metav1.NamespaceDefault: true}
	podKeys := map[string]bool{}
	for _, p := range pods {
		namespaces[p.Namespace] = true
		podKeys[p.Namespace+"/"+p.Name] = true
	}
	nsList := make([]string, 0, len(namespaces))
	for ns := range namespaces {
		nsList = append(nsList, ns)
	}
	sort.Strings(nsList)

	var matched []corev1.Event
	for _, ns := range nsList {
		list, err := c.Client.CoreV1().Events(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list events in %q: %w", ns, err)
		}
		for _, e := range list.Items {
			relevant := (e.InvolvedObject.Kind == "Node" && e.InvolvedObject.Name == node.Name) ||
				(e.InvolvedObject.Kind == "Pod" && podKeys[e.InvolvedObject.Namespace+"/"+e.InvolvedObject.Name])
			if relevant && !eventTime(e).Before(cutoff) {
				matched = append(matched, e)
			}
		}
	}
	sort.SliceStable(matched, func(i, j int) bool { return eventTime(matched[i]).Before(eventTime(matched[j])) })

	out := make([]schema.RawJSON, 0, len(matched))
	for i := range matched {
		raw, err := marshalClean(&matched[i])
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, nil
}

// eventTime is the most recent time an event was observed. Events carry
// several timestamps, populated inconsistently across emitters.
func eventTime(e corev1.Event) time.Time {
	for _, t := range []time.Time{e.LastTimestamp.Time, e.EventTime.Time, e.FirstTimestamp.Time, e.CreationTimestamp.Time} {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

// logs reads logs for unhealthy containers only.
//
// Healthy containers are skipped deliberately. Their logs rarely explain a
// fault, they are where most secrets and PII live, and they would dominate
// the model's context. The current log is read for every unhealthy
// container, and the previous one too wherever the container has restarted —
// which is usually where the crash is.
func (c *Collector) logs(ctx context.Context, pods []corev1.Pod) map[string]string {
	tail, limit := c.LogTailLines, c.LogLimitBytes
	if tail <= 0 {
		tail = DefaultLogTailLines
	}
	if limit <= 0 {
		limit = DefaultLogLimitBytes
	}

	out := map[string]string{}
	for _, p := range pods {
		statuses := append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...)
		for _, s := range statuses {
			if !Unhealthy(s) {
				continue
			}
			key := p.Namespace + "/" + p.Name + "/" + s.Name
			if text, ok := c.readLog(ctx, p, s.Name, false, tail, limit); ok {
				out[key] = text
			}
			if s.RestartCount > 0 {
				if text, ok := c.readLog(ctx, p, s.Name, true, tail, limit); ok {
					out[key+"/previous"] = text
				}
			}
		}
	}
	return out
}

func (c *Collector) readLog(ctx context.Context, p corev1.Pod, container string, previous bool, tail, limit int64) (string, bool) {
	req := c.Client.CoreV1().Pods(p.Namespace).GetLogs(p.Name, &corev1.PodLogOptions{
		Container:  container,
		Previous:   previous,
		TailLines:  &tail,
		LimitBytes: &limit,
		Timestamps: true,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		// No previous instance, container never started, or logs rotated
		// away: none of these should stop the rest of the capture.
		return "", false
	}
	defer stream.Close()
	data, err := io.ReadAll(io.LimitReader(stream, limit))
	if err != nil || len(data) == 0 {
		return "", false
	}
	return string(data), true
}

// Unhealthy reports whether a container status warrants reading its logs.
func Unhealthy(s corev1.ContainerStatus) bool {
	if s.RestartCount > 0 || !s.Ready {
		return true
	}
	if s.State.Waiting != nil {
		return true
	}
	if t := s.State.Terminated; t != nil && t.ExitCode != 0 {
		return true
	}
	return false
}

// marshalClean serialises an API object without managedFields and the
// last-applied-configuration annotation. Both are noise for diagnosis, the
// latter can embed a full copy of the spec, and together they often double
// an object's size.
func marshalClean(obj metav1.Object) (schema.RawJSON, error) {
	obj.SetManagedFields(nil)
	if ann := obj.GetAnnotations(); ann != nil {
		if _, ok := ann[corev1.LastAppliedConfigAnnotation]; ok {
			clean := make(map[string]string, len(ann)-1)
			for k, v := range ann {
				if k != corev1.LastAppliedConfigAnnotation {
					clean[k] = v
				}
			}
			obj.SetAnnotations(clean)
		}
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", obj.GetName(), err)
	}
	return raw, nil
}
