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
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nathanwebb/tropis/pkg/reason"
	"github.com/nathanwebb/tropis/pkg/schema"
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

	// Timeout bounds one request. Zero means five minutes: local models on
	// modest hardware can be slow on a large input.
	Timeout time.Duration

	// HTTP overrides the client, for tests.
	HTTP *http.Client
}

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
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Minute
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: cfg.Timeout}
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Backend{cfg: cfg}, nil
}

// Describe identifies the backend.
func (b *Backend) Describe() schema.BackendInfo {
	return schema.BackendInfo{Provider: "local-" + string(b.cfg.API), Model: b.cfg.Model, PromptVersion: reason.PromptVersion}
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
	switch b.cfg.API {
	case APIOllama:
		text, err = b.ollama(ctx, messages)
	case APIOpenAI:
		text, err = b.openai(ctx, messages)
	}
	if err != nil {
		return schema.Verdict{}, err
	}
	return reason.Finalize([]byte(text), in, b.Describe())
}

func (b *Backend) ollama(ctx context.Context, messages []chatMessage) (string, error) {
	req := map[string]any{
		"model":    b.cfg.Model,
		"messages": messages,
		"stream":   false,
		"format":   reason.OutputSchema(),
		// Deterministic decoding: the eval measures the model, not the dice.
		"options": map[string]any{"temperature": 0},
	}
	var resp struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		DoneReason string `json:"done_reason"`
		Error      string `json:"error"`
	}
	if err := b.post(ctx, "/api/chat", req, &resp); err != nil {
		return "", err
	}
	if resp.Error != "" {
		return "", fmt.Errorf("local backend: %s", resp.Error)
	}
	if resp.DoneReason == "length" {
		return "", fmt.Errorf("%w: response truncated at the model's length limit", reason.ErrMalformedOutput)
	}
	return resp.Message.Content, nil
}

func (b *Backend) openai(ctx context.Context, messages []chatMessage) (string, error) {
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
	}
	if err := b.post(ctx, "/v1/chat/completions", req, &resp); err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("%w: server returned no choices", reason.ErrMalformedOutput)
	}
	c := resp.Choices[0]
	if c.Message.Refusal != "" {
		return "", fmt.Errorf("%w: %s", reason.ErrRefused, c.Message.Refusal)
	}
	if c.FinishReason == "length" {
		return "", fmt.Errorf("%w: response truncated at the model's length limit", reason.ErrMalformedOutput)
	}
	return c.Message.Content, nil
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
