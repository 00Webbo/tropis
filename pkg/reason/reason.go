// Package reason is the reasoning layer: it turns one node's captured host
// and Kubernetes state into a Verdict by asking a model whether one is
// causing the other.
//
// The package is provider-neutral. Backend is the interface every model
// provider implements; provider-specific code and types live in its
// subpackages (anthropic, local, mock) and nowhere else. Prompt construction,
// output parsing and validation live here, so every backend shares them and a
// backend is a thin transport.
//
// Two guarantees are enforced by construction rather than convention:
//
//   - Redaction runs before any model call. An AnalysisInput can only be
//     produced by BuildInput, which redacts, and its content is reachable only
//     through methods that return the redacted form. A backend cannot be
//     handed unredacted text.
//   - Malformed model output is an error, never a verdict. Finalize parses
//     strictly, rejects evidence citing anything not in the input, and runs
//     schema.Verdict.Validate before returning.
package reason

import (
	"context"
	"errors"

	"github.com/nathanwebb/tropis/pkg/schema"
)

// Backend is a model provider able to produce a verdict.
type Backend interface {
	// Analyze produces a verdict for one node. It returns an error, never a
	// fabricated or partially filled verdict, when the model's output cannot
	// be parsed or fails validation.
	Analyze(ctx context.Context, input *AnalysisInput) (schema.Verdict, error)

	// Describe identifies the backend, model and prompt version.
	Describe() schema.BackendInfo
}

var (
	// ErrUnbuiltInput is returned when an AnalysisInput did not come from
	// BuildInput and therefore has not been redacted.
	ErrUnbuiltInput = errors.New("analysis input was not built by reason.BuildInput")

	// ErrMalformedOutput wraps every failure to turn model output into a
	// valid verdict. The eval harness counts these separately from wrong
	// answers.
	ErrMalformedOutput = errors.New("malformed model output")

	// ErrRefused is returned when the provider declined to answer.
	ErrRefused = errors.New("model declined to answer")
)

// AgentVersion is stamped on every verdict. Overridden at build time with
// -ldflags "-X github.com/nathanwebb/tropis/pkg/reason.AgentVersion=v0.1.0".
var AgentVersion = "dev"
