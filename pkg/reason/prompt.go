package reason

import (
	_ "embed"
	"fmt"
)

// PromptVersion identifies what the model sees: the prompt below and the shape
// of the document BuildInput produces. Eval results are comparable only within
// a prompt version, so any change to either that could change model output
// must come with a new version (and, for the prompt, a new file) — never an
// edit in place.
//
// v2: prompt text identical to v1; trigger names (pre-filter rule IDs and NPD
// conditions) were removed from the model input.
//
// v3: host.unreadable also carries devices smartctl produced no output for
// (a drive that hung until the timeout), and the prompt says what a timeout
// means.
const PromptVersion = "v3"

//go:embed prompts/v3.md
var systemPrompt string

// SystemPrompt returns the versioned system prompt. It is identical for every
// node, which is what makes it cacheable.
func SystemPrompt() string { return systemPrompt }

// UserMessage returns the per-node message: the redacted input document.
// It fails on any input not produced by BuildInput.
func UserMessage(in *AnalysisInput) (string, error) {
	text, err := in.Text()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Captured state of node %q follows.\n\n```json\n%s\n```", in.doc.Node, text), nil
}
