package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/safeio"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// SpawnFailureRecord captures why a session's initial process died at or shortly
// after spawn. Issue #1580: when a custom command (e.g. a broken
// `codex.command` npx wrapper) exits immediately, tmux tears the whole session
// down and takes the pane output with it. Status detection later only sees
// "tmux session missing" → StatusError, so the TUI shows a bare "error" with no
// preview and nothing in the logs. This record is written to disk as a durable
// sidecar the moment the death is observed, so the failure survives the process
// boundary and can be surfaced in the preview pane, `session show`, and the
// lifecycle log.
type SpawnFailureRecord struct {
	InstanceID  string `json:"instance_id"`
	Tool        string `json:"tool"`
	Command     string `json:"command,omitempty"`
	Reason      string `json:"reason"`                 // tmux_start_failed | spawn_died_fast
	DyingOutput string `json:"dying_output,omitempty"` // last pane snapshot captured while alive
	ElapsedMs   int64  `json:"elapsed_ms"`             // ms from spawn to observed death (0 for tmux_start_failed)
	Timestamp   int64  `json:"ts"`
}

// spawnFailureDir returns <data>/runtime/spawn-failure, falling back to a temp
// path when the data dir cannot be resolved (mirrors GetSessionIDLifecycleLogPath).
func spawnFailureDir() string {
	path, err := runtimeDataPath("spawn-failure")
	if err != nil {
		return tempAgentDeckPath("runtime", "spawn-failure")
	}
	return path
}

// spawnFailureRecordPath returns the sidecar path for one instance.
func spawnFailureRecordPath(instanceID string) string {
	return filepath.Join(spawnFailureDir(), instanceID+".json")
}

// writeSpawnFailureRecord persists a record atomically. Best-effort: a failure
// to write must never block or crash the caller.
func writeSpawnFailureRecord(rec SpawnFailureRecord) error {
	if rec.Timestamp == 0 {
		rec.Timestamp = time.Now().Unix()
	}
	path := spawnFailureRecordPath(rec.InstanceID)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create spawn-failure dir: %w", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal spawn-failure record: %w", err)
	}
	// SkipBackup: these sidecars are transient and self-clearing; a .bak would
	// just be noise. RefuseEmpty is irrelevant (data is never empty).
	return safeio.SafeOverwrite(path, data, safeio.Options{Perm: 0o644, SkipBackup: true})
}

// readSpawnFailureRecord loads the sidecar for an instance, or (nil, nil) when
// none exists.
func readSpawnFailureRecord(instanceID string) (*SpawnFailureRecord, error) {
	path := spawnFailureRecordPath(instanceID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var rec SpawnFailureRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// clearSpawnFailureRecord removes any stale sidecar for an instance. Called at
// the top of every start attempt so a stale record from a prior failure never
// masks a now-healthy session.
func clearSpawnFailureRecord(instanceID string) {
	_ = safeio.SafeRemove(spawnFailureRecordPath(instanceID), safeio.RemoveOptions{})
}

// SpawnFailure returns the recorded spawn-failure diagnostic for this instance,
// or nil when there is none. Exported for CLI surfaces (`session show`) that
// want to explain a bare "error" status (#1580).
func (i *Instance) SpawnFailure() *SpawnFailureRecord {
	rec, err := readSpawnFailureRecord(i.ID)
	if err != nil {
		return nil
	}
	return rec
}

// FormatForDisplay renders the record as a human-readable block for the preview
// pane and `session show`.
func (r *SpawnFailureRecord) FormatForDisplay() string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("⚠  session failed to start\n")
	switch r.Reason {
	case "tmux_start_failed":
		b.WriteString("The terminal session could not be created.\n")
	case "spawn_died_fast":
		fmt.Fprintf(&b, "The command exited almost immediately (after %dms).\n", r.ElapsedMs)
	default:
		b.WriteString("The session ended unexpectedly during startup.\n")
	}
	if r.Command != "" {
		fmt.Fprintf(&b, "\ncommand: %s\n", r.Command)
	}
	if strings.TrimSpace(r.DyingOutput) != "" {
		b.WriteString("\nlast output before exit:\n")
		b.WriteString(strings.TrimRight(r.DyingOutput, "\n"))
		b.WriteString("\n")
	} else {
		b.WriteString("\n(no output was captured before the process exited)\n")
	}
	b.WriteString("\nTip: run the command manually in this directory to see the full error,\n")
	b.WriteString("or check logs/session-id-lifecycle.jsonl for the spawn trace.\n")
	return b.String()
}

// spawnFastDeathWindow / spawnFastDeathTick bound the fast-death watcher: a
// session that outlives the window is assumed to have started successfully. The
// tick period trades detection latency against subprocess overhead.
const (
	spawnFastDeathWindow = 15 * time.Second
	spawnFastDeathTick   = 250 * time.Millisecond
)

// watchForFastDeath runs after a successful tmux Start() and captures the pane
// output while the initial process is alive. If the tmux session vanishes
// within spawnFastDeathWindow and the disappearance was not a deliberate stop
// (Status == StatusStopped) or a session-swap (a fresh session took over the
// same name), it persists a SpawnFailureRecord with the last captured output +
// elapsed time and emits a spawn_died_fast lifecycle event. A survivor emits a
// spawn_survived event and writes nothing.
//
// Best-effort and self-contained: all errors are swallowed so the watcher can
// never affect the spawn path. Runs in its own goroutine.
// The watcher runs in its own goroutine, so it must never read i's
// mutex-guarded mutable fields directly. Everything it needs is passed by
// value: the pane session pointer captured at launch (sess), the instance id
// and tool, and gen — a snapshot of i.spawnGen taken at launch. A deliberate
// stop or a restart/respawn bumps i.spawnGen, so a mismatch means this watcher
// has been superseded and must exit quietly (#1580 data-race fix).
func (i *Instance) watchForFastDeath(command string, gen uint64, sess *tmux.Session, id, tool string, logger *slog.Logger) {
	if sess == nil {
		return
	}
	start := time.Now()
	deadline := start.Add(spawnFastDeathWindow)
	var lastSnapshot string

	ticker := time.NewTicker(spawnFastDeathTick)
	defer ticker.Stop()

	for {
		<-ticker.C

		// A newer spawn or a deliberate stop bumped the generation — this
		// watcher is stale, so stop quietly and never record a failure.
		if i.spawnGen.Load() != gen {
			return
		}

		if sess.Exists() {
			// Alive: snapshot the current pane so we hold the dying output the
			// instant it disappears (tmux discards the pane on process exit for
			// non-remain-on-exit sessions).
			if content, err := sess.CapturePane(); err == nil {
				if trimmed := strings.TrimSpace(content); trimmed != "" {
					lastSnapshot = trimmed
				}
			}
			if time.Now().After(deadline) {
				// Survived the window: healthy start.
				_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
					InstanceID: id,
					Tool:       tool,
					Action:     "spawn_survived",
					Source:     "spawn_watcher",
				})
				return
			}
			continue
		}

		// Session is gone and it was not a deliberate stop → fast death.
		elapsed := time.Since(start).Milliseconds()
		rec := SpawnFailureRecord{
			InstanceID:  id,
			Tool:        tool,
			Command:     command,
			Reason:      "spawn_died_fast",
			DyingOutput: lastSnapshot,
			ElapsedMs:   elapsed,
		}
		if err := writeSpawnFailureRecord(rec); err != nil {
			logger.Warn("spawn_failure_record_write_failed",
				slog.String("instance_id", id),
				slog.String("error", err.Error()))
		}
		logger.Error("spawn_died_fast",
			slog.String("instance_id", id),
			slog.String("tool", tool),
			slog.String("command", command),
			slog.Int64("elapsed_ms", elapsed),
			slog.String("dying_output", lastSnapshot))
		_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
			InstanceID: id,
			Tool:       tool,
			Action:     "spawn_died_fast",
			Source:     "spawn_watcher",
			Reason:     fmt.Sprintf("exited after %dms", elapsed),
		})
		return
	}
}

// recordTmuxStartFailure persists a record for the case where tmux itself
// failed to create the session (i.tmuxSession.Start returned an error). Unlike
// the fast-death path this has no pane to snapshot — the error string is the
// diagnostic.
func (i *Instance) recordTmuxStartFailure(command string, startErr error) {
	rec := SpawnFailureRecord{
		InstanceID:  i.ID,
		Tool:        i.Tool,
		Command:     command,
		Reason:      "tmux_start_failed",
		DyingOutput: startErr.Error(),
	}
	if err := writeSpawnFailureRecord(rec); err != nil {
		sessionLog.Warn("spawn_failure_record_write_failed",
			slog.String("instance_id", i.ID),
			slog.String("error", err.Error()))
	}
	_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
		InstanceID: i.ID,
		Tool:       i.Tool,
		Action:     "spawn_failed",
		Source:     "spawn_watcher",
		Reason:     startErr.Error(),
	})
}

// recordSpawnAttempt clears any stale record and logs a spawn_attempt lifecycle
// event so every start leaves a durable trace even when the process dies before
// any other code runs.
func (i *Instance) recordSpawnAttempt() {
	clearSpawnFailureRecord(i.ID)
	_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
		InstanceID: i.ID,
		Tool:       i.Tool,
		Action:     "spawn_attempt",
		Source:     "spawn_watcher",
	})
}
