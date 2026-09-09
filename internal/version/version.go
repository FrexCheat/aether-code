// Package version holds the build version of AetherCode.
package version

// Version is the application version shown in the startup banner.
//
// Override at build time with:
//
//	go build -ldflags "-X github.com/FrexCheat/aether-code/internal/version.Version=x.y.z" ./cmd/aethercode
var Version = "0.1.0-dev"
