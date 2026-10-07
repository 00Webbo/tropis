// Package local is the reasoning backend for self-hosted models, for
// clusters whose data must not leave the network.
//
// It speaks two wire protocols, since between them they cover nearly every
// self-hosted server:
//
//   - "ollama": Ollama's native /api/chat, with the output schema passed as
//     `format` for constrained decoding.
//   - "openai": the OpenAI-compatible /v1/chat/completions that vLLM,
//     llama.cpp's server, LM Studio and Ollama itself all expose, with the
//     schema passed as a json_schema response_format.
//
// With this backend, nothing leaves the cluster. It is a first-class
// configuration, not a degraded mode: same prompt, same input, same
// validation as the hosted backend.
package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/00Webbo/tropis/pkg/reason"
	"github.com/00Webbo/tropis/pkg/schema"
)

// API selects the wire protocol.
type API string

const (
	APIOllama API = "ollama"
	APIOpenAI API = "openai"
)

// Config configures the local backend.
type Config struct {
	// BaseURL is the server, e.g. http://ollama.tropis-system:11434 or
	// http://vllm:8000. Required.
	BaseURL string

	// Model is the model name as the server knows it. Required.
	Model string

	// API selects the protocol. Empty means ollama.
	API API

	// APIKey is sent as a bearer token when set; some OpenAI-compatible
	// servers require one.
	APIKey string

	// ContextTokens is the context window requested from Ollama, as
	// num_ctx. Zero means DefaultContextTokens. Ollama's own default is
	// smaller than a typical Tropis input, and Ollama truncates an input
	// that does not fit without reporting an error. OpenAI-compatible
	// servers fix the window when the model is loaded, so this is not sent
	// to them.
	ContextTokens int

	// Think controls a thinking model's hidden reasoning, sent to Ollama as
	// the top-level `think` field: "true", "false", or a level ("low",
	// "medium", "high") for models that take one. Empty sends nothing, so
	// the model's own default applies; many thinking models think by
	// default, and their thinking counts against the context window. The
	// openai protocol has no such field, so there it is the server's
	// setting, and New rejects a Think value.
	Think string

	// Timeout bounds one request. Zero means five minutes: local models on
	// modest hardware can be slow on a large input.
	Timeout time.Duration

	// HTTP overrides the client, for tests.
	HTTP *http.Client
}

// DefaultContextTokens is the default Ollama context window: room for the
// prompt, a node's redacted evidence and the verdict.
const DefaultContextTokens = 16384

// maxBytesPerToken bounds how many bytes of input one token can stand for.
// Tokenizers average three to four bytes per token on Tropis input, so a
// server reporting fewer than len(input)/maxBytesPerToken prompt tokens
// cannot have read the whole input.
const maxBytesPerToken = 6

// Backend calls a self-hosted model.
type Backend struct{ cfg Config }

// New returns a local backend.
func New(cfg Config) (*Backend, error) {
	if cfg.BaseURL == "" || cfg.Model == "" {
		return nil, fmt.Errorf("local backend: base URL and model are required")
	}
	if cfg.API == "" {
		cfg.API = APIOllama
	}
	if cfg.API != APIOllama && cfg.API != APIOpenAI {
		return nil, fmt.Errorf("local backend: unknown API %q (want %q or %q)", cfg.API, APIOllama, APIOpenAI)
	}
	if cfg.ContextTokens < 0 {
		return nil, fmt.Errorf("local backend: context tokens must not be negative, got %d", cfg.ContextTokens)
	}
	if cfg.ContextTokens == 0 {
		cfg.ContextTokens = DefaultContextTokens
	}
	if _, err := thinkValue(cfg.Think); err != nil {
		return nil, err
	}
	if cfg.Think != "" && cfg.API != APIOllama {
		// A setting that silently does nothing would also go unrecorded.
		return nil, fmt.Errorf("local backend: think is supported only on the %q protocol; on %q, thinking is the server's setting", APIOllama, cfg.API)
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Minute
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: cfg.Timeout}
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Backend{cfg: cfg}, nil
}

// Describe identifies the backend, including the thinking setting when one
// is sent, since it changes the results.
func (b *Backend) Describe() schema.BackendInfo {
	return schema.BackendInfo{Provider: "local-" + string(b.cfg.API), Model: b.cfg.Model, PromptVersion: reason.PromptVersion, Think: b.cfg.Think}
}

// thinkValue returns the JSON value of Ollama's `think` field for a Think
// setting: a boolean for true and false, the level string otherwise, and
// nil for empty, meaning the field is not sent.
func thinkValue(s string) (any, error) {
	switch s {
	case "":
		return nil, nil
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "low", "medium", "high":
		return s, nil
	}
	return nil, fmt.Errorf("local backend: unknown think setting %q (want true, false, low, medium or high, or empty for the model's default)", s)
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Analyze asks the local model for a verdict.
func (b *Backend) Analyze(ctx context.Context, in *reason.AnalysisInput) (schema.Verdict, error) {
	user, err := reason.UserMessage(in)
	if err != nil {
		return schema.Verdict{}, err
	}
	messages := []chatMessage{
		{Role: "system", Content: reason.SystemPrompt()},
		{Role: "user", Content: user},
	}

	var text string
	var promptTokens int
	switch b.cfg.API {
	case APIOllama:
		text, promptTokens, err = b.ollama(ctx, messages)
	case APIOpenAI:
		text, promptTokens, err = b.openai(ctx, messages)
	}
	if err != nil {
		return schema.Verdict{}, err
	}
	if err := checkNotTruncated(messages, promptTokens); err != nil {
		return schema.Verdict{}, err
	}
	return reason.Finalize([]byte(text), in, b.Describe())
}

// ErrInputTruncated means the server read only part of the input. A verdict
// from part of the evidence would look exactly like one from all of it, so
// it is an error.
var ErrInputTruncated = errors.New("local backend: the server truncated the input")

// checkNotTruncated rejects a response whose reported prompt token count is
// too small to cover the input. Ollama, on both of its APIs, drops the part
// of an input that does not fit its context window and answers anyway. A
// server that reports no count cannot be checked.
func checkNotTruncated(messages []chatMessage, promptTokens int) error {
	if promptTokens <= 0 {
		return nil
	}
	n := 0
	for _, m := range messages {
		n += len(m.Content)
	}
	if promptTokens*maxBytesPerToken < n {
		return fmt.Errorf("%w: it read %d tokens of a %d-byte input; raise its context window (TROPIS_CONTEXT_TOKENS, or the server's own setting)",
			ErrInputTruncated, promptTokens, n)
	}
	return nil
}

func (b *Backend) ollama(ctx context.Context, messages []chatMessage) (string, int, error) {
	req := map[string]any{
		"model":    b.cfg.Model,
		"messages": messages,
		"stream":   false,
		"format":   reason.OutputSchema(),
		// Deterministic decoding: the eval measures the model, not the dice.
		"options": map[string]any{"temperature": 0, "num_ctx": b.cfg.ContextTokens},
	}
	if think, _ := thinkValue(b.cfg.Think); think != nil {
		req["think"] = think
	}
	var resp struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		DoneReason      string `json:"done_reason"`
		Error           string `json:"error"`
		PromptEvalCount int    `json:"prompt_eval_count"`
	}
	if err := b.post(ctx, "/api/chat", req, &resp); err != nil {
		return "", 0, err
	}
	if resp.Error != "" {
		return "", 0, fmt.Errorf("local backend: %s", resp.Error)
	}
	if resp.DoneReason == "length" {
		return "", 0, fmt.Errorf("%w: response truncated at the model's length limit", reason.ErrMalformedOutput)
	}
	return resp.Message.Content, resp.PromptEvalCount, nil
}

func (b *Backend) openai(ctx context.Context, messages []chatMessage) (string, int, error) {
	req := map[string]any{
		"model":       b.cfg.Model,
		"messages":    messages,
		"temperature": 0,
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "tropis_verdict",
				"strict": true,
				"schema": reason.OutputSchema(),
			},
		},
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := b.post(ctx, "/v1/chat/completions", req, &resp); err != nil {
		return "", 0, err
	}
	if len(resp.Choices) == 0 {
		return "", 0, fmt.Errorf("%w: server returned no choices", reason.ErrMalformedOutput)
	}
	c := resp.Choices[0]
	if c.Message.Refusal != "" {
		return "", 0, fmt.Errorf("%w: %s", reason.ErrRefused, c.Message.Refusal)
	}
	if c.FinishReason == "length" {
		return "", 0, fmt.Errorf("%w: response truncated at the model's length limit", reason.ErrMalformedOutput)
	}
	return c.Message.Content, resp.Usage.PromptTokens, nil
}

func (b *Backend) post(ctx context.Context, path string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if b.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.cfg.APIKey)
	}
	resp, err := b.cfg.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("local backend: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("local backend: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("local backend: %s: %s", resp.Status, truncate(string(data), 300))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("local backend: decode response: %w", err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
