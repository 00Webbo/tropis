package fixture

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/00Webbo/tropis/pkg/schema"
)

// A record in the exact shape hack/inject/lib.sh's emit prints for
// read-errors.sh stop. The scripts' output must slot into label.json under the
// same strict decoding LoadLabel uses, or captured labels would fail to load.
// If emit's format changes, this must change with it.
const injectRecord = `{"faultType":"disk.read_errors","injection":{"tool":"dm-error","device":"/dev/loop2","params":{"badBlocks":"200000 200001","blockSize":"512","dmDevice":"/dev/mapper/tropis-target","smartBefore":"unavailable","smartAfter":"unavailable","smartEffect":"none"},"startedAt":"2026-09-26T10:14:03Z","endedAt":"2026-09-26T10:14:04Z"}}`

func TestInjectionRecordFitsLabel(t *testing.T) {
	var fragment struct {
		FaultType string            `json:"faultType"`
		Injection *schema.Injection `json:"injection"`
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(injectRecord)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fragment); err != nil {
		t.Fatalf("injection record does not decode strictly: %v", err)
	}

	layer := schema.LayerHost
	label := schema.Label{
		ScenarioID:     "disk-read-errors-01",
		Relationship:   schema.RelationshipCausal,
		RootCauseLayer: &layer,
		FaultType:      fragment.FaultType,
		Injection:      fragment.Injection,
	}
	if err := label.Validate(); err != nil {
		t.Fatalf("label built from an injection record is invalid: %v", err)
	}
	if label.Injection.Params["smartEffect"] != "none" || label.Injection.EndedAt.IsZero() {
		t.Errorf("injection = %+v", label.Injection)
	}
}
