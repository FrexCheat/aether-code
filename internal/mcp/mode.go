package mcp

import (
	"net/url"
	"os"
	"strings"

	"github.com/FrexCheat/aether-code/internal/config"
)

type LoadingMode string

const (
	LoadingEager    LoadingMode = "eager"
	LoadingNative   LoadingMode = "native"
	LoadingDispatch LoadingMode = "dispatch"
)

const (
	deferredToolThreshold = 20
	officialAnthropicHost = "api.anthropic.com"
	envLoadingOverride    = "AETHERCODE_MCP_LOADING"
)

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
