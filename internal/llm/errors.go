// Package llm — errors: every adapter maps its HTTP/API error shapes onto these
// six kinds so callers have a single errors.As path. Error() never embeds the
// provider API key.
package llm

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// AuthenticationError indicates invalid or revoked credentials (HTTP 401/403).
type AuthenticationError struct {
	StatusCode int
	Message    string
	Cause      error
}

func (e *AuthenticationError) Error() string {
	return describeError("authentication error", e.StatusCode, e.Message)
}
func (e *AuthenticationError) Unwrap() error { return e.Cause }

// InvalidRequestError indicates a malformed or rejected request (400/404/422).
type InvalidRequestError struct {
	StatusCode int
	Message    string
	Cause      error
}

func (e *InvalidRequestError) Error() string {
	return describeError("invalid request", e.StatusCode, e.Message)
}
func (e *InvalidRequestError) Unwrap() error { return e.Cause }

// RateLimitError indicates a 429 rate-limit condition.
type RateLimitError struct {
	StatusCode int
	Message    string
	Cause      error
}

func (e *RateLimitError) Error() string {
	return describeError("rate limit", e.StatusCode, e.Message)
}
func (e *RateLimitError) Unwrap() error { return e.Cause }

// ServerError indicates a 5xx service failure or provider overload (including
// overloaded_error / response.failed from the streaming APIs).
type ServerError struct {
	StatusCode int
	Message    string
	Cause      error
}

func (e *ServerError) Error() string {
	return describeError("server error", e.StatusCode, e.Message)
}
func (e *ServerError) Unwrap() error { return e.Cause }

// NetworkError indicates a transport-level failure with no HTTP status (e.g.
// connection refused, DNS, timeout).
type NetworkError struct {
	Message string
	Cause   error
}

func (e *NetworkError) Error() string {
	switch {
	case e.Message != "":
		return "network error: " + e.Message
	case e.Cause != nil:
		return fmt.Sprintf("network error: %v", e.Cause)
	default:
		return "network error"
	}
}
func (e *NetworkError) Unwrap() error { return e.Cause }

// ContextTooLongError indicates the request exceeded the model's context
// window. Providers report it as 413 or as 400 + invalid_request_error with a
// "prompt is too long" message, which is why it must classify before the 400
// fallback.
type ContextTooLongError struct {
	StatusCode int
	Message    string
	Cause      error
}

func (e *ContextTooLongError) Error() string {
	return describeError("context too long", e.StatusCode, e.Message)
}
func (e *ContextTooLongError) Unwrap() error { return e.Cause }

func IsContextTooLong(err error) bool {
	_, ok := errors.AsType[*ContextTooLongError](err)
	return ok
}

// describeError is the spelling the four status-carrying kinds share.
func describeError(kind string, status int, message string) string {
	switch {
	case message != "" && status != 0:
		return fmt.Sprintf("%s %d: %s", kind, status, message)
	case message != "":
		return kind + ": " + message
	default:
		return fmt.Sprintf("%s %d", kind, status)
	}
}

// classify maps a status and API-reported type/code/message onto one of the
// six kinds; specific matches are tried before the status-range fallbacks.
func classify(status int, apiType, apiCode, message string, cause error) error {
	switch {
	case status == 401 || status == 403 || apiType == "authentication_error" || apiCode == "invalid_api_key":
		return &AuthenticationError{StatusCode: status, Message: message, Cause: cause}
	case status == 429 || apiType == "rate_limit_error":
		return &RateLimitError{StatusCode: status, Message: message, Cause: cause}
	case status >= 500 || apiType == "overloaded_error" || apiCode == "server_error":
		return &ServerError{StatusCode: status, Message: message, Cause: cause}
	case status == 413 || apiCode == "context_length_exceeded" || contextTooLongHint(message):
		return &ContextTooLongError{StatusCode: status, Message: message, Cause: cause}
	case status == 400 || status == 404 || status == 422 || apiType == "invalid_request_error":
		return &InvalidRequestError{StatusCode: status, Message: message, Cause: cause}
	case status == 0:
		return &NetworkError{Message: message, Cause: cause}
	case status >= 400:
		return &InvalidRequestError{StatusCode: status, Message: message, Cause: cause}
	default:
		return &ServerError{StatusCode: status, Message: message, Cause: cause}
	}
}

// Streaming errors arrive with no status; the message is the only signal.
func contextTooLongHint(message string) bool {
	s := strings.ToLower(message)
	return strings.Contains(s, "prompt is too long") ||
		strings.Contains(s, "context_length_exceeded") ||
		strings.Contains(s, "maximum context length") ||
		strings.Contains(s, "context length is")
}

// isNetworkHint reports whether err is plausibly a transport-level failure that
// carries no HTTP status.
func isNetworkHint(err error) bool {
	if err == nil {
		return false
	}
	_, ok := errors.AsType[net.Error](err)
	return ok
}

// statusHint recovers an HTTP status from an error's text when the SDK
// surfaced none: 401/403 for auth, 429 for rate limit, 0 otherwise. Both
// adapters share this sniff — the SDKs do not always turn the wire status
// into a typed error on streaming paths.
func statusHint(message string) int {
	s := strings.ToLower(message)
	switch {
	case strings.Contains(s, "401") || strings.Contains(s, "403") || strings.Contains(s, "authentication"):
		return 401
	case strings.Contains(s, "429") || strings.Contains(s, "rate limit"):
		return 429
	}
	return 0
}
