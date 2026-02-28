// Package version holds the build version string.
//
// The Version variable is injected at build time via ldflags:
//
//	go build -ldflags "-X github.com/vector76/raymond/internal/version.Version=v1.2.3" ./cmd/raymond
//
// Without that flag the binary reports "dev", which is the expected value for
// local development builds.
package version

// Version is the program version. Set at build time; defaults to "dev".
var Version = "dev"
