package tmux

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// VersionProbe returns the raw output of `tmux -V`, e.g. "tmux 3.6a".
type VersionProbe func() (string, error)

var versionWarningOnce sync.Once

// WarnIfVulnerableTmux prints a one-time stderr warning when the host tmux
// is known-vulnerable to the CONTROL_SHOULD_NOTIFY_CLIENT NULL-deref
// (tmux #4980, stability row S14, issue #737). No-op on non-macOS, no-op
// when AGENTDECK_SUPPRESS_TMUX_WARNING=1/true. Safe to call from main()
// unconditionally; gated by sync.Once so repeat invocations are free.
func WarnIfVulnerableTmux() {
	checkAndWarnTmuxVersion(defaultTmuxVersionProbe, os.Stderr, runtime.GOOS, os.Getenv("AGENTDECK_SUPPRESS_TMUX_WARNING"))
}

// ResetVersionWarningOnceForTest clears the sync.Once so tests can exercise
// the repeat-call path. Not for production use.
func ResetVersionWarningOnceForTest() {
	versionWarningOnce = sync.Once{}
}

func checkAndWarnTmuxVersion(probe VersionProbe, w io.Writer, goos, suppress string) {
	versionWarningOnce.Do(func() {
		doCheckAndWarnTmuxVersion(probe, w, goos, suppress)
	})
}

func doCheckAndWarnTmuxVersion(probe VersionProbe, w io.Writer, goos, suppress string) {
	if goos != "darwin" {
		return
	}
	if suppress == "1" || suppress == "true" {
		return
	}
	raw, err := probe()
	if err != nil {
		return
	}
	ver := parseTmuxVersion(raw)
	if ver == "" || !isVulnerableTmuxVersion(ver) {
		return
	}
	fmt.Fprintf(w,
		"agent-deck: heads-up — your tmux (%s) has an unfixed control-mode NULL deref (tmux #4980) that can crash the server. "+
			"We apply a SIGTERM+grace mitigation in v1.7.68+, but a patched tmux from Homebrew will close the window entirely. "+
			"Set AGENTDECK_SUPPRESS_TMUX_WARNING=1 to silence.\n",
		ver)
}

func defaultTmuxVersionProbe() (string, error) {
	out, err := exec.Command("tmux", "-V").CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

var tmuxVersionRE = regexp.MustCompile(`^tmux\s+(\S+)`)

func parseTmuxVersion(raw string) string {
	m := tmuxVersionRE.FindStringSubmatch(strings.TrimSpace(raw))
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// isVulnerableTmuxVersion reports whether the given tmux version is <= 3.6a.
// Upstream fix commits (881bec95, e5a2a25f, 31c93c48) landed on master after
// the 3.6a release and have not been tagged yet. Returns false for master/next
// (assumed patched) and for anything unparseable (don't warn on the unknown).
func isVulnerableTmuxVersion(ver string) bool {
	if ver == "" || ver == "master" || ver == "next" {
		return false
	}
	major, minor, letter, ok := splitTmuxVersion(ver)
	if !ok {
		return false
	}
	if major < 3 {
		return true
	}
	if major > 3 {
		return false
	}
	if minor < 6 {
		return true
	}
	if minor > 6 {
		return false
	}
	// major.minor == 3.6: bare "3.6" and "3.6a" are vulnerable; "3.6b"+ assumed patched.
	return letter <= "a"
}

func splitTmuxVersion(ver string) (major, minor int, letter string, ok bool) {
	parts := strings.SplitN(ver, ".", 2)
	if len(parts) != 2 {
		return 0, 0, "", false
	}
	m, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, "", false
	}
	tail := parts[1]
	i := 0
	for i < len(tail) && tail[i] >= '0' && tail[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, 0, "", false
	}
	n, err := strconv.Atoi(tail[:i])
	if err != nil {
		return 0, 0, "", false
	}
	return m, n, strings.ToLower(tail[i:]), true
}
