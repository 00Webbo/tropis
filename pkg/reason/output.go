package reason

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/00Webbo/tropis/pkg/schema"
)

// ModelOutput is what the model is asked to produce. The backend fills the
// rest of the Verdict — node, timestamps, backend identity, digest — from the
// input, so the model cannot get them wrong.
type ModelOutput struct {
	// Reasoning is a short account of the judgement. It is not part of the
	// verdict; it exists because writing it before answering improves the
	// answer, particularly on smaller local models without native thinking.
	Reasoning    string          `json:"reasoning"`
	Relationship string          `json:"relationship"`
	RootCause    *ModelRootCause `json:"rootCause"`
	Confidence   float64         `json:"confidence"`
	Evidence     []ModelEvidence `json:"evidence"`
	NextStep     string          `json:"nextStep"`
}

// ModelRootCause is the model's root cause, null unless causal.
type ModelRootCause struct {
	Layer       string `json:"layer"`
	Description string `json:"description"`
}

// ModelEvidence cites an item in the input by its ref.
type ModelEvidence struct {
	Ref     string `json:"ref"`
	Excerpt string `json:"excerpt"`
}

// OutputSchema is the JSON Schema for ModelOutput, used for constrained
// decoding.
//
// It uses only what constrained decoding supports across providers: every
// object closed with additionalProperties false, every property required,
// nullable via anyOf. Bounds such as confidence in [0, 1] and the rule that
// only a causal verdict carries a root cause cannot be expressed here, so
// Finalize enforces them — as it would anyway, since not every backend
// constrains decoding.
func OutputSchema() map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"reasoning", "relationship", "rootCause", "confidence", "evidence", "nextStep"},
		"properties": map[string]any{
			"reasoning": str("Two to five sentences: what the host layer shows, what the Kubernetes layer shows, and whether the timing and mechanism connect them."),
			"relationship": map[string]any{
				"type": "string",
				"enum": []string{
					string(schema.RelationshipCausal),
					string(schema.RelationshipCoincidental),
					string(schema.RelationshipInsufficientEvidence),
				},
			},
			"rootCause": map[string]any{
				"anyOf": []any{
					map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"required":             []string{"layer", "description"},
						"properties": map[string]any{
							"layer":       map[string]any{"type": "string", "enum": []string{string(schema.LayerHost), string(schema.LayerKubernetes)}},
							"description": str("The fault and how it produces the symptoms."),
						},
					},
					map[string]any{"type": "null"},
				},
				"description": "Required when relationship is causal; null otherwise.",
			},
			"confidence": map[string]any{"type": "number", "description": "Calibrated probability from 0 to 1 that this verdict is correct."},
			"evidence": map[string]any{
				"type":        "array",
				"description": "At least one item. Cite refs exactly as they appear in the input.",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"ref", "excerpt"},
					"properties": map[string]any{
						"ref":     str("A ref from the input, e.g. smart:/dev/sdb#197, event:<uid>, pod:ns/name, log:ns/pod/container#L12."),
						"excerpt": str("The specific value or line relied on, quoted."),
					},
				},
			},
			"nextStep": str("Advisory text for a human operator. A recommendation, never an instruction to act."),
		},
	}
}

// Finalize turns raw model output into a validated Verdict.
//
// It is the single gate every backend passes through, and it never repairs:
// unknown fields, a citation of anything not in the input, or any violation
// of schema.Verdict.Validate produce an error wrapping ErrMalformedOutput.
// A guessed-at verdict would be worse than none — it would reach a CRD or an
// accuracy number looking exactly like a real one.
func Finalize(raw []byte, in *AnalysisInput, info schema.BackendInfo) (schema.Verdict, error) {
	if !in.Built() {
		return schema.Verdict{}, ErrUnbuiltInput
	}

	var out ModelOutput
	dec := json.NewDecoder(bytes.NewReader(extractJSON(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return schema.Verdict{}, fmt.Errorf("%w: %v", ErrMalformedOutput, err)
	}
	if dec.More() {
		return schema.Verdict{}, fmt.Errorf("%w: trailing content after the JSON object", ErrMalformedOutput)
	}

	v := schema.Verdict{
		Node:         in.Node(),
		ObservedAt:   in.ObservedAt(),
		AgentVersion: AgentVersion,
		Backend:      info,
		Relationship: schema.Relationship(out.Relationship),
		Confidence:   out.Confidence,
		NextStep:     strings.TrimSpace(out.NextStep),
		TriggeredBy:  in.TriggeredBy(),
		InputDigest:  in.Digest(),
	}
	if out.RootCause != nil {
		v.RootCause = &schema.RootCause{
			Layer:       schema.Layer(out.RootCause.Layer),
			Description: strings.TrimSpace(out.RootCause.Description),
		}
	}

	var unknown []string
	for _, e := range out.Evidence {
		ref := strings.TrimSpace(e.Ref)
		info, ok := in.lookupRef(ref)
		if !ok {
			unknown = append(unknown, ref)
			continue
		}
		v.Evidence = append(v.Evidence, schema.Evidence{
			Source:      info.source,
			CollectedAt: info.collectedAt,
			Ref:         ref,
			Excerpt:     strings.TrimSpace(e.Excerpt),
		})
	}
	// A citation of something not in the input is a fabricated citation.
	if len(unknown) > 0 {
		return schema.Verdict{}, fmt.Errorf("%w: evidence cites refs not present in the input: %s",
			ErrMalformedOutput, strings.Join(unknown, ", "))
	}

	if err := v.Validate(); err != nil {
		return schema.Verdict{}, fmt.Errorf("%w: %v", ErrMalformedOutput, err)
	}
	return v, nil
}

// extractJSON tolerates exactly one kind of wrapping: a single markdown code
// fence around the object, which some local models emit even when asked not
// to. Anything else is passed through to fail parsing honestly.
func extractJSON(raw []byte) []byte {
	s := bytes.TrimSpace(raw)
	if !bytes.HasPrefix(s, []byte("```")) {
		return s
	}
	s = s[3:]
	if nl := bytes.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	}
	if end := bytes.LastIndex(s, []byte("```")); end >= 0 {
		s = s[:end]
	}
	return bytes.TrimSpace(s)
}
