package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
)

// ListStatsFlag is the `list --json` flag that makes it emit one JSON line
// of status-pass timing on stderr after its stdout listing (#2331).
// SSHRunner passes it only on its own remote invocation, never for a human
// running the CLI directly, so stdout's wire format and default stderr
// output are both unchanged for everyone else. It is a CLI flag rather than
// an env var because the persistent remote channel (#2174) spawns the
// request as `agent-deck <args>` argv with no env passthrough — an env var
// would silently never apply on that (normally the faster, more used) path.
const ListStatsFlag = "--stats"

// ListStatsPrefix marks the stats line so a reader can find it among any
// other stderr chatter (tmux warnings, hook errors) without guessing at
// line position.
const ListStatsPrefix = "agent-deck-list-stats: "

// ListStats is the status pass's own timing, as read by an SSH caller that
// cannot otherwise see what a `list --json` on the far end actually spent
// its time on. Root cause of #2331: a slow remote gave the controller one
// opaque wall-clock number covering network + status pass together, with no
// way to tell which stage was slow.
type ListStats struct {
	StatusPassMS int64 `json:"status_pass_ms"`
	TmuxCalls    int64 `json:"tmux_calls"`
	Sessions     int   `json:"sessions"`
}

// parseListStats scans stderr for the stats line a remote emits when asked
// (ListStatsEnvVar). It is best-effort: an older remote binary, or one that
// was not asked, simply has no such line, and a malformed one is skipped
// rather than failing the fetch — the session listing itself already parsed
// fine from stdout by the time this runs.
func parseListStats(stderr []byte) *ListStats {
	scanner := bufio.NewScanner(bytes.NewReader(stderr))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		payload, ok := strings.CutPrefix(line, ListStatsPrefix)
		if !ok {
			continue
		}
		var stats ListStats
		if err := json.Unmarshal([]byte(payload), &stats); err != nil {
			continue
		}
		return &stats
	}
	return nil
}
