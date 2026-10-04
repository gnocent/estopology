// Package buildinfo exposes the version stamped into the binary at build time.
package buildinfo

import (
	"fmt"
	"runtime"
)

// Set with -ldflags "-X estopo/internal/buildinfo.Version=... -X ...Commit=... -X ...Date=..."
// (see build-release.sh). Development builds keep the defaults.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// String returns a one-line human-readable description of the build.
func String(program string) string {
	return fmt.Sprintf("%s %s (commit %s, built %s, %s %s/%s)",
		program, Version, Commit, Date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
