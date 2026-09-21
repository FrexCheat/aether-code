package mcp

import (
	"net/url"
	"os"
	"strings"

	"github.com/FrexCheat/aether-code/internal/config"
)

// LoadingMode decides what enters the tools list at settle time.
type LoadingMode string

const (
	// LoadingEager puts every tool's full schema on the wire.
	LoadingEager LoadingMode = "eager"
	// LoadingNative keeps every tool on the wire under the provider's
	// deferred-loading flag; ToolSearch surfaces schemas via tool_reference.
	LoadingNative LoadingMode = "native"
	// LoadingDispatch keeps MCP tools off the wire entirely; the model finds
	// them via ToolSearch and calls through McpCall.
	LoadingDispatch LoadingMode = "dispatch"
)

const (
	// deferredToolThreshold is the tool count past which schemas stop
	// fitting the request cheaply and deferral starts. The decision is
	// session-wide: below it everything is eager, at or above it nothing is.
	deferredToolThreshold = 20
	// officialAnthropicHost is the only endpoint that honors defer_loading
	// and tool_reference; a proxy would reject the beta header instead.
	officialAnthropicHost = "api.anthropic.com"
	// envLoadingOverride forces a mode. It doubles as the opt-in for a proxy
	// known to forward the tool-search beta to the real endpoint.
	envLoadingOverride = "AETHERCODE_MCP_LOADING"
)

// decideMode picks the session's loading mode: below the threshold eager,
// above it native only on the official Anthropic endpoint, dispatch
// everywhere else. The override wins outright.
func decideMode(protocol config.Protocol, baseURL string, toolCount int) LoadingMode {
	switch LoadingMode(os.Getenv(envLoadingOverride)) {
	case LoadingEager, LoadingNative, LoadingDispatch:
		return LoadingMode(os.Getenv(envLoadingOverride))
	}
	if toolCount < deferredToolThreshold {
		return LoadingEager
	}
	if protocol == config.ProtocolAnthropic && officialAnthropicEndpoint(baseURL) {
		return LoadingNative
	}
	return LoadingDispatch
}

func officialAnthropicEndpoint(baseURL string) bool {
	u, err := url.Parse(baseURL)
	return err == nil && strings.EqualFold(u.Hostname(), officialAnthropicHost)
}
