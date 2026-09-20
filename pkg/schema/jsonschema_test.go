package schema

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestVerdictJSONSchemaShape(t *testing.T) {
	s := VerdictJSONSchema()

	if s["$schema"] != JSONSchemaVersion {
		t.Errorf("$schema = %v, want %v", s["$schema"], JSONSchemaVersion)
	}
	if s["type"] != "object" {
		t.Errorf("type = %v, want object", s["type"])
	}

	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties missing or not an object")
	}

	// Every wire field of Verdict must appear.
	for _, want := range []string{
		"node", "observedAt", "agentVersion", "backend", "relationship",
		"rootCause", "confidence", "evidence", "nextStep", "triggeredBy",
		"inputDigest",
	} {
		if _, ok := props[want]; !ok {
			t.Errorf("property %q missing from schema", want)
		}
	}
}

func TestVerdictJSONSchemaEnums(t *testing.T) {
	props := VerdictJSONSchema()["properties"].(map[string]any)

	rel := props["relationship"].(map[string]any)
	got, ok := rel["enum"].([]string)
	if !ok {
		t.Fatalf("relationship enum missing, got %T", rel["enum"])
	}
	want := []string{"causal", "coincidental", "insufficient_evidence"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("relationship enum = %v, want %v", got, want)
	}

	layer := props["rootCause"].(map[string]any)["properties"].(map[string]any)["layer"].(map[string]any)
	gotLayer, ok := layer["enum"].([]string)
	if !ok {
		t.Fatalf("layer enum missing, got %T", layer["enum"])
	}
	if !reflect.DeepEqual(gotLayer, []string{"host", "kubernetes"}) {
		t.Errorf("layer enum = %v", gotLayer)
	}
}

// The schema must carry the bounds Validate enforces, since backends rely on
// it for constrained decoding.
func TestVerdictJSONSchemaConstraints(t *testing.T) {
	s := VerdictJSONSchema()
	props := s["properties"].(map[string]any)

	conf := props["confidence"].(map[string]any)
	if conf["minimum"] != 0 || conf["maximum"] != 1 {
		t.Errorf("confidence bounds = [%v, %v], want [0, 1]", conf["minimum"], conf["maximum"])
	}

	ev := props["evidence"].(map[string]any)
	if ev["minItems"] != 1 {
		t.Errorf("evidence minItems = %v, want 1", ev["minItems"])
	}
	if ev["type"] != "array" {
		t.Errorf("evidence type = %v, want array", ev["type"])
	}

	// The conditional tying rootCause to a causal relationship.
	ifClause, ok := s["if"].(map[string]any)
	if !ok {
		t.Fatal("missing if clause for the rootCause/relationship rule")
	}
	constVal := ifClause["properties"].(map[string]any)["relationship"].(map[string]any)["const"]
	if constVal != "causal" {
		t.Errorf("if clause pins relationship to %v, want causal", constVal)
	}
	if _, ok := s["then"]; !ok {
		t.Error("missing then clause requiring rootCause")
	}
	if _, ok := s["else"]; !ok {
		t.Error("missing else clause forbidding rootCause")
	}
}

// Optional fields must not be listed as required; mandatory ones must be.
func TestVerdictJSONSchemaRequired(t *testing.T) {
	s := VerdictJSONSchema()
	required, _ := s["required"].([]string)

	isRequired := make(map[string]bool, len(required))
	for _, r := range required {
		isRequired[r] = true
	}

	for _, want := range []string{"node", "relationship", "confidence", "evidence", "inputDigest"} {
		if !isRequired[want] {
			t.Errorf("%q should be required", want)
		}
	}
	// rootCause is a pointer, nextStep and triggeredBy are omitempty.
	for _, notWant := range []string{"rootCause", "nextStep", "triggeredBy"} {
		if isRequired[notWant] {
			t.Errorf("%q should not be unconditionally required", notWant)
		}
	}
}

func TestVerdictJSONSchemaTimeFormat(t *testing.T) {
	props := VerdictJSONSchema()["properties"].(map[string]any)
	observedAt := props["observedAt"].(map[string]any)

	if observedAt["type"] != "string" {
		t.Errorf("observedAt type = %v, want string", observedAt["type"])
	}
	if observedAt["format"] != "date-time" {
		t.Errorf("observedAt format = %v, want date-time", observedAt["format"])
	}
}

func TestVerdictJSONSchemaBytesIsValidJSON(t *testing.T) {
	b, err := VerdictJSONSchemaBytes()
	if err != nil {
		t.Fatalf("VerdictJSONSchemaBytes: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("generated schema is not valid JSON: %v", err)
	}
	if parsed["title"] != "Verdict" {
		t.Errorf("title = %v, want Verdict", parsed["title"])
	}
}

// Descriptions are read by the model during constrained decoding, so a field
// added without one silently degrades output quality. Fail instead.
func TestFieldDocsCoverVerdict(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeOf(Verdict{}),
		reflect.TypeOf(RootCause{}),
		reflect.TypeOf(Evidence{}),
		reflect.TypeOf(BackendInfo{}),
	}

	for _, typ := range types {
		docs, ok := fieldDocs[typ.Name()]
		if !ok {
			t.Errorf("no fieldDocs entry for type %s", typ.Name())
			continue
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if !f.IsExported() {
				continue
			}
			if _, _, serialised := parseJSONTag(f); !serialised {
				continue
			}
			if docs[f.Name] == "" {
				t.Errorf("%s.%s has no description in fieldDocs", typ.Name(), f.Name)
			}
		}
		// Catch entries left behind after a field is renamed or removed.
		for name := range docs {
			if _, ok := typ.FieldByName(name); !ok {
				t.Errorf("fieldDocs[%s] has a stale entry %q", typ.Name(), name)
			}
		}
	}
}
