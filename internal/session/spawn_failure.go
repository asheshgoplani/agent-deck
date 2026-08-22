package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
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
	Reason      string `json:"reason"`                 // tmux_start_failed | spawn_died_fast | prepare_failed
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
	return writeSpawnFailureRecordTo(rec, spawnFailureDir())
}

// envExportPattern matches shell export statements with single-quoted values
// (including the quote escape produced by buildEnvExports) so persisted
// commands never carry credential values. Only the value is redacted; the key
// stays visible for diagnosis.
var envExportPattern = regexp.MustCompile(`export ([A-Za-z_][A-Za-z0-9_]*)='(?:[^']*(?:'\\''[^']*)*)'`)

// redactEnvValues strips env values from a persisted command or error string.
// A restart --env API_KEY=... that fails during prepare would otherwise write
// the literal secret into a sidecar that session show exposes.
func redactEnvValues(s string) string {
	return envExportPattern.ReplaceAllString(s, "export $1='[redacted]'")
}

// redactSidecarBytes redacts every string field of an already-persisted sidecar,
// returning (redacted, true) only when something actually changed.
//
// It works on the decoded fields rather than the raw JSON text on purpose: a
// value containing a quote carries the shell quote escape, which JSON in turn
// escapes the backslash of, and envExportPattern would only half-match that —
// a regex sweep over the raw bytes would leave part of such a secret on disk.
// Decoding into
// json.RawMessage (not into SpawnFailureRecord) keeps numbers byte-exact and
// preserves fields written by a newer or older version of the struct, so a
// rewrite never silently drops a record's contents.
func redactSidecarBytes(data []byte) ([]byte, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, false
	}
	changed := false
	for key, raw := range fields {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			continue // not a string: numbers and nested shapes stay verbatim
		}
		redacted := redactEnvValues(s)
		if redacted == s {
			continue
		}
		encoded, err := json.Marshal(redacted)
		if err != nil {
			continue
		}
		fields[key] = encoded
		changed = true
	}
	if !changed {
		return nil, false
	}
	out, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return nil, false
	}
	return out, true
}

// ensureSpawnFailureDirSecure hardens a spawn-failure directory that already
// exists. os.MkdirAll leaves an EXISTING directory's mode untouched, so an
// install upgraded from a version that created the dir 0755 keeps it — and every
// 0644 sidecar already in it, written before redaction existed — readable by
// every other local user. Without this, the redaction above only ever protects
// fresh installs, and the credentials that actually leaked stay leaked.
//
// So: tighten the directory, then sweep the sidecars already in it — chmod 0600
// AND rewrite them through the redactor, so a stored credential is removed from
// disk rather than merely hidden behind directory permissions.
//
// Best-effort by construction: every step ignores its error and moves on. This
// runs on the spawn-failure path only (a handful of files, and only when a
// session has already failed to start), and it must never be the reason a
// failure record fails to be written.
func ensureSpawnFailureDirSecure(dir string) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return
	}
	if info.Mode().Perm() != 0o700 {
		_ = os.Chmod(dir, 0o700)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		fi, err := entry.Info()
		// Lstat semantics: a symlink is never a regular file here, so we can
		// neither chmod nor rewrite through one into an unrelated target.
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if fi.Mode().Perm() != 0o600 {
			_ = os.Chmod(path, 0o600)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		redacted, changed := redactSidecarBytes(data)
		if !changed {
			continue
		}
		_ = safeio.SafeOverwrite(path, redacted, safeio.Options{Perm: 0o600, SkipBackup: true})
	}
}

// writeSpawnFailureRecordTo is the path-explicit variant, used by
// watchForFastDeath. dir is resolved once by the caller at goroutine-spawn
// time (see the comment on watchForFastDeath) rather than re-resolved here
// from the live $HOME, which could have moved on by the time the watcher's
// goroutine — never joined — actually gets to write.
func writeSpawnFailureRecordTo(rec SpawnFailureRecord, dir string) error {
	if rec.Timestamp == 0 {
		rec.Timestamp = time.Now().Unix()
	}
	// Single choke point: every writer funnels through here, so redaction and
	// tight permissions cannot be forgotten at a call site.
	rec.Command = redactEnvValues(rec.Command)
	rec.DyingOutput = redactEnvValues(rec.DyingOutput)
	path := filepath.Join(dir, rec.InstanceID+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create spawn-failure dir: %w", err)
	}
	// MkdirAll above only sets the mode on a directory it creates; upgraded
	// installs come with a 0755 one full of world-readable pre-fix sidecars.
	ensureSpawnFailureDirSecure(filepath.Dir(path))
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal spawn-failure record: %w", err)
	}
	// SkipBackup: these sidecars are transient and self-clearing; a .bak would
	// just be noise. RefuseEmpty is irrelevant (data is never empty).
	return safeio.SafeOverwrite(path, data, safeio.Options{Perm: 0o600, SkipBackup: true})
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
	case "prepare_failed":
		// Nothing was launched, so the default arm's "ended unexpectedly
		// during startup" would be actively misleading here.
		b.WriteString("The command could not be prepared, so nothing was launched.\n")
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
//
// wake is the supersede channel, subscribed synchronously by
// startFastDeathWatcher before the goroutine starts. Closing it lets a
// supersession be noticed immediately rather than only on the next
// spawnFastDeathTick poll.
//
// The generation is re-checked immediately before each write, not just once
// per iteration: sess.Exists() shells out to tmux and can take tens of
// milliseconds, and a Stop/Kill landing inside that call would otherwise be
// observed as a vanished session — i.e. recorded as a spurious
// spawn_died_fast for what was a deliberate teardown (and written into a HOME
// the owning test may already be tearing down).
//
// Every write additionally goes through commitSpawnWatchWrite, which re-checks
// the generation while holding spawnWriteMu. That is what turns the remaining
// check-then-write gap from small into closed: a teardown that bumps and then
// takes the same mutex (bumpSpawnGenAndBarrier) is guaranteed that any write
// still pending here either completed before it, or sees the new generation and
// is suppressed. Only the writes are covered, never the tmux calls, so a stop
// never waits on tmux.
//
// lifecycleLogPath and failureDir are resolved before the goroutine starts and
// passed by value, so the watcher cannot write through a later process-global
// HOME value.
func (i *Instance) startFastDeathWatcher(command string, sess *tmux.Session, id, tool string, logger *slog.Logger) {
	gen, wake := i.newSpawnGenWatch()
	lifecycleLogPath := GetSessionIDLifecycleLogPath()
	failureDir := spawnFailureDir()
	i.spawnWatchers.Add(1)
	go func() {
		defer i.spawnWatchers.Done()
		i.watchForFastDeath(command, gen, wake, sess, id, tool, logger, lifecycleLogPath, failureDir)
	}()
}

func (i *Instance) waitForFastDeathWatchers() {
	i.spawnWatchers.Wait()
}

func (i *Instance) watchForFastDeath(command string, gen uint64, wake <-chan struct{}, sess *tmux.Session, id, tool string, logger *slog.Logger, lifecycleLogPath, failureDir string) {
	if sess == nil {
		return
	}
	start := time.Now()
	deadline := start.Add(spawnFastDeathWindow)
	var lastSnapshot string

	ticker := time.NewTicker(spawnFastDeathTick)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
		case <-wake:
			// A bump closed this — a newer spawn or a deliberate stop
			// happened, so the generation check below is guaranteed to
			// mismatch and return on this same iteration instead of waiting
			// out the rest of the tick interval. Drop the channel so a
			// closed-channel receive can never spin the loop if that
			// invariant is ever weakened.
			wake = nil
		}

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
				// Survived the window: healthy start. The commit re-checks the
				// generation under the write barrier — CapturePane above shells
				// out to tmux, so a teardown can easily have started since the
				// check at the top of this iteration.
				_ = i.commitSpawnWatchWrite(gen, func() {
					_ = writeSessionIDLifecycleEventTo(SessionIDLifecycleEvent{
						InstanceID: id,
						Tool:       tool,
						Action:     "spawn_survived",
						Source:     "spawn_watcher",
					}, lifecycleLogPath)
				})
				return
			}
			continue
		}
		if i.spawnGen.Load() != gen {
			return
		}

		// Session is gone — but sess.Exists() shells out to tmux, so a
		// teardown may well have started during that call: killInternal and
		// the restart paths bump the generation BEFORE killing tmux, precisely
		// so this is visible here even though it was not visible at the top of
		// the iteration. Everything below therefore commits under the write
		// barrier, which re-checks the generation, rather than trusting the
		// earlier check.
		elapsed := time.Since(start).Milliseconds()
		rec := SpawnFailureRecord{
			InstanceID:  id,
			Tool:        tool,
			Command:     command,
			Reason:      "spawn_died_fast",
			DyingOutput: lastSnapshot,
			ElapsedMs:   elapsed,
		}
		var writeErr error
		committed := i.commitSpawnWatchWrite(gen, func() {
			writeErr = writeSpawnFailureRecordTo(rec, failureDir)
			_ = writeSessionIDLifecycleEventTo(SessionIDLifecycleEvent{
				InstanceID: id,
				Tool:       tool,
				Action:     "spawn_died_fast",
				Source:     "spawn_watcher",
				Reason:     fmt.Sprintf("exited after %dms", elapsed),
			}, lifecycleLogPath)
		})
		if !committed {
			// Superseded mid-flight: a deliberate stop, not a spawn failure.
			return
		}

		// Logging happens OUTSIDE the write barrier. Teardown waits on that
		// mutex, and a slow or blocking log handler is not something a
		// deliberate stop should ever be held up by; the records above are the
		// part that must be ordered against teardown, not the diagnostics.
		if writeErr != nil {
			logger.Warn("spawn_failure_record_write_failed",
				slog.String("instance_id", logging.SanitizeValue(id)),
				slog.String("error", logging.SanitizeValue(writeErr.Error())))
		}
		// Every value here is session-supplied: the tool and command come from
		// user config, and dying_output is raw captured pane content — newlines
		// and control characters in it would otherwise forge log records
		// (CodeQL go/log-injection).
		logger.Error("spawn_died_fast",
			slog.String("instance_id", logging.SanitizeValue(id)),
			slog.String("tool", logging.SanitizeValue(tool)),
			slog.String("command", logging.SanitizeValue(command)),
			slog.Int64("elapsed_ms", elapsed),
			slog.String("dying_output", logging.SanitizeValue(lastSnapshot)))
		return
	}
}

// recordPrepareFailure persists a record for a start that failed BEFORE tmux
// was ever asked to do anything — command/wrapper assembly, sandbox setup,
// config regeneration (#1924).
//
// These paths returned a bare error, so the session landed on StatusError with
// no tmux session, no spawn-failure record and nothing in `session show`: an
// error state with no reason, which is a guess rendered as fact. They are also
// the paths most likely to be hit right after a user edits a wrapper or command,
// which is exactly when a reason is worth the most.
func (i *Instance) recordPrepareFailure(command string, prepErr error) {
	if prepErr == nil {
		return
	}
	rec := SpawnFailureRecord{
		InstanceID: i.ID,
		Tool:       i.Tool,
		Command:    command,
		Reason:     "prepare_failed",
		// Reusing DyingOutput for the error string, as recordTmuxStartFailure
		// already does: there is no pane to snapshot, so the error IS the
		// diagnostic.
		DyingOutput: prepErr.Error(),
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
		Source:     "prepare_command",
		Reason:     prepErr.Error(),
	})
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
