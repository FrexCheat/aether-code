package llm

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

type AuthenticationError struct {
	StatusCode int
	Message    string
	Cause      error
}

func (e *AuthenticationError) Error() string {
	return describeError("authentication error", e.StatusCode, e.Message)
}
func (e *AuthenticationError) Unwrap() error { return e.Cause }

type InvalidRequestError struct {
	StatusCode int
	Message    string
	Cause      error
}

func (e *InvalidRequestError) Error() string {
	return describeError("invalid request", e.StatusCode, e.Message)
}
func (e *InvalidRequestError) Unwrap() error { return e.Cause }

type RateLimitError struct {
	StatusCode int
	Message    string
	Cause      error
}

func (e *RateLimitError) Error() string {
	return describeError("rate limit", e.StatusCode, e.Message)
}
func (e *RateLimitError) Unwrap() error { return e.Cause }

type ServerError struct {
	StatusCode int
	Message    string
	Cause      error
}

func (e *ServerError) Error() string {
	return describeError("server error", e.StatusCode, e.Message)
}
func (e *ServerError) Unwrap() error { return e.Cause }

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

func contextTooLongHint(message string) bool {
	s := strings.ToLower(message)
	return strings.Contains(s, "prompt is too long") ||
		strings.Contains(s, "context_length_exceeded") ||
		strings.Contains(s, "maximum context length") ||
		strings.Contains(s, "context length is")
}

func isNetworkHint(err error) bool {
	if err == nil {
		return false
	}
	_, ok := errors.AsType[net.Error](err)
	return ok
}

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
