package llm

import (
	"errors"
	"testing"
)

// errorKind labels a classified error with a short name for table assertions.
func errorKind(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.As(err, new(*AuthenticationError)):
		return "auth"
	case errors.As(err, new(*RateLimitError)):
		return "rate"
	case errors.As(err, new(*ServerError)):
		return "server"
	case errors.As(err, new(*ContextTooLongError)):
		return "context"
	case errors.As(err, new(*InvalidRequestError)):
		return "invalid"
	case errors.As(err, new(*NetworkError)):
		return "network"
	default:
		return "unknown"
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		apiType string
		apiCode string
		message string
		want    string
	}{
		{"401", 401, "", "", "", "auth"},
		{"403", 403, "", "", "", "auth"},
		{"authentication type", 0, "authentication_error", "", "", "auth"},
		{"invalid api key code", 0, "", "invalid_api_key", "", "auth"},
		{"429", 429, "", "", "", "rate"},
		{"rate limit type", 0, "rate_limit_error", "", "", "rate"},
		{"500", 500, "", "", "", "server"},
		{"529 overload", 529, "", "", "", "server"},
		{"overloaded type", 0, "overloaded_error", "", "", "server"},
		{"context status", 413, "", "", "", "context"},
		{"context code", 400, "", "context_length_exceeded", "", "context"},
		{"prompt too long", 400, "", "", "prompt is too long: 200000 > 128000", "context"},
		{"maximum context length", 400, "", "", "This model's maximum context length is 128000 tokens", "context"},
		{"400 fallback", 400, "", "", "bad request", "invalid"},
		{"404", 404, "", "", "not found", "invalid"},
		{"422", 422, "", "", "unprocessable", "invalid"},
		{"no status", 0, "", "", "stream died", "network"},
		{"other 4xx", 418, "", "", "teapot", "invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classify(tt.status, tt.apiType, tt.apiCode, tt.message, errors.New("cause"))
			if got := errorKind(err); got != tt.want {
				t.Errorf("classify(%d, %q, %q, %q) → %s, want %s", tt.status, tt.apiType, tt.apiCode, tt.message, got, tt.want)
			}
		})
	}
}

func TestStatusHint(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    int
	}{
		{"401", "http 401 unauthorized", 401},
		{"403", "status 403", 401},
		{"authentication word", "authentication failed", 401},
		{"429", "http 429", 429},
		{"rate limit phrase", "Rate Limit exceeded", 429},
		{"no hint", "something odd happened", 0},
		{"unrelated digits", "processed 301 items", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusHint(tt.message); got != tt.want {
				t.Errorf("statusHint(%q) = %d, want %d", tt.message, got, tt.want)
			}
		})
	}
}

func TestIsContextTooLong(t *testing.T) {
	tooLong := classify(413, "", "", "prompt is too long", errors.New("cause"))
	if !IsContextTooLong(tooLong) {
		t.Error("IsContextTooLong(413) = false, want true")
	}
	if IsContextTooLong(classify(429, "", "", "", errors.New("cause"))) {
		t.Error("IsContextTooLong(429) = true, want false")
	}
}
