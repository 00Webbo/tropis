package schema

import "reflect"

// fieldDocs supplies human-readable descriptions for the generated JSON
// Schema, keyed by Go type name and field name.
//
// Go reflection cannot see doc comments, and parsing the package source at
// runtime would be far more machinery than this needs. The cost is that these
// strings must be kept in step with the doc comments on the structs; the test
// TestFieldDocsCoverVerdict fails if a field is added without an entry here,
// which is the part that actually matters.
//
// These descriptions are read by the model during schema-constrained
// decoding, so they are written as instructions to whoever fills the field in,
// not as notes to a maintainer.
var fieldDocs = map[string]map[string]string{
	"Verdict": {
		"Node":         "The Kubernetes node name this verdict concerns.",
		"ObservedAt":   "When the underlying signals were collected (RFC 3339).",
		"AgentVersion": "The Tropis build that produced this verdict.",
		"Backend":      "Which model produced this verdict, and under which prompt version.",
		"Relationship": "Whether the host-layer evidence explains the Kubernetes-layer symptoms. Use 'coincidental' when both a host anomaly and a workload problem are present but unrelated, and 'insufficient_evidence' when the evidence does not support a judgement either way. Both are expected answers, not failures.",
		"RootCause":    "The confirmed fault. Set this only when relationship is 'causal'; omit it entirely otherwise.",
		"Confidence":   "Calibrated confidence in this verdict, from 0 to 1. Report genuine uncertainty rather than anchoring high.",
		"Evidence":     "The specific observations this verdict relied on. Required and non-empty, including for 'insufficient_evidence', where what was examined and found wanting is the evidence.",
		"NextStep":     "Advisory text for a human operator. Phrase as a recommendation, never as an instruction to act: Tropis is read-only and takes no action.",
		"TriggeredBy":  "Pre-filter rule IDs that raised this node as a candidate.",
		"InputDigest":  "Hash of the exact analysis input, tying this verdict to the bytes that produced it.",
	},
	"RootCause": {
		"Layer":       "Which layer the fault originates in.",
		"Description": "Human-readable explanation of the fault.",
	},
	"Evidence": {
		"Source":      "The collector that produced this observation, for example 'smartctl', 'kubelet-events' or 'pod-logs'.",
		"CollectedAt": "When this specific observation was taken (RFC 3339).",
		"Ref":         "Where the observation sits within its source: a SMART attribute name, an event UID, a log offset.",
		"Excerpt":     "The specific line or value relied upon. Quote it rather than paraphrasing.",
	},
	"BackendInfo": {
		"Provider":      "Backend family, for example 'anthropic', 'ollama' or 'mock'.",
		"Model":         "The specific model identifier.",
		"PromptVersion": "Version of the prompt template used. Results are only comparable within a prompt version.",
	},
}

// fieldDoc returns the description for a field, or "" if none is recorded.
func fieldDoc(t reflect.Type, field string) string {
	return fieldDocs[t.Name()][field]
}
