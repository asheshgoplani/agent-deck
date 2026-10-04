package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/update"
)

// Each remote's own update timer, seen and managed from the controller
// (#2472). `remote list --check` asks every remote for `update
// --timer-status --json`; `remote update --install-timer` runs `update
// --install-timer --json` there, and a plain `remote update` runs `update
// --ensure-timer --json` after the deploy. A remote whose binary predates
// these flags answers with text or a flag error, which degrades to
// {"kind":"unknown"} (status) or a note (install); the command itself still
// succeeds.

// remoteTimerInstallTimeout bounds a remote's install/migration: a few
// systemctl or launchctl calls, but over a possibly slow SSH path.
const remoteTimerInstallTimeout = 2 * time.Minute

// RemoteTimerManager is the slice of SSHRunner that reads and manages a
// remote's update timer. Tests substitute a stub.
type RemoteTimerManager interface {
	FetchTimerStatus(ctx context.Context) update.TimerStatus
	InstallUpdateTimer(ctx context.Context, ensureOnly bool) (RemoteTimerInstall, error)
}

// RemoteTimerInstall is a remote's answer to `update --install-timer` or
// `--ensure-timer`.
type RemoteTimerInstall struct {
	update.TimerEnsureResult
	// Legacy is set when the remote binary does not answer in JSON (it
	// predates #2472): Output carries its first line instead.
	Legacy bool   `json:"legacy_binary,omitempty"`
	Output string `json:"output,omitempty"`
}

// legacyBinaryHint follows a Legacy summary: an old binary's own install
// leaves a hand-made legacy timer running beside the new one, which only
// a current binary migrates.
const legacyBinaryHint = "; update the remote's agent-deck first (agent-deck remote update) so it can retire a hand-made legacy timer"

// Summary is the one-line report for the CLI and the update note.
func (i RemoteTimerInstall) Summary() string {
	if i.Legacy {
		return "remote binary predates timer migration: " + i.Output + legacyBinaryHint
	}
	return i.Line()
}

// UnknownTimerStatus is what a remote that cannot report its timer shows.
func UnknownTimerStatus(note string) update.TimerStatus {
	return update.TimerStatus{Kind: update.TimerKindUnknown, Note: note}
}

// ParseRemoteTimerStatus reads a remote's `update --timer-status --json`
// answer. Anything that is not that JSON document (an older binary's text,
// a flag error, an unreachable host) is kind unknown with a note saying
// why, never a guess.
func ParseRemoteTimerStatus(out []byte, err error) update.TimerStatus {
	if err != nil {
		return UnknownTimerStatus("timer status unavailable: " + firstNonEmptyLine(string(out), err.Error()))
	}
	var st update.TimerStatus
	if !decodeFirstJSONObject(out, &st) || st.Kind == "" {
		return UnknownTimerStatus("remote agent-deck does not report its update timer (older than --timer-status --json)")
	}
	return st
}

// FetchTimerStatus asks the remote for its update timer state.
func (r *SSHRunner) FetchTimerStatus(ctx context.Context) update.TimerStatus {
	out, err := r.Run(ctx, "update", "--timer-status", "--json")
	return ParseRemoteTimerStatus(out, err)
}

// InstallUpdateTimer runs the remote's own install (or, ensureOnly, its
// manage_timer-honouring heal). An old remote that still knows
// --install-timer but answers in text is reported as Legacy with its first
// line; a remote that rejects the flag is an error.
func (r *SSHRunner) InstallUpdateTimer(ctx context.Context, ensureOnly bool) (RemoteTimerInstall, error) {
	flag := "--install-timer"
	if ensureOnly {
		flag = "--ensure-timer"
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, remoteTimerInstallTimeout)
	defer cancel()
	out, err := r.run(timeoutCtx, "update", flag, "--json")
	return parseRemoteTimerInstall(out, err)
}

func parseRemoteTimerInstall(out []byte, err error) (RemoteTimerInstall, error) {
	var doc struct {
		update.TimerEnsureResult
		Error string `json:"error"`
	}
	if decodeFirstJSONObject(out, &doc) && doc.Action != "" {
		res := RemoteTimerInstall{TimerEnsureResult: doc.TimerEnsureResult}
		switch {
		case doc.Error != "":
			return res, fmt.Errorf("%s", doc.Error)
		case err != nil:
			return res, err
		}
		return res, nil
	}
	if err != nil {
		return RemoteTimerInstall{}, fmt.Errorf("%w: %s", err, firstNonEmptyLine(string(out), ""))
	}
	return RemoteTimerInstall{Legacy: true, Output: firstNonEmptyLine(string(out), "no output")}, nil
}

// decodeFirstJSONObject decodes the first JSON object in out into v,
// skipping any text a remote printed before it (an update notice, a
// warning).
func decodeFirstJSONObject(out []byte, v any) bool {
	i := bytes.IndexByte(out, '{')
	if i < 0 {
		return false
	}
	return json.NewDecoder(bytes.NewReader(out[i:])).Decode(v) == nil
}

func firstNonEmptyLine(text, fallback string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return fallback
}

// RecordRemoteTimers stores freshly read timer states in the shared remote
// cache next to each remote's version, so `remote list` and the TUI show
// them without asking again.
func RecordRemoteTimers(timers map[string]update.TimerStatus, at time.Time) error {
	if len(timers) == 0 {
		return nil
	}
	return updateRemoteVersionCache(func(cache *remoteVersionCache) {
		for name, st := range timers {
			state := cache.Remotes[name]
			st := st
			state.Timer = &st
			state.TimerCheckedAt = at
			cache.Remotes[name] = state
		}
	})
}
