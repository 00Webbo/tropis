package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"

	"github.com/00Webbo/tropis/pkg/schema"
)

// Defaults for locating the host collector.
const (
	DefaultCollectorSelector = "app.kubernetes.io/component=collector,app.kubernetes.io/name=tropis"
	DefaultCollectorPort     = 9476
	collectorSMARTPath       = "/v1/smart"
)

// HostFetcher retrieves a node's host capture from the tropis-collector pod
// running on it.
//
// The collector holds no Kubernetes API permissions, so it cannot publish
// anything itself. The analyser pulls instead: it finds the collector pod on
// the node and asks it through the API server's pod proxy. Going through the
// API server rather than to the pod IP means `tropis analyze` works the same
// from a laptop as from inside the cluster, and needs only get on pods/proxy
// in Tropis's own namespace — granted by a namespaced Role, never cluster-wide.
type HostFetcher struct {
	Client    kubernetes.Interface
	Namespace string
	Selector  string
	Port      int

	// get fetches a path from a pod. Replaced in tests; the fake clientset
	// cannot serve proxy requests.
	get func(ctx context.Context, namespace, pod string, port int, path string) ([]byte, error)
}

func (f *HostFetcher) selector() string {
	if f.Selector == "" {
		return DefaultCollectorSelector
	}
	return f.Selector
}

func (f *HostFetcher) port() int {
	if f.Port == 0 {
		return DefaultCollectorPort
	}
	return f.Port
}

// CollectorPod returns the name of the running collector pod on nodeName.
func (f *HostFetcher) CollectorPod(ctx context.Context, nodeName string) (string, error) {
	pods, err := f.Client.CoreV1().Pods(f.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: f.selector(),
		FieldSelector: fields.OneTermEqualSelector("spec.nodeName", nodeName).String(),
	})
	if err != nil {
		return "", fmt.Errorf("find collector on %q: %w", nodeName, err)
	}
	for _, p := range pods.Items {
		if p.Spec.NodeName == nodeName && p.Status.Phase == corev1.PodRunning {
			return p.Name, nil
		}
	}
	return "", fmt.Errorf("no running tropis-collector pod on node %q (selector %q in namespace %q)",
		nodeName, f.selector(), f.Namespace)
}

// Fetch returns the host capture for nodeName.
func (f *HostFetcher) Fetch(ctx context.Context, nodeName string) (*schema.HostCapture, error) {
	pod, err := f.CollectorPod(ctx, nodeName)
	if err != nil {
		return nil, err
	}
	get := f.get
	if get == nil {
		get = f.proxyGet
	}
	body, err := get(ctx, f.Namespace, pod, f.port(), collectorSMARTPath)
	if err != nil {
		return nil, fmt.Errorf("fetch host capture from %s/%s: %w", f.Namespace, pod, err)
	}
	var hc schema.HostCapture
	if err := json.Unmarshal(body, &hc); err != nil {
		return nil, fmt.Errorf("decode host capture from %s/%s: %w", f.Namespace, pod, err)
	}
	return &hc, nil
}

func (f *HostFetcher) proxyGet(ctx context.Context, namespace, pod string, port int, path string) ([]byte, error) {
	return f.Client.CoreV1().Pods(namespace).
		ProxyGet("http", pod, strconv.Itoa(port), path, nil).
		DoRaw(ctx)
}
