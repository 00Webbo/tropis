package schema

import (
	"bytes"
	"encoding/json"
	"errors"
)

// RawJSON is a verbatim JSON document, stored and replayed byte-for-byte.
//
// Captures are held raw rather than parsed so that replay exercises the real
// parser. A fixture storing pre-parsed structs could not catch a parser
// regression, which is half of what the corpus exists to do — and the corpus
// cannot be recaptured without hardware, so it has to outlive changes to the
// parsing code.
//
// It behaves as json.RawMessage, with a MarshalJSON that never emits invalid
// JSON for a zero value.
type RawJSON []byte

// MarshalJSON returns the document verbatim, or null when empty.
func (r RawJSON) MarshalJSON() ([]byte, error) {
	if len(r) == 0 {
		return []byte("null"), nil
	}
	return r, nil
}

// UnmarshalJSON stores the document verbatim.
func (r *RawJSON) UnmarshalJSON(data []byte) error {
	if r == nil {
		return errors.New("schema.RawJSON: UnmarshalJSON on nil pointer")
	}
	*r = append((*r)[:0], data...)
	return nil
}

// String returns the raw document as a string.
func (r RawJSON) String() string { return string(r) }

// Len returns the size of the raw document in bytes.
func (r RawJSON) Len() int { return len(r) }

// IsEmpty reports whether the document is absent or JSON null.
func (r RawJSON) IsEmpty() bool {
	trimmed := bytes.TrimSpace(r)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

// Valid reports whether the document is syntactically valid JSON. Empty is not
// valid; callers wanting to allow absence should check IsEmpty first.
func (r RawJSON) Valid() bool {
	return json.Valid(r)
}
