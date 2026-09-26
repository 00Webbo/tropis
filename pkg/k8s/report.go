package k8s

import (
	"context"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"

	tropis "github.com/00Webbo/tropis/pkg/schema"
)

// The NodeHealthReport API. v1alpha1, loudly: it will change before it is
// stable, which will not happen until the evaluation shows which fields
// actually carry weight.
const (
	Group    = "tropis.io"
	Version  = "v1alpha1"
	Kind     = "NodeHealthReport"
	Resource = "nodehealthreports"
)

// ReportGVR identifies NodeHealthReports for the dynamic client.
var ReportGVR = schema.GroupVersionResource{Group: Group, Version: Version, Resource: Resource}

// ReportWriter writes verdicts as NodeHealthReport resources, one per node,
// named after the node, with the verdict as .status.
//
// This is not an operator. There is no controller and no reconciliation
// loop: a verdict is written when one is produced, and that is all.
type ReportWriter struct {
	Client dynamic.Interface
}

// Write creates or updates the node's report with this verdict.
func (w *ReportWriter) Write(ctx context.Context, v tropis.Verdict) error {
	if err := v.Validate(); err != nil {
		return fmt.Errorf("refusing to write an invalid verdict: %w", err)
	}
	status, err := StatusFromVerdict(v)
	if err != nil {
		return err
	}
	reports := w.Client.Resource(ReportGVR)

	obj, err := reports.Get(ctx, v.Node, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		obj = &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": Group + "/" + Version,
			"kind":       Kind,
			"metadata": map[string]any{
				"name":   v.Node,
				"labels": map[string]any{"app.kubernetes.io/managed-by": "tropis"},
			},
			"spec": map[string]any{"nodeName": v.Node},
		}}
		if obj, err = reports.Create(ctx, obj, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create NodeHealthReport %s: %w", v.Node, err)
		}
	case err != nil:
		return fmt.Errorf("get NodeHealthReport %s: %w", v.Node, err)
	}

	// A label on the relationship lets `kubectl get nhr -l
	// tropis.io/relationship=causal` find the nodes that matter.
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	if labels["tropis.io/relationship"] != string(v.Relationship) {
		labels["tropis.io/relationship"] = string(v.Relationship)
		obj.SetLabels(labels)
		if obj, err = reports.Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("label NodeHealthReport %s: %w", v.Node, err)
		}
	}

	obj.Object["status"] = status
	if _, err := reports.UpdateStatus(ctx, obj, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update NodeHealthReport %s status: %w", v.Node, err)
	}
	return nil
}

// StatusFromVerdict renders a verdict as a report's .status. It is the same
// JSON `tropis analyze --json` prints: one type, serialised once.
func StatusFromVerdict(v tropis.Verdict) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var status map[string]any
	if err := json.Unmarshal(raw, &status); err != nil {
		return nil, err
	}
	return status, nil
}

// VerdictFromReport reads the verdict back out of a report.
func VerdictFromReport(obj *unstructured.Unstructured) (tropis.Verdict, error) {
	var v tropis.Verdict
	status, ok := obj.Object["status"]
	if !ok {
		return v, fmt.Errorf("NodeHealthReport %s has no status", obj.GetName())
	}
	raw, err := json.Marshal(status)
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(raw, &v)
	return v, err
}

// CRDYAML renders the NodeHealthReport CustomResourceDefinition. Its .status
// schema is generated from the Verdict type, so the CRD cannot drift from
// what Tropis writes.
func CRDYAML() ([]byte, error) {
	str := map[string]any{"type": "string"}
	crd := map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata": map[string]any{
			"name": Resource + "." + Group,
			"annotations": map[string]any{
				"tropis.io/generated-by": "go run ./hack/gen-crd; do not edit",
			},
		},
		"spec": map[string]any{
			"group": Group,
			"scope": "Cluster",
			"names": map[string]any{
				"kind":       Kind,
				"listKind":   Kind + "List",
				"plural":     Resource,
				"singular":   "nodehealthreport",
				"shortNames": []string{"nhr"},
			},
			"versions": []any{map[string]any{
				"name":         Version,
				"served":       true,
				"storage":      true,
				"subresources": map[string]any{"status": map[string]any{}},
				"additionalPrinterColumns": []any{
					map[string]any{"name": "Relationship", "type": "string", "jsonPath": ".status.relationship"},
					map[string]any{"name": "Layer", "type": "string", "jsonPath": ".status.rootCause.layer"},
					map[string]any{"name": "Confidence", "type": "number", "jsonPath": ".status.confidence"},
					map[string]any{"name": "Observed", "type": "date", "jsonPath": ".status.observedAt"},
					map[string]any{"name": "Backend", "type": "string", "jsonPath": ".status.backend.model", "priority": 1},
				},
				"schema": map[string]any{"openAPIV3Schema": map[string]any{
					"type": "object",
					"description": "NodeHealthReport is Tropis's latest verdict on one node: whether its host layer " +
						"explains its Kubernetes-layer symptoms. v1alpha1: this API will change.",
					"properties": map[string]any{
						"apiVersion": str,
						"kind":       str,
						"metadata":   map[string]any{"type": "object"},
						"spec": map[string]any{
							"type":       "object",
							"required":   []string{"nodeName"},
							"properties": map[string]any{"nodeName": map[string]any{"type": "string", "description": "The node this report concerns."}},
						},
						"status": tropis.VerdictStructuralSchema(),
					},
				}},
			}},
		},
	}
	return yaml.Marshal(crd)
}
