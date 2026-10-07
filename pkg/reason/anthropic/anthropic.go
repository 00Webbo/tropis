// Package anthropic is the default reasoning backend, calling Claude through
// the Anthropic Messages API.
//
// This is the only package in Tropis that imports the Anthropic SDK. Nothing
// here leaks out: the backend takes a reason.AnalysisInput and returns a
// schema.Verdict, like every other backend.
package anthropic

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"

	"github.com/00Webbo/tropis/pkg/reason"
	"github.com/00Webbo/tropis/pkg/schema"
)

// DefaultModel is the model used when none is configured.
const DefaultModel = "claude-opus-5"

// Config configures the Anthropic backend.
type Config struct {
	// Model is the Claude model ID. Empty means DefaultModel.
	Model string

	// APIKey authenticates. Empty falls back to the SDK's own credential
	// resolution (ANTHROPIC_API_KEY and friends).
	APIKey string

	// BaseURL overrides the API endpoint, for proxies and tests.
	BaseURL string

	// Effort is the output_config effort level: low, medium, high, xhigh or
	// max. Empty uses the API default.
	Effort string

	// MaxTokens bounds the response. Zero means 16000, which leaves room for
	// adaptive thinking ahead of a JSON verdict well under that size.
	MaxTokens int64

	// Timeout bounds one request. Zero means two minutes.
	Timeout time.Duration

	// DisableFallbacks turns off server-side refusal fallbacks. They are on
	// by default, so a request declined by one model's safety classifiers is
	// re-served by another rather than producing no verdict.
	DisableFallbacks bool
}

// Backend calls Claude.
type Backend struct {
	client sdk.Client
	cfg    Config
}

// New returns an Anthropic backend.
func New(cfg Config) *Backend {
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 16000
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 2 * time.Minute
	}
	opts := []option.RequestOption{option.WithRequestTimeout(cfg.Timeout)}
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &Backend{client: sdk.NewClient(opts...), cfg: cfg}
}

// Describe identifies the configured model. A verdict records the model that
// actually answered, which differs if a fallback served the request.
func (b *Backend) Describe() schema.BackendInfo {
	return schema.BackendInfo{Provider: "anthropic", Model: b.cfg.Model, PromptVersion: reason.PromptVersion, Effort: b.cfg.Effort}
}

// Analyze asks Claude for a verdict.
func (b *Backend) Analyze(ctx context.Context, in *reason.AnalysisInput) (schema.Verdict, error) {
	// UserMessage refuses any input that did not come through BuildInput,
	// so nothing unredacted can be sent.
	user, err := reason.UserMessage(in)
	if err != nil {
		return schema.Verdict{}, err
	}

	params := sdk.BetaMessageNewParams{
		Model:     sdk.Model(b.cfg.Model),
		MaxTokens: b.cfg.MaxTokens,
		// The system prompt is identical for every node and carries the
		// few-shot examples, so it is the cacheable prefix.
		System: []sdk.BetaTextBlockParam{{
			Text:         reason.SystemPrompt(),
			CacheControl: sdk.NewBetaCacheControlEphemeralParam(),
		}},
		Messages: []sdk.BetaMessageParam{
			sdk.NewBetaUserMessage(sdk.NewBetaTextBlock(user)),
		},
		Thinking: sdk.BetaThinkingConfigParamUnion{OfAdaptive: &sdk.BetaThinkingConfigAdaptiveParam{}},
		OutputConfig: sdk.BetaOutputConfigParam{
			Effort: sdk.BetaOutputConfigEffort(b.cfg.Effort),
			Format: sdk.BetaJSONOutputFormatParam{Schema: reason.OutputSchema()},
		},
	}
	if !b.cfg.DisableFallbacks {
		params.Betas = append(params.Betas, sdk.AnthropicBetaServerSideFallback2026_07_01)
		params.Fallbacks = sdk.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
	}

	resp, err := b.client.Beta.Messages.New(ctx, params)
	if err != nil {
		var apiErr *sdk.Error
		if errors.As(err, &apiErr) {
			return schema.Verdict{}, fmt.Errorf("anthropic: %d: %w", apiErr.StatusCode, err)
		}
		return schema.Verdict{}, fmt.Errorf("anthropic: %w", err)
	}

	switch resp.StopReason {
	case sdk.BetaStopReasonRefusal:
		return schema.Verdict{}, fmt.Errorf("%w: %s", reason.ErrRefused, resp.StopDetails.Explanation)
	case sdk.BetaStopReasonMaxTokens:
		return schema.Verdict{}, fmt.Errorf("%w: response hit max_tokens (%d) before completing",
			reason.ErrMalformedOutput, b.cfg.MaxTokens)
	}

	var text strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(sdk.BetaTextBlock); ok {
			text.WriteString(t.Text)
		}
	}

	info := b.Describe()
	if resp.Model != "" {
		info.Model = string(resp.Model)
	}
	return reason.Finalize([]byte(text.String()), in, info)
}
