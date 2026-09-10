// Package llm — errors: unified classification over all provider backends.
//
// Each provider adapter maps HTTP and API error shapes onto five kinds so
// callers have a single errors.As path. Error() intentionally never embeds the
// provider API key. ClassifyNetwork covers transport failures that carry no
// status code.
package llm

import (
	"errors"
	"fmt"
	"net"
)

// AuthenticationError indicates invalid or revoked credentials (HTTP 401/403).
type AuthenticationError struct {
	StatusCode int
	Message    string
	Cause      error
}

func (e *AuthenticationError) Error() string {
	if e.Message != "" && e.StatusCode != 0 {
		return fmt.Sprintf("authentication error %d: %s", e.StatusCode, e.Message)
	}
	if e.Message != "" {
		return fmt.Sprintf("authentication error: %s", e.Message)
	}
	return fmt.Sprintf("authentication error %d", e.StatusCode)
}
func (e *AuthenticationError) Unwrap() error { return e.Cause }

// InvalidRequestError indicates a malformed or rejected request (400/404/422).
type InvalidRequestError struct {
	StatusCode int
	Message    string
	Cause      error
}

func (e *InvalidRequestError) Error() string {
	if e.Message != "" && e.StatusCode != 0 {
		return fmt.Sprintf("invalid request %d: %s", e.StatusCode, e.Message)
	}
	if e.Message != "" {
		return fmt.Sprintf("invalid request: %s", e.Message)
	}
	return fmt.Sprintf("invalid request %d", e.StatusCode)
}
func (e *InvalidRequestError) Unwrap() error { return e.Cause }

// RateLimitError indicates a 429 rate-limit condition.
type RateLimitError struct {
	StatusCode int
	Message    string
	Cause      error
}

func (e *RateLimitError) Error() string {
	if e.Message != "" && e.StatusCode != 0 {
		return fmt.Sprintf("rate limit %d: %s", e.StatusCode, e.Message)
	}
	if e.Message != "" {
		return fmt.Sprintf("rate limit: %s", e.Message)
	}
	return fmt.Sprintf("rate limit %d", e.StatusCode)
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
	if e.Message != "" && e.StatusCode != 0 {
		return fmt.Sprintf("server error %d: %s", e.StatusCode, e.Message)
	}
	if e.Message != "" {
		return fmt.Sprintf("server error: %s", e.Message)
	}
	return fmt.Sprintf("server error %d", e.StatusCode)
}
func (e *ServerError) Unwrap() error { return e.Cause }

// NetworkError indicates a transport-level failure with no HTTP status (e.g.
// connection refused, DNS, timeout).
type NetworkError struct {
	Message string
	Cause   error
}

func (e *NetworkError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("network error: %s", e.Message)
	}
	if e.Cause != nil {
		return fmt.Sprintf("network error: %v", e.Cause)
	}
	return "network error"
}
func (e *NetworkError) Unwrap() error { return e.Cause }

// classify maps a status and API-reported type/code/message onto one of the
// five kinds. Callers should have already stripped any API key from message.
func classify(status int, apiType, apiCode, message string, cause error) error {
	if status == 401 || status == 403 || apiType == "authentication_error" || apiCode == "invalid_api_key" {
		return &AuthenticationError{StatusCode: status, Message: message, Cause: cause}
	}
	if status == 429 || apiType == "rate_limit_error" {
		return &RateLimitError{StatusCode: status, Message: message, Cause: cause}
	}
	if status >= 500 && status < 600 || apiType == "overloaded_error" || apiCode == "server_error" {
		return &ServerError{StatusCode: status, Message: message, Cause: cause}
	}
	if status == 400 || status == 404 || status == 422 || apiType == "invalid_request_error" {
		return &InvalidRequestError{StatusCode: status, Message: message, Cause: cause}
	}
	if status == 0 {
		return &NetworkError{Message: message, Cause: cause}
	}
	if status >= 500 {
		return &ServerError{StatusCode: status, Message: message, Cause: cause}
	}
	if status >= 400 {
		return &InvalidRequestError{StatusCode: status, Message: message, Cause: cause}
	}
	return &ServerError{StatusCode: status, Message: message, Cause: cause}
}

// isNetworkHint reports whether err is plausibly a transport-level failure
// (so callers can map stray connection errors to NetworkError without a status).
func isNetworkHint(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	return false
}

var _ = isNetworkHint // keep helper reachable for adapters; drop the underscore once used

// NewAuthenticationError, NewInvalidRequestError, NewRateLimitError,
// NewServerError, NewNetworkError are thin constructors for single-line
// construction in factories and tests.
func NewAuthenticationError(status int, message string, cause error) error {
	return &AuthenticationError{StatusCode: status, Message: message, Cause: cause}
}
func NewInvalidRequestError(status int, message string, cause error) error {
	return &InvalidRequestError{StatusCode: status, Message: message, Cause: cause}
}
func NewRateLimitError(status int, message string, cause error) error {
	return &RateLimitError{StatusCode: status, Message: message, Cause: cause}
}
func NewServerError(status int, message string, cause error) error {
	return &ServerError{StatusCode: status, Message: message, Cause: cause}
}
func NewNetworkError(message string, cause error) error {
	return &NetworkError{Message: message, Cause: cause}
}
