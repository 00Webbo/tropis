package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"

	"github.com/nathanwebb/tropis/pkg/schema"
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
// the node and asks it over HTTP. This keeps every API write, and every API
// permission, on the analyser side.
type HostFetcher struct {
	Client    kubernetes.Interface
	Namespace string
	Selector  string
	Port      int
	HTTP      *http.Client
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

func (f *HostFetcher) httpClient() *http.Client {
	if f.HTTP != nil {
		return f.HTTP
	}
	// SMART on a degrading drive can take a while; a cold collector may be
	// querying every device when the request arrives.
	return &http.Client{Timeout: 3 * time.Minute}
}

// CollectorURL returns the URL of the collector pod on nodeName.
func (f *HostFetcher) CollectorURL(ctx context.Context, nodeName string) (string, error) {
	pods, err := f.Client.CoreV1().Pods(f.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: f.selector(),
		FieldSelector: fields.OneTermEqualSelector("spec.nodeName", nodeName).String(),
	})
	if err != nil {
		return "", fmt.Errorf("find collector on %q: %w", nodeName, err)
	}
	for _, p := range pods.Items {
		if p.Spec.NodeName == nodeName && p.Status.Phase == corev1.PodRunning && p.Status.PodIP != "" {
			host := net.JoinHostPort(p.Status.PodIP, strconv.Itoa(f.port()))
			return "http://" + host + collectorSMARTPath, nil
		}
	}
	return "", fmt.Errorf("no running tropis-collector pod on node %q (selector %q in namespace %q)",
		nodeName, f.selector(), f.Namespace)
}

// Fetch returns the host capture for nodeName.
func (f *HostFetcher) Fetch(ctx context.Context, nodeName string) (*schema.HostCapture, error) {
	url, err := f.CollectorURL(ctx, nodeName)
	if err != nil {
		return nil, err
	}
	return FetchHostCapture(ctx, f.httpClient(), url)
}

// FetchHostCapture GETs a host capture from a collector URL.
func FetchHostCapture(ctx context.Context, client *http.Client, url string) (*schema.HostCapture, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch host capture from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("collector at %s returned %s: %s", url, resp.Status, body)
	}
	var hc schema.HostCapture
	// A host capture is a few kilobytes per device; cap it well above that
	// rather than trusting the peer.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&hc); err != nil {
		return nil, fmt.Errorf("decode host capture from %s: %w", url, err)
	}
	return &hc, nil
}
