package backend

import (
	"testing"

	"github.com/00Webbo/tropis/pkg/reason"
	"github.com/00Webbo/tropis/pkg/reason/anthropic"
	"github.com/00Webbo/tropis/pkg/reason/local"
	"github.com/00Webbo/tropis/pkg/reason/mock"
)

func TestNew(t *testing.T) {
	tests := []struct {
		cfg      Config
		provider string
		wantErr  bool
	}{
		{Config{}, "anthropic", false},
		{Config{Provider: Anthropic, Model: "claude-opus-5"}, "anthropic", false},
		{Config{Provider: Local, BaseURL: "http://ollama:11434", Model: "qwen3:32b"}, "local-ollama", false},
		{Config{Provider: Local, BaseURL: "http://vllm:8000", Model: "m", API: "openai"}, "local-openai", false},
		{Config{Provider: Mock}, "mock", false},
		{Config{Provider: Local}, "", true},
		{Config{Provider: Local, BaseURL: "http://ollama:11434", Model: "m", Think: "maybe"}, "", true},
		{Config{Provider: "gemini"}, "", true},
	}
	for _, tt := range tests {
		b, err := New(tt.cfg)
		if (err != nil) != tt.wantErr {
			t.Errorf("%+v: err = %v", tt.cfg, err)
			continue
		}
		if err == nil && b.Describe().Provider != tt.provider {
			t.Errorf("%+v: provider = %q, want %q", tt.cfg, b.Describe().Provider, tt.provider)
		}
		if err == nil && b.Describe().PromptVersion != reason.PromptVersion {
			t.Errorf("%+v: prompt version not recorded", tt.cfg)
		}
	}
}

func TestNewEscalating(t *testing.T) {
	b, err := New(Config{Provider: Anthropic, Model: "claude-sonnet-5", EscalationModel: "claude-opus-5"})
	if err != nil {
		t.Fatal(err)
	}
	e, ok := b.(*reason.Escalating)
	if !ok {
		t.Fatalf("got %T, want *reason.Escalating", b)
	}
	if e.Sweep.Describe().Model != "claude-sonnet-5" || e.Escalate.Describe().Model != "claude-opus-5" {
		t.Errorf("sweep %s, escalate %s", e.Sweep.Describe().Model, e.Escalate.Describe().Model)
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("TROPIS_BACKEND", "local")
	t.Setenv("TROPIS_MODEL", "qwen3:32b")
	t.Setenv("TROPIS_BASE_URL", "http://ollama:11434")
	t.Setenv("TROPIS_TIMEOUT", "90s")
	t.Setenv("TROPIS_ESCALATE_BELOW", "0.6")
	t.Setenv("TROPIS_LOCAL_THINK", "false")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "local" || cfg.Model != "qwen3:32b" || cfg.Timeout.Seconds() != 90 || cfg.EscalateBelow != 0.6 || cfg.Think != "false" {
		t.Errorf("cfg = %+v", cfg)
	}

	t.Setenv("TROPIS_TIMEOUT", "soon")
	if _, err := FromEnv(); err == nil {
		t.Error("bad duration should be an error")
	}
}

// Every backend satisfies the one interface.
var (
	_ reason.Backend = (*anthropic.Backend)(nil)
	_ reason.Backend = (*local.Backend)(nil)
	_ reason.Backend = (*mock.Backend)(nil)
	_ reason.Backend = (*reason.Escalating)(nil)
)
