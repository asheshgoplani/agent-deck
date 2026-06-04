// Package buildinfo resolves the git commit the binary was built from.
//
// The commit is primarily injected at build time via -ldflags
// "-X main.Commit=..." (see the Makefile and .goreleaser.yaml). When that
// override is absent — e.g. a plain `go build` / `go run` / `go install`
// without the wrapper flags — it falls back to the VCS revision that the Go
// toolchain automatically stamps into the binary via debug.ReadBuildInfo().
package buildinfo

import (
	"runtime/debug"
	"strings"
)

// shortLen is the number of leading hex characters shown for a commit hash.
// Matches the width used by the Makefile (git rev-parse --short=8) and the
// goreleaser template so the displayed hash is stable across build paths.
const shortLen = 8

// Commit returns the short commit hash the binary was built from.
//
// override is the value injected via -ldflags -X main.Commit=...; when
// non-empty it wins (already short and authoritative for release builds).
// Otherwise the embedded VCS revision is used, truncated to shortLen. Returns
// "unknown" when no commit information is available (e.g. building outside a
// git checkout or with -buildvcs=false).
func Commit(override string) string {
	if c := strings.TrimSpace(override); c != "" {
		return c
	}
	if rev := vcsRevision(); rev != "" {
		if len(rev) > shortLen {
			rev = rev[:shortLen]
		}
		return rev
	}
	return "unknown"
}

// vcsRevision reads the git revision the Go toolchain embedded at build time.
// Returns "" when build info or the vcs.revision setting is unavailable.
func vcsRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return ""
}
