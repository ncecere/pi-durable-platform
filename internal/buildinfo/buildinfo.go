// Package buildinfo exposes version metadata injected at link time.
package buildinfo

import "fmt"

// Set with -ldflags "-X github.com/ncecere/pi-durable-platform/internal/buildinfo.Version=..."
var (
	Version = "0.1.2"
	Commit  = "unknown"
)

func String() string { return fmt.Sprintf("pi-durable-platform %s (%s)", Version, Commit) }
