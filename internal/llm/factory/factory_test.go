package factory

import (
	"strings"
	"testing"

	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/llm/anthropic"
	"github.com/FrexCheat/aether-code/internal/llm/openai"
)

func base(p config.Protocol) config.Provider {
	return config.Provider{Name: "x", Protocol: p, Model: "m", BaseURL: "https://example.com", APIKey: "k"}
}

func TestNewAnthropic(t *testing.T) {
	p, err := New(base(config.ProtocolAnthropic))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*anthropic.Client); !ok {
		t.Errorf("got %T, want *anthropic.Client", p)
	}
}

func TestNewOpenAI(t *testing.T) {
	p, err := New(base(config.ProtocolOpenAI))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*openai.Client); !ok {
		t.Errorf("got %T, want *openai.Client", p)
	}
}

func TestNewUnknown(t *testing.T) {
	_, err := New(base(config.Protocol("foo")))
	if err == nil || !strings.Contains(err.Error(), "unsupported protocol") {
		t.Errorf("err = %v", err)
	}
}
