package llm

import (
	"errors"
	"strings"
	"testing"

	"github.com/FrexCheat/aether-code/internal/config"
)

func base(p config.Protocol) config.Provider {
	return config.Provider{Name: "x", Protocol: p, Model: "m", BaseURL: "https://example.com", APIKey: "k"}
}

func TestNewClientAnthropic(t *testing.T) {
	p, err := NewClient(base(config.ProtocolAnthropic), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*anthropicClient); !ok {
		t.Errorf("got %T, want *anthropicClient", p)
	}
}

func TestNewClientOpenAI(t *testing.T) {
	p, err := NewClient(base(config.ProtocolOpenAI), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*openAIClient); !ok {
		t.Errorf("got %T, want *openAIClient", p)
	}
}

func TestNewClientUnknown_IsInvalidRequest(t *testing.T) {
	_, err := NewClient(base(config.Protocol("foo")), nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported protocol") {
		t.Fatalf("err = %v", err)
	}
	var ire *InvalidRequestError
	if !errors.As(err, &ire) {
		t.Errorf("want InvalidRequestError, got %T", err)
	}
}
