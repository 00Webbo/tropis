package reason

import (
	_ "embed"
	"fmt"
)

// PromptVersion identifies the prompt below. Eval results are comparable only
// within a prompt version, so any edit to prompts/v1.md that could change
// model output must come with a new file and a new version — never an edit in
// place.
const PromptVersion = "v1"

//go:embed prompts/v1.md
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
