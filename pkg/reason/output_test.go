package reason

import (
	"errors"
	"strings"
	"testing"

	"github.com/00Webbo/tropis/pkg/schema"
)

var testInfo = schema.BackendInfo{Provider: "test", Model: "test-1", PromptVersion: PromptVersion}

func builtInput(t *testing.T) *AnalysisInput {
	t.Helper()
	in, err := BuildInput(plantedRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	return in
}

const validCausal = `{
 "reasoning": "Disk failing, postgres fsync errors on it.",
 "relationship": "causal",
 "rootCause": {"layer": "host", "description": "/dev/sdb is failing."},
 "confidence": 0.9,
 "evidence": [
  {"ref": "smart:/dev/sdb#197", "excerpt": "Current_Pending_Sector 168"},
  {"ref": "log:db/postgres-0/postgres/previous#L6", "excerpt": "could not fsync file"}
 ],
 "nextStep": "Consider replacing /dev/sdb."
}`

func TestFinalizeValid(t *testing.T) {
	in := builtInput(t)
	v, err := Finalize([]byte(validCausal), in, testInfo)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if v.Relationship != schema.RelationshipCausal || v.RootCause.Layer != schema.LayerHost {
		t.Errorf("verdict = %+v", v)
	}
	if v.Node != "worker-03" || v.InputDigest != in.Digest() || v.Backend != testInfo {
		t.Errorf("backend-filled fields wrong: %+v", v)
	}
	if v.Evidence[0].Source != "smartctl" || v.Evidence[1].Source != "pod-logs" {
		t.Errorf("sources = %s, %s", v.Evidence[0].Source, v.Evidence[1].Source)
	}
	if v.Evidence[0].CollectedAt.IsZero() {
		t.Error("evidence collectedAt should be filled from the input")
	}
	if len(v.TriggeredBy) != 2 {
		t.Errorf("triggeredBy = %v", v.TriggeredBy)
	}
}

// Malformed output is always an error, never a verdict.
func TestFinalizeRejectsMalformedOutput(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"not JSON", `The disk is broken.`, ""},
		{"empty", ``, ""},
		{"truncated", validCausal[:120], ""},
		{"unknown field", strings.Replace(validCausal, `"nextStep"`, `"verdict":"yes","nextStep"`, 1), "unknown field"},
		{"two objects", validCausal + validCausal, "trailing"},
		{"invented relationship", strings.Replace(validCausal, `"causal"`, `"likely"`, 1), "relationship"},
		{"root cause on coincidental", strings.Replace(validCausal, `"causal"`, `"coincidental"`, 1), "rootCause"},
		{"causal without root cause", strings.Replace(validCausal, `{"layer": "host", "description": "/dev/sdb is failing."}`, `null`, 1), "rootCause"},
		{"confidence out of range", strings.Replace(validCausal, `0.9`, `1.7`, 1), "confidence"},
		{"no evidence", `{"reasoning":"x","relationship":"insufficient_evidence","rootCause":null,"confidence":0.3,"evidence":[],"nextStep":""}`, "evidence"},
		{"fabricated ref", strings.Replace(validCausal, `smart:/dev/sdb#197`, `smart:/dev/sdz#197`, 1), "not present in the input"},
		{"log line out of range", strings.Replace(validCausal, `#L6`, `#L99`, 1), "not present in the input"},
		{"line suffix on a non-log ref", strings.Replace(validCausal, `smart:/dev/sdb#197`, `smart:/dev/sdb#L1`, 1), "not present in the input"},
		{"empty excerpt", strings.Replace(validCausal, `"Current_Pending_Sector 168"`, `""`, 1), "excerpt"},
		{"bad layer", strings.Replace(validCausal, `"host"`, `"firmware"`, 1), "layer"},
	}
	in := builtInput(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := Finalize([]byte(tt.raw), in, testInfo)
			if err == nil {
				t.Fatalf("expected an error, got verdict %+v", v)
			}
			if !errors.Is(err, ErrMalformedOutput) {
				t.Errorf("error should wrap ErrMalformedOutput: %v", err)
			}
			if tt.want != "" && !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q should mention %q", err, tt.want)
			}
			if v.Node != "" || v.Relationship != "" {
				t.Errorf("a failed Finalize must return a zero verdict, got %+v", v)
			}
		})
	}
}

// insufficient_evidence and coincidental are real answers that finalize
// cleanly.
func TestFinalizeNonCausalAnswers(t *testing.T) {
	in := builtInput(t)
	for _, rel := range []string{"coincidental", "insufficient_evidence"} {
		raw := `{"reasoning":"r","relationship":"` + rel + `","rootCause":null,"confidence":0.4,` +
			`"evidence":[{"ref":"pod:db/postgres-0","excerpt":"CrashLoopBackOff"}],"nextStep":"Keep watching."}`
		v, err := Finalize([]byte(raw), in, testInfo)
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if string(v.Relationship) != rel || v.RootCause != nil {
			t.Errorf("%s: verdict = %+v", rel, v)
		}
	}
}

// Some local models wrap JSON in a code fence despite instructions. That one
// wrapping is tolerated; anything else is not.
func TestFinalizeToleratesCodeFence(t *testing.T) {
	in := builtInput(t)
	if _, err := Finalize([]byte("```json\n"+validCausal+"\n```"), in, testInfo); err != nil {
		t.Errorf("fenced JSON should parse: %v", err)
	}
	if _, err := Finalize([]byte("Here you go:\n"+validCausal), in, testInfo); err == nil {
		t.Error("prose before the JSON should not be tolerated")
	}
}

func TestOutputSchemaIsConstrainedDecodingSafe(t *testing.T) {
	var check func(path string, v any)
	check = func(path string, v any) {
		m, ok := v.(map[string]any)
		if !ok {
			if arr, ok := v.([]any); ok {
				for i, e := range arr {
					check(path, e)
					_ = i
				}
			}
			return
		}
		for _, banned := range []string{"minimum", "maximum", "minItems", "maxItems", "minLength", "if", "then", "else"} {
			if _, ok := m[banned]; ok {
				t.Errorf("%s uses %q, unsupported in constrained decoding", path, banned)
			}
		}
		if m["type"] == "object" {
			if m["additionalProperties"] != false {
				t.Errorf("%s: objects must set additionalProperties false", path)
			}
			props, _ := m["properties"].(map[string]any)
			req, _ := m["required"].([]string)
			if len(req) != len(props) {
				t.Errorf("%s: every property must be required (%d of %d)", path, len(req), len(props))
			}
		}
		for k, child := range m {
			check(path+"."+k, child)
		}
	}
	check("$", OutputSchema())
}
