// Package backend constructs a reasoning backend from configuration.
//
// It is the only package that knows every provider. Adding or switching a
// backend changes this package and the provider's own, and nothing outside
// pkg/reason.
package backend

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/nathanwebb/tropis/pkg/reason"
	"github.com/nathanwebb/tropis/pkg/reason/anthropic"
	"github.com/nathanwebb/tropis/pkg/reason/local"
	"github.com/nathanwebb/tropis/pkg/reason/mock"
)

// Providers.
const (
	Anthropic = "anthropic"
	Local     = "local"
	Mock      = "mock"
)

// Config selects and configures a backend.
type Config struct {
	// Provider is anthropic (default), local or mock.
	Provider string

	// Model is the model name. Empty uses the provider default where one
	// exists; local requires it.
	Model string

	// BaseURL is the endpoint. Required for local; optional for anthropic.
	BaseURL string

	// API is the local wire protocol: ollama (default) or openai.
	API string

	// APIKey authenticates. For anthropic, empty defers to the SDK's own
	// credential resolution.
	APIKey string

	// Effort is the anthropic effort level.
	Effort string

	// Timeout bounds one request.
	Timeout time.Duration

	// DisableFallbacks turns off anthropic server-side refusal fallbacks.
	DisableFallbacks bool

	// EscalationModel, when set, makes Model a cheap sweep model and escalates
	// to this model on the same provider when the sweep claims causality or
	// is unsure. Off by default: a single call to the default model is the
	// accurate configuration, and trading accuracy for cost is the
	// operator's decision.
	EscalationModel string

	// EscalateBelow is the sweep confidence below which to escalate.
	EscalateBelow float64
}

// New builds the configured backend.
func New(cfg Config) (reason.Backend, error) {
	primary, err := single(cfg, cfg.Model)
	if err != nil {
		return nil, err
	}
	if cfg.EscalationModel == "" {
		return primary, nil
	}
	escalate, err := single(cfg, cfg.EscalationModel)
	if err != nil {
		return nil, err
	}
	return &reason.Escalating{Sweep: primary, Escalate: escalate, Below: cfg.EscalateBelow}, nil
}

func single(cfg Config, model string) (reason.Backend, error) {
	switch cfg.Provider {
	case "", Anthropic:
		return anthropic.New(anthropic.Config{
			Model:            model,
			APIKey:           cfg.APIKey,
			BaseURL:          cfg.BaseURL,
			Effort:           cfg.Effort,
			Timeout:          cfg.Timeout,
			DisableFallbacks: cfg.DisableFallbacks,
		}), nil
	case Local:
		return local.New(local.Config{
			BaseURL: cfg.BaseURL,
			Model:   model,
			API:     local.API(cfg.API),
			APIKey:  cfg.APIKey,
			Timeout: cfg.Timeout,
		})
	case Mock:
		return mock.New(), nil
	default:
		return nil, fmt.Errorf("unknown backend provider %q (want %s, %s or %s)", cfg.Provider, Anthropic, Local, Mock)
	}
}

// FromEnv reads a Config from TROPIS_* environment variables:
//
//	TROPIS_BACKEND             anthropic | local | mock
//	TROPIS_MODEL               model name
//	TROPIS_BASE_URL            endpoint
//	TROPIS_LOCAL_API           ollama | openai
//	TROPIS_API_KEY             API key (anthropic also honours ANTHROPIC_API_KEY)
//	TROPIS_EFFORT              anthropic effort level
//	TROPIS_TIMEOUT             request timeout, e.g. 2m
//	TROPIS_DISABLE_FALLBACKS   true to disable anthropic refusal fallbacks
//	TROPIS_ESCALATION_MODEL    enables sweep-then-escalate
//	TROPIS_ESCALATE_BELOW      sweep confidence threshold, e.g. 0.7
func FromEnv() (Config, error) {
	cfg := Config{
		Provider:        os.Getenv("TROPIS_BACKEND"),
		Model:           os.Getenv("TROPIS_MODEL"),
		BaseURL:         os.Getenv("TROPIS_BASE_URL"),
		API:             os.Getenv("TROPIS_LOCAL_API"),
		APIKey:          os.Getenv("TROPIS_API_KEY"),
		Effort:          os.Getenv("TROPIS_EFFORT"),
		EscalationModel: os.Getenv("TROPIS_ESCALATION_MODEL"),
	}
	if s := os.Getenv("TROPIS_TIMEOUT"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return cfg, fmt.Errorf("TROPIS_TIMEOUT: %w", err)
		}
		cfg.Timeout = d
	}
	if s := os.Getenv("TROPIS_DISABLE_FALLBACKS"); s != "" {
		v, err := strconv.ParseBool(s)
		if err != nil {
			return cfg, fmt.Errorf("TROPIS_DISABLE_FALLBACKS: %w", err)
		}
		cfg.DisableFallbacks = v
	}
	if s := os.Getenv("TROPIS_ESCALATE_BELOW"); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return cfg, fmt.Errorf("TROPIS_ESCALATE_BELOW: %w", err)
		}
		cfg.EscalateBelow = v
	}
	return cfg, nil
}
