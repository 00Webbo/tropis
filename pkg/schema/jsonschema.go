package schema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// JSONSchemaVersion identifies the draft the generated schema conforms to.
const JSONSchemaVersion = "https://json-schema.org/draft/2020-12/schema"

// VerdictJSONSchema returns the JSON Schema for a Verdict, derived from the Go
// types by reflection.
//
// It is generated rather than hand-written so it cannot drift from the structs:
// the schema is used for schema-constrained decoding by model backends, for
// CRD validation, and for documentation, and a stale copy in any of those
// places would be worse than none.
//
// Constraints that JSON Schema can express are expressed here. The conditional
// rule tying RootCause to a causal Relationship is emitted as an if/then/else,
// but Validate remains the authority — Go validation is what backends run, and
// not every consumer of this schema enforces conditionals.
func VerdictJSONSchema() map[string]any {
	s := schemaForType(reflect.TypeOf(Verdict{}))
	s["$schema"] = JSONSchemaVersion
	s["$id"] = "https://tropis.io/schema/v1alpha1/verdict.json"
	s["title"] = "Verdict"
	s["description"] = "The result of one Tropis analysis pass over one node."

	props, _ := s["properties"].(map[string]any)

	// Enumerations. Reflection cannot see these, so they are attached from the
	// same constants Validate uses.
	if rel, ok := props["relationship"].(map[string]any); ok {
		rel["enum"] = []string{
			string(RelationshipCausal),
			string(RelationshipCoincidental),
			string(RelationshipInsufficientEvidence),
		}
	}
	if rc, ok := props["rootCause"].(map[string]any); ok {
		if rcProps, ok := rc["properties"].(map[string]any); ok {
			if layer, ok := rcProps["layer"].(map[string]any); ok {
				layer["enum"] = []string{string(LayerHost), string(LayerKubernetes)}
			}
		}
	}

	// Numeric and cardinality bounds mirroring Validate.
	if conf, ok := props["confidence"].(map[string]any); ok {
		conf["minimum"] = 0
		conf["maximum"] = 1
	}
	if ev, ok := props["evidence"].(map[string]any); ok {
		ev["minItems"] = 1
	}

	// RootCause is permitted only on a causal verdict.
	s["if"] = map[string]any{
		"properties": map[string]any{
			"relationship": map[string]any{"const": string(RelationshipCausal)},
		},
		"required": []string{"relationship"},
	}
	s["then"] = map[string]any{"required": []string{"rootCause"}}
	s["else"] = map[string]any{
		"not": map[string]any{"required": []string{"rootCause"}},
	}

	return s
}

// VerdictJSONSchemaBytes returns the Verdict JSON Schema as indented JSON.
func VerdictJSONSchemaBytes() ([]byte, error) {
	b, err := json.MarshalIndent(VerdictJSONSchema(), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal verdict schema: %w", err)
	}
	return append(b, '\n'), nil
}

var timeType = reflect.TypeOf(time.Time{})

// schemaForType builds a JSON Schema fragment for a Go type by reflection.
func schemaForType(t reflect.Type) map[string]any {
	// time.Time is a struct but serialises as an RFC 3339 string.
	if t == timeType {
		return map[string]any{"type": "string", "format": "date-time"}
	}

	switch t.Kind() {
	case reflect.Pointer:
		return schemaForType(t.Elem())

	case reflect.String:
		return map[string]any{"type": "string"}

	case reflect.Bool:
		return map[string]any{"type": "boolean"}

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}

	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}

	case reflect.Slice, reflect.Array:
		return map[string]any{
			"type":  "array",
			"items": schemaForType(t.Elem()),
		}

	case reflect.Map:
		return map[string]any{
			"type":                 "object",
			"additionalProperties": schemaForType(t.Elem()),
		}

	case reflect.Struct:
		props := map[string]any{}
		var required []string

		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, opts, ok := parseJSONTag(f)
			if !ok {
				continue
			}

			field := schemaForType(f.Type)
			if doc := fieldDoc(t, f.Name); doc != "" {
				field["description"] = doc
			}
			props[name] = field

			// A field is required unless it is omitempty or a pointer; those are
			// the ones that can legitimately be absent from the wire form.
			if !opts.omitempty && f.Type.Kind() != reflect.Pointer {
				required = append(required, name)
			}
		}

		s := map[string]any{
			"type":                 "object",
			"properties":           props,
			"additionalProperties": false,
		}
		if len(required) > 0 {
			s["required"] = required
		}
		return s

	default:
		// Interfaces and anything else unmapped: accept any JSON value rather
		// than emitting an unsatisfiable schema.
		return map[string]any{}
	}
}

type jsonTagOpts struct {
	omitempty bool
}

// parseJSONTag returns the wire name and options for a struct field, and
// whether the field is serialised at all.
func parseJSONTag(f reflect.StructField) (string, jsonTagOpts, bool) {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", jsonTagOpts{}, false
	}

	name, rest, _ := strings.Cut(tag, ",")
	if name == "" {
		name = f.Name
	}

	var opts jsonTagOpts
	for _, o := range strings.Split(rest, ",") {
		if o == "omitempty" {
			opts.omitempty = true
		}
	}
	return name, opts, true
}
