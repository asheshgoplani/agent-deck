# Phase 21: Watcher Folder Hierarchy — Pattern Map

**Mapped:** 2026-04-16
**Files analyzed:** 9 (3 new source, 1 new test, 3 modified, 3 new templates grouped)
**Analogs found:** 9/9 (all have direct in-repo analogs)

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `internal/watcher/layout.go` (new) | utility (path resolver + scaffolder + migrator) | file-I/O | `internal/session/conductor.go:246-263` (LayoutDir) + `:822-874` (InstallShared*) + `:923-969` (MigrateLegacyConductors) | exact (conductor is the named analog) |
| `internal/watcher/state.go` (new) | model (struct + persistence) | CRUD (atomic write-temp-rename) | `internal/session/watcher_meta.go:47-75` (SaveWatcherMeta) | exact |
| `internal/watcher/event_log.go` (new) | utility (append-only writer) | file-I/O (O_APPEND) | `cmd/agent-deck/watcher_cmd.go:666-714` (mergeClientsJSON atomic) for conventions; no direct O_APPEND analog | partial (primitive adaptation) |
| `internal/watcher/layout_test.go` (new) | test | file-I/O assertions | `internal/watcher/health_bridge_test.go:1-80` (harness style) + `internal/watcher/gmail_test.go:160-185` (`t.Setenv("HOME", t.TempDir())`) | exact |
| `internal/watcher/engine.go` (modify :134, :275-399) | service (orchestrator) | event-driven | same file | — |
| `internal/session/watcher_meta.go` (modify :20-27, :11, :29) | model (path resolver) | request-response | same file | — |
| `cmd/agent-deck/watcher_cmd.go` (modify :524, :310-316, :320-346) | controller (CLI handlers) | request-response | same file + `internal/watcher/health.go:148` (Check) | exact |
| `cmd/agent-deck/watcher_cmd_test.go` (modify — append `TestWatcherList_JSON_ExposesStateFields`) | test | file-I/O + JSON assertions | same file `:16-46` (`TestParseChannelsJSON_Valid`) | exact |
| `assets/watcher-templates/{CLAUDE.md, POLICY.md, LEARNINGS.md}` (new) | config (static templates) | N/A | `internal/session/conductor_templates.go` (conductorSharedClaudeMDTemplate, conductorPolicyTemplate, conductorLearningsTemplate) | exact (tone + structure) |

---

## Pattern Assignments

### `internal/watcher/layout.go` (new)

**Primary analog:** `internal/session/conductor.go:246-263`, `:822-874`, `:923-969`
**Supplementary:** `internal/session/watcher_meta.go:20-36`

#### Pattern 1a: Base directory accessors — copy from `internal/session/conductor.go:246-262`

```go
// ConductorDir returns the base conductor directory (~/.agent-deck/conductor)
func ConductorDir() (string, error) {
	dir, err := GetAgentDeckDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "conductor"), nil
}

// ConductorNameDir returns the directory for a named conductor (~/.agent-deck/conductor/<name>)
func ConductorNameDir(name string) (string, error) {
	base, err := ConductorDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, name), nil
}
```

**Phase-21 variant:** In `layout.go`, expose thin wrappers that delegate to `session.WatcherDir()` (singular after the Phase-21 edit) so there's exactly one source of truth. E.g. `func LayoutDir() (string, error) { return session.WatcherDir() }` and `func WatcherDir(name string) (string, error) { return session.WatcherNameDir(name) }`.

#### Pattern 1b: Scaffold shared top-level files — adapt from `internal/session/conductor.go:858-873` (InstallLearningsMD)

```go
// InstallLearningsMD writes the default LEARNINGS.md to the conductor base directory.
// This is the shared (Tier 1) learnings file for generic patterns across all conductors.
func InstallLearningsMD() error {
	dir, err := ConductorDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	targetPath := filepath.Join(dir, "LEARNINGS.md")
	// Don't overwrite if already exists (preserves user entries)
	if _, err := os.Stat(targetPath); err == nil {
		return nil
	}
	return os.WriteFile(targetPath, []byte(conductorLearningsTemplate), 0o644)
}
```

**Phase-21 variant:** CONTEXT.md specifics line 115 mandates "check-and-create each file independently". Prefer the `O_CREATE|O_EXCL` form from RESEARCH.md Pattern 3 to eliminate the TOCTOU window:

```go
func writeIfAbsent(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) { return nil }
		return err
	}
	defer f.Close()
	_, err = f.Write(content)
	return err
}
```

Loop over `{CLAUDE.md, POLICY.md, LEARNINGS.md, clients.json}` with per-file content embedded via `//go:embed assets/watcher-templates/*`. `clients.json` scaffold content is `{}\n`.

#### Pattern 1c: Migration — adapt from `internal/session/conductor.go:923-969` (MigrateLegacyConductors)

The conductor migrator is for a *different* shape (missing meta.json backfill), so the rename+symlink flow is novel. Use the structure below (already drafted in RESEARCH.md Pattern 4). Key conventions to carry over from the conductor analog: return `(..., error)` with wrapped errors via `fmt.Errorf("...: %w", err)`; never panic; no-op when preconditions don't match. Use `os.Lstat` (not `os.Stat`) so the guard doesn't follow a pre-existing symlink.

```go
func MigrateLegacyWatchersDir() error {
	deck, err := session.GetAgentDeckDir()
	if err != nil { return err }
	legacy := filepath.Join(deck, "watchers")
	current := filepath.Join(deck, "watcher")

	_, curErr := os.Stat(current)
	legacyInfo, legacyErr := os.Lstat(legacy)

	if legacyErr == nil && legacyInfo.Mode()&os.ModeSymlink == 0 && os.IsNotExist(curErr) {
		if err := os.Rename(legacy, current); err != nil {
			return fmt.Errorf("migrate watchers dir: %w", err)
		}
		if err := os.Symlink("watcher", legacy); err != nil {
			slog.Warn("watcher: symlink creation failed (non-fatal)", "error", err)
		}
		slog.Info("watcher: migrated legacy ~/.agent-deck/watchers/ → ~/.agent-deck/watcher/",
			slog.String("note", "legacy ~/.agent-deck/issue-watcher/ NOT migrated (out of scope per REQ-WF-6)"))
		return nil
	}
	if legacyErr == nil && legacyInfo.Mode()&os.ModeSymlink == 0 && curErr == nil {
		slog.Warn("watcher: both ~/.agent-deck/watchers/ and ~/.agent-deck/watcher/ exist; skipping migration")
		return nil
	}
	return nil
}
```

**Logger choice:** `slog` (stdlib). Matches `engine.go` (imports `log/slog`). Test captures via `slog.NewJSONHandler(buf, nil)` per RESEARCH.md line 380.

---

### `internal/watcher/state.go` (new)

**Analog:** `internal/session/watcher_meta.go:38-75` (SaveWatcherMeta) + `:77-96` (LoadWatcherMeta)

#### Pattern 2: Atomic write-temp-rename — copy verbatim from `internal/session/watcher_meta.go:47-75`

```go
// SaveWatcherMeta writes meta.json for a watcher.
// Creates the watcher directory if it does not exist.
//
// Uses a write-temp-rename atomic pattern: data is first written to
// meta.json.tmp, then renamed into place via os.Rename (POSIX atomic on
// the same filesystem). A mid-write crash therefore leaves either the
// old meta.json intact or the new meta.json complete — never a partial
// write. Any stale .tmp from a previous crash is overwritten on the
// next Save and removed on rename failure.
func SaveWatcherMeta(meta *WatcherMeta) error {
	if meta == nil {
		return fmt.Errorf("watcher metadata cannot be nil")
	}
	if meta.Name == "" {
		return fmt.Errorf("watcher name cannot be empty")
	}
	dir, err := WatcherNameDir(meta.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create watcher dir: %w", err)
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal watcher meta.json: %w", err)
	}
	finalPath := filepath.Join(dir, "meta.json")
	tmpPath := finalPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("failed to write watcher meta.json.tmp: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename watcher meta.json: %w", err)
	}
	return nil
}
```

**Phase-21 variant:** rename function → `SaveState(name string, s *WatcherState) error`; rename path strings to `state.json` / `state.json.tmp`. Validate `name` via same zero-value guard (`if name == "" { return fmt.Errorf("watcher name cannot be empty") }`). Do NOT fsync — matches the existing pattern (RESEARCH.md Q2 of open questions).

#### Pattern 2b: Read pattern — copy from `internal/session/watcher_meta.go:77-96`

```go
func LoadWatcherMeta(name string) (*WatcherMeta, error) {
	dir, err := WatcherNameDir(name)
	if err != nil {
		return nil, err
	}
	metaPath := filepath.Join(dir, "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read meta.json for watcher %q: %w", name, err)
	}
	var meta WatcherMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse meta.json for watcher %q: %w", name, err)
	}
	if meta.Name == "" {
		meta.Name = name
	}
	return &meta, nil
}
```

**Phase-21 variant:** `LoadState(name) (*WatcherState, error)` — if `state.json` does not exist, return `(nil, nil)` (RESEARCH.md line 446 confirms `list --json` expects this sentinel). Change error wrap to `"failed to read state.json for watcher %q: %w"`.

#### Struct shape (per CONTEXT.md line 44 + RESEARCH.md Q5)

```go
type HealthSample struct {
	TS time.Time `json:"ts"`
	OK bool      `json:"ok"`
}

type WatcherState struct {
	LastEventTS    time.Time       `json:"last_event_ts"`
	ErrorCount     int             `json:"error_count"`
	AdapterHealthy bool            `json:"adapter_healthy"`
	HealthWindow   []HealthSample  `json:"health_window"`    // cap 64
	DedupCursor    string          `json:"dedup_cursor"`
}
```

---

### `internal/watcher/event_log.go` (new)

**Analog (partial):** `cmd/agent-deck/watcher_cmd.go:686-711` (atomic-write conventions) + RESEARCH.md Pattern 2 (append-only adaptation)

No direct O_APPEND analog exists in the repo — this is a small new primitive. Apply the function-signature and error-wrap conventions from existing watcher file writers.

```go
// AppendEventLog appends a single Markdown line to watcher/<name>/task-log.md.
//
// Entry format: "## <RFC3339-ts> - <event_type>: <summary>"
// Atomicity: single Write with O_APPEND is atomic per POSIX for sizes ≤ PIPE_BUF
// (4096 on Linux, 512 on older BSD). AppendEventLog truncates summary so the full
// line stays < 512 bytes — this survives any future multi-goroutine refactor of
// engine.writerLoop (currently single-writer, so serialization is guaranteed today).
func AppendEventLog(name, entry string) error {
	dir, err := WatcherDir(name)
	if err != nil { return err }
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create watcher dir: %w", err)
	}
	path := filepath.Join(dir, "task-log.md")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open task-log.md: %w", err)
	}
	defer f.Close()
	_, err = f.WriteString(entry + "\n")
	return err
}
```

**Phase-21 variant:** adapter-provided `entry` should be constructed by the engine (not inside `AppendEventLog`) so callers control the Markdown template; `AppendEventLog` is a dumb writer. Name-validation (no `..`, no `/`) lives in `WatcherDir(name)` via the shared `session.ValidateConductorName`-style pattern (RESEARCH.md V5 security row).

---

### `internal/watcher/layout_test.go` (new — Task A RED)

**Harness analog:** `internal/watcher/gmail_test.go:159-185` (HOME override)
**Mock/goroutine analog:** `internal/watcher/health_bridge_test.go:1-80` (channel-driven, atomic counters)

#### Pattern 3a: HOME override — copy from `internal/watcher/gmail_test.go:162-166`

```go
func seedFakeOAuth(t *testing.T, watcherName string) string {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	dir := filepath.Join(tmpDir, ".agent-deck", "watchers", watcherName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir watcher dir: %v", err)
	}
	...
}
```

**Phase-21 variant:** every layout test opens with `t.Setenv("HOME", t.TempDir())`. Use the singular `"watcher"` subdir name for all new post-migration paths. For the migration test, seed the legacy `watchers/` directory first, then call `MigrateLegacyWatchersDir`, then assert the singular path + symlink.

#### Pattern 3b: Test function naming + table style — copy from `cmd/agent-deck/watcher_cmd_test.go:16-46` (TestParseChannelsJSON_Valid)

```go
func TestParseChannelsJSON_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "channels.json")
	content := `{...}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	channels, err := parseChannelsJSON(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(channels))
	}
	...
}
```

**Phase-21 variant:** test names per RESEARCH.md line 633-639: `TestLayout_FreshInstallCreatesLayout`, `TestLayout_LegacyMigrationAtomic`, `TestLayout_SymlinkResolves`, `TestLayout_StateRoundtrip`, `TestLayout_EventLogAppendAtomic`, `TestLayout_HotReloadSafe`, `TestLayout_Integration_ThreeEvents`. Use `t.Fatalf` for setup failures, `t.Errorf` for assertion failures (matches repo convention).

#### Pattern 3c: slog capture — RESEARCH.md line 380

```go
var buf bytes.Buffer
logger := slog.New(slog.NewJSONHandler(&buf, nil))
// inject via slog.SetDefault(logger) or via engine config
...
if !strings.Contains(buf.String(), "watcher: migrated legacy") {
	t.Errorf("expected migration log line; got:\n%s", buf.String())
}
```

---

### `internal/watcher/engine.go` (modify)

#### Change 1: line 134 — default `ClientsPath` to singular

```go
// Current (:132-136)
if cfg.ClientsPath == "" {
	if home, err := os.UserHomeDir(); err == nil {
		cfg.ClientsPath = filepath.Join(home, ".agent-deck", "watchers", "clients.json")
	}
}
```

**New:** replace `"watchers"` → `"watcher"`. Add code comment noting that the legacy `watchers/` path is served transparently via the compatibility symlink created by `MigrateLegacyWatchersDir` (RESEARCH.md Q6).

Also update the doc comment at `engine.go:46`: `// Defaults to $HOME/.agent-deck/watcher/clients.json when empty.`

#### Change 2: wire migration + scaffolding into `Start()` — analog `engine.go:181-203`

```go
// Current (:181-199)
func (e *Engine) Start() error {
	for i := range e.adapters {
		entry := &e.adapters[i]
		if err := entry.adapter.Setup(e.ctx, entry.config); err != nil {
			...
			continue
		}
		...
	}
	// Single-writer goroutine serializes all DB writes (D-13).
	e.wg.Add(1)
	go e.writerLoop()
	...
}
```

**Phase-21 variant:** at the top of `Start()` (BEFORE the adapter loop per RESEARCH.md line 516-521):

```go
if err := MigrateLegacyWatchersDir(); err != nil {
	e.log.Warn("watcher_migration_failed", slog.String("error", err.Error()))
	// Non-fatal: continue with current layout.
}
if err := ScaffoldWatcherLayout(); err != nil {
	e.log.Warn("watcher_scaffold_failed", slog.String("error", err.Error()))
}
```

#### Change 3: writerLoop event persistence — analog `engine.go:326-344` (SaveWatcherEvent block)

```go
// Existing (:346-353) — on successful insert:
if inserted {
	// New event: update health tracker and forward to TUI (D-14).
	env.tracker.RecordEvent()
	...
}
```

**Phase-21 variant:** inside the `if inserted {` block (after `env.tracker.RecordEvent()`), add two calls guarded by error-log-and-continue:

```go
entry := fmt.Sprintf("## %s - %s: %s",
	e.clock.Now().UTC().Format(time.RFC3339),
	env.event.Sender,
	truncate(env.event.Subject, 400))
if err := AppendEventLog(env.config.Name, entry); err != nil {
	e.log.Warn("event_log_append_failed",
		slog.String("watcher", env.config.Name), slog.String("error", err.Error()))
}
if err := SaveState(env.config.Name, rebuildState(env.tracker)); err != nil {
	e.log.Warn("state_save_failed",
		slog.String("watcher", env.config.Name), slog.String("error", err.Error()))
}
```

**Critical (CONTEXT.md line 50):** failures in `AppendEventLog` / `SaveState` MUST NOT drop the event — log and continue. No `return` / no `continue` exiting the loop.

---

### `internal/session/watcher_meta.go` (modify)

#### Change: line 20-27 — flip `WatcherDir()` to singular

```go
// Current (:20-27)
// WatcherDir returns the base directory for all watchers (~/.agent-deck/watchers).
func WatcherDir() (string, error) {
	dir, err := GetAgentDeckDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "watchers"), nil
}
```

**New:** `filepath.Join(dir, "watcher")` and update doc comment to `~/.agent-deck/watcher`.

Additional text-only edits required (RESEARCH.md Q7 table):
- `watcher_meta.go:11` — comment `// Persisted as meta.json in ~/.agent-deck/watchers/<name>/.` → singular.
- `watcher_meta.go:29` — doc `// WatcherNameDir returns the directory for a named watcher (~/.agent-deck/watchers/<name>).` → singular.

---

### `cmd/agent-deck/watcher_cmd.go` (modify)

#### Change 1: line 524 — update user-facing error text

```go
// Current (:522-525)
if err != nil {
	fmt.Printf("Routing config: not available (%v)\n", err)
	fmt.Println("  Create ~/.agent-deck/watchers/clients.json to enable routing.")
	return
}
```

**New:** replace `watchers/clients.json` → `watcher/clients.json` (single text change, no logic change).

#### Change 2: `handleWatcherList` (:310-346) — extend JSON schema

Existing struct (lines 310-316):

```go
type watcherListEntry struct {
	Name          string  `json:"name"`
	Type          string  `json:"type"`
	Status        string  `json:"status"`
	EventsPerHour float64 `json:"events_per_hour"`
	Health        string  `json:"health"`
}
```

**Phase-21 variant (Task D):** add three fields per CONTEXT.md line 59 and RESEARCH.md lines 432-457:

```go
type watcherListEntry struct {
	Name          string     `json:"name"`
	Type          string     `json:"type"`
	Status        string     `json:"status"`
	EventsPerHour float64    `json:"events_per_hour"`
	Health        string     `json:"health"`
	// NEW (Task D):
	LastEventTS   *time.Time `json:"last_event_ts"`  // nil if no state.json
	ErrorCount    int        `json:"error_count"`
	HealthStatus  string     `json:"health_status"`  // per HealthTracker.Check
}
```

Population logic (new, inside the `for _, w := range watchers` loop):

```go
state, err := watcher.LoadState(w.Name)
if err != nil || state == nil {
	entry.LastEventTS = nil
	entry.ErrorCount = 0
	entry.HealthStatus = "unknown"
} else {
	ts := state.LastEventTS
	entry.LastEventTS = &ts
	entry.ErrorCount = state.ErrorCount
	entry.HealthStatus = string(classifyFromState(state, maxSilenceMin))
}
```

Where `classifyFromState` either (a) rehydrates a `HealthTracker` via new `ForHydration` setters on `health.go`, or (b) inlines the rules from `health.go:163-178`. RESEARCH.md line 509 recommends the setter path. CONTEXT.md line 60 locks "do NOT reinvent" — reuse `internal/watcher/health.go`.

#### Health classifier reference: `internal/watcher/health.go:148-188`

```go
func (h *HealthTracker) Check() HealthState {
	...
	switch {
	case !h.adapterHealthy || h.consecutiveErrors >= 10:
		status = HealthStatusError
		...
	case h.consecutiveErrors >= 3:
		status = HealthStatusWarning
		...
	case !h.lastEventTime.IsZero() && time.Since(h.lastEventTime) > time.Duration(h.maxSilenceMinutes)*time.Minute:
		status = HealthStatusWarning
		...
	}
	return HealthState{...}
}
```

---

### `cmd/agent-deck/watcher_cmd_test.go` (modify — append one test)

**Analog in same file:** `TestParseChannelsJSON_Valid` at `:16-47` (shown above).

**Phase-21 variant:** `TestWatcherList_JSON_ExposesStateFields` — per RESEARCH.md line 640. Steps:
1. `t.Setenv("HOME", t.TempDir())`
2. Seed `~/.agent-deck/watcher/w1/state.json` with fixed `last_event_ts`, `error_count: 2`, etc.
3. Invoke the `watcher list --json` handler (may require exposing an internal handler helper or exec-style test).
4. Unmarshal stdout into the extended struct; assert `LastEventTS != nil`, `ErrorCount == 2`, `HealthStatus == "warning"` (because errorCount >= 3 triggers warning, but 2 < 3 so assert "healthy" instead — pick fixture values that cleanly hit one branch).

---

### `assets/watcher-templates/{CLAUDE.md, POLICY.md, LEARNINGS.md}` (new)

**Analog:** `internal/session/conductor_templates.go` — specifically `conductorSharedClaudeMDTemplate` (starts line 9), `conductorPolicyTemplate`, `conductorLearningsTemplate`.

```go
// internal/session/conductor_templates.go:1-12
package session

// conductorSharedClaudeMDTemplate is the shared instructions file written to
// ~/.agent-deck/conductor/<instructions-file> for the selected conductor agent.
// It contains CLI reference, protocols, and formats shared by all conductors (mechanism).
// Agent behavior (rules, auto-response policy) lives in POLICY.md, not here.
// The active agent walks up the directory tree, so per-conductor instructions files inherit this automatically.
const conductorSharedClaudeMDTemplate = `# Conductor: Shared Knowledge Base

This file contains shared infrastructure knowledge (CLI reference, protocols, formats) for all conductor sessions.
...
`
```

**Phase-21 variant:** unlike the conductor templates (which live as Go string constants), Phase-21 places them as raw Markdown under `assets/watcher-templates/` and loads via `//go:embed`. Rationale: matches the "shared resources in `assets/`" convention already present (`assets/` directory exists — see `ls` output). Content responsibilities:
- `CLAUDE.md` — mechanism/CLI reference for watchers (analog tone to `conductorSharedClaudeMDTemplate`).
- `POLICY.md` — escalation + dedup + retry rules (analog tone to `conductorPolicyTemplate`).
- `LEARNINGS.md` — empty-with-heading scaffold (analog to `conductorLearningsTemplate` — user-owned accumulator).

Embedding pattern (in `layout.go`):

```go
import _ "embed"

//go:embed assets/watcher-templates/CLAUDE.md
var watcherClaudeTemplate []byte

//go:embed assets/watcher-templates/POLICY.md
var watcherPolicyTemplate []byte

//go:embed assets/watcher-templates/LEARNINGS.md
var watcherLearningsTemplate []byte
```

(Note: go:embed paths are relative to the source file, so either keep templates under `internal/watcher/templates/` or add a small embed shim at `assets/assets.go`. Planner/executor discretion.)

---

## Shared Patterns

### Atomic Write-Temp-Rename
**Source:** `internal/session/watcher_meta.go:47-75` (SaveWatcherMeta)
**Apply to:** `state.go::SaveState`. Also the sibling `cmd/agent-deck/watcher_cmd.go:686-711` (mergeClientsJSON) shows the same primitive via `os.CreateTemp` — either form is acceptable, but stick with the `WriteFile(tmp) + Rename` style to match `watcher_meta.go` exactly (least code, one fewer fd).

### HOME-override test isolation
**Source:** `internal/watcher/gmail_test.go:163-166`
**Apply to:** every new test in `layout_test.go` and the new case in `watcher_cmd_test.go`.
```go
tmpDir := t.TempDir()
t.Setenv("HOME", tmpDir)
```

### slog conventions for new log lines
**Source:** `internal/watcher/engine.go:300-304` (thread_reply_lookup_failed warn), `:337-341` (save_event_failed error)
```go
e.log.Warn("save_event_failed",
	slog.String("watcher_id", env.watcherID),
	slog.String("sender", env.event.Sender),
	slog.String("error", err.Error()),
)
```
**Apply to:** all new log lines in `engine.go` (event_log_append_failed, state_save_failed, watcher_migration_failed, watcher_scaffold_failed) — use snake_case event name, structured key/value attrs, `slog.String("error", err.Error())` for errors.

### Error wrapping convention
**Source:** `internal/session/watcher_meta.go:58, :62, :67, :70, :86, :89`
```go
return fmt.Errorf("failed to create watcher dir: %w", err)
```
**Apply to:** all new error returns. Prefix lowercase, `%w` for wraps, specific to action.

### Migration collision handling (use `os.Lstat`, not `os.Stat`)
**Source:** `internal/session/conductor.go:842-844`
```go
if info, err := os.Lstat(targetPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
	return nil
}
```
**Apply to:** `MigrateLegacyWatchersDir` — prevents following a pre-existing symlink and mis-detecting "legacy exists".

### Name validation (security: CWE-59 path traversal)
**Source:** `internal/session/conductor.go:275-287` (ValidateConductorName)
```go
func ValidateConductorName(name string) error {
	if name == "" { return fmt.Errorf("conductor name cannot be empty") }
	if len(name) > 64 { return fmt.Errorf("conductor name too long (max 64 characters)") }
	if !conductorNameRegex.MatchString(name) {
		return fmt.Errorf("invalid conductor name %q: must start with alphanumeric and contain only alphanumeric, dots, underscores, or hyphens", name)
	}
	return nil
}
```
**Apply to:** watcher name guard in `layout.go::WatcherDir(name)` and `event_log.go::AppendEventLog(name, ...)`. Per RESEARCH.md line 684 "watcher names follow conductor rules" — reuse the same regex.

---

## No Analog Found

All files have in-repo analogs. The weakest match is `event_log.go` (no direct O_APPEND writer exists in the watcher package), but the primitive is 10 lines of stdlib and the error-wrap / path-join / mkdir conventions come from `watcher_meta.go`.

---

## Metadata

**Analog search scope:** `internal/session/`, `internal/watcher/`, `cmd/agent-deck/`, `assets/`
**Files scanned:** ~25 via Grep + 8 read in full
**Pattern extraction date:** 2026-04-16

## PATTERN MAPPING COMPLETE

**Phase:** 21 - Watcher Folder Hierarchy
**Files classified:** 9
**Analogs found:** 9/9

### Coverage
- Files with exact analog: 8
- Files with role-match / partial analog: 1 (event_log.go — no direct O_APPEND writer in repo)
- Files with no analog: 0

### Key Patterns Identified
- All new file I/O in `state.go` copies the atomic write-temp-rename pattern verbatim from `internal/session/watcher_meta.go:47-75`.
- All layout/migration code mirrors `internal/session/conductor.go` (`ConductorDir` / `InstallLearningsMD` / `MigrateLegacyConductors`), with one correctness upgrade: use `O_CREATE|O_EXCL` for template scaffolding (RESEARCH.md Pattern 3) instead of the conductor's stat-then-write.
- All new tests use `t.Setenv("HOME", t.TempDir())` per `internal/watcher/gmail_test.go:163-166`; slog migration log captured via `slog.NewJSONHandler(&buf, nil)` and asserted with `strings.Contains(buf.String(), "watcher: migrated legacy")`.
- CLI `list --json` extension reuses `HealthTracker.Check()` (`internal/watcher/health.go:148`) for the `health_status` field — no parallel classifier.

### File Created
`<repo-root>/.worktrees/watcher-completion/.planning/phases/21-watcher-folder-hierarchy/21-PATTERNS.md`

### Ready for Planning
Pattern mapping complete. Planner can reference analog file:line ranges + verbatim excerpts when writing PLAN.md task actions.
