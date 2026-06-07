# XDG Base Dirs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement issue #1272 so new installs respect the XDG Base Directory layout while existing `~/.agent-deck` users keep working until they explicitly migrate.

**Architecture:** Add a small import-safe `internal/agentpaths` package for XDG and legacy path resolution, then route `session`, `tmux`, `feedback`, `update`, watcher, and CLI call sites through it. New installs use XDG paths; existing users continue reading legacy paths when the relevant XDG path does not exist. `agent-deck migrate-paths` copies legacy files into the split XDG layout and never deletes the original.

**Tech Stack:** Go 1.25.11, standard library `os`/`filepath`/`io/fs`, existing `flag` CLI style, existing Go test suites.

---

## Design Decisions

- Config files live under `$XDG_CONFIG_HOME/agent-deck`, or `~/.config/agent-deck` when `XDG_CONFIG_HOME` is unset.
- Durable application state lives under `$XDG_DATA_HOME/agent-deck`, or `~/.local/share/agent-deck` when `XDG_DATA_HOME` is unset.
- Cache/debug/update/pricing files live under `$XDG_CACHE_HOME/agent-deck`, or `~/.cache/agent-deck` when `XDG_CACHE_HOME` is unset.
- `profiles/` is state, not config, because profile directories contain `state.db` session history.
- Existing users keep working through legacy fallback. If the XDG target for a category is absent and `~/.agent-deck` contains that category's legacy markers, the resolver returns the legacy path.
- New users with no legacy markers write to XDG paths immediately.
- Migration is opt-in only. Startup may log a one-line hint, but it must not copy, move, or delete user files automatically.
- Keep `~/.agent-deck` strings in user-facing migration/help text where they refer to legacy paths. Remove direct production path construction that builds `HOME/.agent-deck`.

## Target Mapping

| Legacy item | New category | New path |
|---|---|---|
| `config.toml` | config | `$XDG_CONFIG_HOME/agent-deck/config.toml` |
| `config.json` | config | `$XDG_CONFIG_HOME/agent-deck/config.json` |
| `skills/` | config | `$XDG_CONFIG_HOME/agent-deck/skills/` |
| `profiles/` | data | `$XDG_DATA_HOME/agent-deck/profiles/` |
| `sessions.json*` legacy profile files | data | `$XDG_DATA_HOME/agent-deck/sessions.json*` before existing profile migration runs |
| `hooks/` | data | `$XDG_DATA_HOME/agent-deck/hooks/` |
| `locks/` | data | `$XDG_DATA_HOME/agent-deck/locks/` |
| `cost-events/` | data | `$XDG_DATA_HOME/agent-deck/cost-events/` |
| `events/` | data | `$XDG_DATA_HOME/agent-deck/events/` |
| `runtime/` | data | `$XDG_DATA_HOME/agent-deck/runtime/` |
| `inboxes/` | data | `$XDG_DATA_HOME/agent-deck/inboxes/` |
| `conductor/` | data | `$XDG_DATA_HOME/agent-deck/conductor/` |
| `watcher/`, `watchers/`, `triage/` | data | `$XDG_DATA_HOME/agent-deck/...` |
| `logs/` session and transition logs | data | `$XDG_DATA_HOME/agent-deck/logs/` |
| `feedback-state.json` | data | `$XDG_DATA_HOME/agent-deck/feedback-state.json` |
| `ack-signal`, `badge-updates/` | data | `$XDG_DATA_HOME/agent-deck/...` |
| `debug.log`, `cost-debug.log` | cache | `$XDG_CACHE_HOME/agent-deck/...` |
| `update-cache.json`, `pricing.json` | cache | `$XDG_CACHE_HOME/agent-deck/...` |
| `debug-dump-*.jsonl`, `crash-dump-*.jsonl` | cache | `$XDG_CACHE_HOME/agent-deck/...` |

## Files

- Create: `internal/agentpaths/paths.go` - XDG/legacy path resolver.
- Create: `internal/agentpaths/paths_test.go` - resolver unit tests.
- Create: `internal/agentpaths/migrate.go` - migration planning and copy implementation.
- Create: `internal/agentpaths/migrate_test.go` - migration tests.
- Modify: `internal/session/config.go` - wrap config/data path APIs and legacy profile migration.
- Modify: `internal/session/userconfig.go` - load/save user config through config path API.
- Modify: `internal/session/{hook_watcher,event_writer,inbox,inbox_consumer,inbox_stophook,transition_notifier,session_id_event_log,maintenance,plugin_install,skills_catalog,taskworker,worker_scratch,watcher_meta,conductor,instance_spawn_guard,idle_timeout_watcher}.go` - route runtime/data paths through the data API.
- Modify: `internal/tmux/{tmux,badge_update}.go` - route logs, ack-signal, and badge update files through `internal/agentpaths`.
- Modify: `internal/feedback/state.go` - route feedback state through the data API.
- Modify: `internal/update/update.go` - route update cache through the cache API.
- Modify: `cmd/agent-deck/{main,hook_handler,watcher_cmd_skills,plugin_cmd,plugin_cli,try_cmd,session_cmd}.go` - route CLI paths and update help strings.
- Modify: `internal/watcher/{engine,layout}.go` - route watcher default dirs through session/agentpaths APIs.
- Modify: selected tests under `internal/session`, `internal/tmux`, `internal/feedback`, `internal/update`, `cmd/agent-deck`, `internal/watcher`, and `tests/eval/session` that seed `~/.agent-deck`.
- Modify: `README.md`, `CHANGELOG.md`, and watcher embedded docs only where user-facing default path text changes.

---

### Task 1: Add Core Path Resolver Tests

**Files:**
- Create: `internal/agentpaths/paths_test.go`
- Create: `internal/agentpaths/paths.go`

- [ ] **Step 1: Create a compiling stub package**

Create `internal/agentpaths/paths.go` with only the public surface used by the tests:

```go
package agentpaths

import "fmt"

const AppDirName = "agent-deck"

func LegacyDir() (string, error) { return "", fmt.Errorf("not implemented") }
func ConfigDir() (string, error) { return "", fmt.Errorf("not implemented") }
func DataDir() (string, error) { return "", fmt.Errorf("not implemented") }
func CacheDir() (string, error) { return "", fmt.Errorf("not implemented") }
func EffectiveConfigPath(name string) (string, error) { return "", fmt.Errorf("not implemented") }
func EffectiveDataDir(markers ...string) (string, error) { return "", fmt.Errorf("not implemented") }
```

- [ ] **Step 2: Write failing XDG default and env override tests**

Create `internal/agentpaths/paths_test.go` with these tests:

```go
package agentpaths

import (
	"os"
	"path/filepath"
	"testing"
)

func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	return home
}

func TestXDGDirs_DefaultToHomeFallbacks(t *testing.T) {
	home := isolateHome(t)
	wantConfig := filepath.Join(home, ".config", "agent-deck")
	wantData := filepath.Join(home, ".local", "share", "agent-deck")
	wantCache := filepath.Join(home, ".cache", "agent-deck")

	if got, _ := ConfigDir(); got != wantConfig {
		t.Fatalf("ConfigDir() = %q, want %q", got, wantConfig)
	}
	if got, _ := DataDir(); got != wantData {
		t.Fatalf("DataDir() = %q, want %q", got, wantData)
	}
	if got, _ := CacheDir(); got != wantCache {
		t.Fatalf("CacheDir() = %q, want %q", got, wantCache)
	}
}

func TestXDGDirs_EnvOverrides(t *testing.T) {
	home := isolateHome(t)
	cfg := filepath.Join(home, "xdg-config")
	data := filepath.Join(home, "xdg-data")
	cache := filepath.Join(home, "xdg-cache")
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_CACHE_HOME", cache)

	if got, _ := ConfigDir(); got != filepath.Join(cfg, "agent-deck") {
		t.Fatalf("ConfigDir() = %q", got)
	}
	if got, _ := DataDir(); got != filepath.Join(data, "agent-deck") {
		t.Fatalf("DataDir() = %q", got)
	}
	if got, _ := CacheDir(); got != filepath.Join(cache, "agent-deck") {
		t.Fatalf("CacheDir() = %q", got)
	}
}

func TestEffectiveConfigPath_LegacyWinsOnlyWhenXDGFileMissing(t *testing.T) {
	home := isolateHome(t)
	legacy := filepath.Join(home, ".agent-deck")
	xdg := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.toml"), []byte("theme = \"dark\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := EffectiveConfigPath("config.toml")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(legacy, "config.toml") {
		t.Fatalf("legacy fallback path = %q", got)
	}

	if err := os.MkdirAll(xdg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "config.toml"), []byte("theme = \"light\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = EffectiveConfigPath("config.toml")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(xdg, "config.toml") {
		t.Fatalf("xdg path = %q", got)
	}
}

func TestEffectiveDataDir_LegacyWinsOnlyWhenXDGDataMissing(t *testing.T) {
	home := isolateHome(t)
	legacy := filepath.Join(home, ".agent-deck")
	xdg := filepath.Join(home, ".local", "share", "agent-deck")
	if err := os.MkdirAll(filepath.Join(legacy, "profiles", "default"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := EffectiveDataDir("profiles")
	if err != nil {
		t.Fatal(err)
	}
	if got != legacy {
		t.Fatalf("legacy data fallback = %q, want %q", got, legacy)
	}

	if err := os.MkdirAll(filepath.Join(xdg, "profiles"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err = EffectiveDataDir("profiles")
	if err != nil {
		t.Fatal(err)
	}
	if got != xdg {
		t.Fatalf("xdg data path = %q, want %q", got, xdg)
	}
}
```

- [ ] **Step 3: Run tests and verify they fail**

Run:

```bash
GOTOOLCHAIN=go1.25.11 GOPATH=/private/tmp/agent-deck-go \
GOCACHE=/private/tmp/agent-deck-go-build-cache GOTMPDIR=/private/tmp \
go test ./internal/agentpaths -run 'TestXDGDirs|TestEffective' -count=1
```

Expected: tests fail with `not implemented`.

### Task 2: Implement `internal/agentpaths`

**Files:**
- Modify: `internal/agentpaths/paths.go`
- Test: `internal/agentpaths/paths_test.go`

- [ ] **Step 1: Implement path resolution**

Replace the stub with an implementation matching this API:

```go
package agentpaths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const AppDirName = "agent-deck"

func homeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return home, nil
}

func LegacyDir() (string, error) {
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agent-deck"), nil
}

func xdgDir(envName string, fallbackParts ...string) (string, error) {
	if v := strings.TrimSpace(os.Getenv(envName)); v != "" {
		return filepath.Join(v, AppDirName), nil
	}
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	parts := append([]string{home}, fallbackParts...)
	parts = append(parts, AppDirName)
	return filepath.Join(parts...), nil
}

func ConfigDir() (string, error) {
	return xdgDir("XDG_CONFIG_HOME", ".config")
}

func DataDir() (string, error) {
	return xdgDir("XDG_DATA_HOME", ".local", "share")
}

func CacheDir() (string, error) {
	return xdgDir("XDG_CACHE_HOME", ".cache")
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func EffectiveConfigPath(name string) (string, error) {
	cfg, err := ConfigDir()
	if err != nil {
		return "", err
	}
	xdgPath := filepath.Join(cfg, filepath.Base(name))
	if exists(xdgPath) {
		return xdgPath, nil
	}
	legacy, err := LegacyDir()
	if err != nil {
		return "", err
	}
	legacyPath := filepath.Join(legacy, filepath.Base(name))
	if exists(legacyPath) {
		return legacyPath, nil
	}
	return xdgPath, nil
}

func EffectiveDataDir(markers ...string) (string, error) {
	data, err := DataDir()
	if err != nil {
		return "", err
	}
	if exists(data) {
		return data, nil
	}
	legacy, err := LegacyDir()
	if err != nil {
		return "", err
	}
	for _, marker := range markers {
		if marker == "" {
			continue
		}
		if exists(filepath.Join(legacy, marker)) {
			return legacy, nil
		}
	}
	return data, nil
}

func EffectiveDataPath(name string, markers ...string) (string, error) {
	dir, err := EffectiveDataDir(markers...)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.Clean(name)), nil
}

func CachePath(name string) (string, error) {
	dir, err := CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.Clean(name)), nil
}
```

- [ ] **Step 2: Run resolver tests**

Run:

```bash
GOTOOLCHAIN=go1.25.11 GOPATH=/private/tmp/agent-deck-go \
GOCACHE=/private/tmp/agent-deck-go-build-cache GOTMPDIR=/private/tmp \
go test ./internal/agentpaths -count=1
```

Expected: `ok github.com/asheshgoplani/agent-deck/internal/agentpaths`.

- [ ] **Step 3: Commit**

```bash
git add internal/agentpaths/paths.go internal/agentpaths/paths_test.go
git commit -m "feat(paths): add XDG base directory resolver"
```

### Task 3: Route Session Config and Profile Storage

**Files:**
- Modify: `internal/session/config.go`
- Modify: `internal/session/userconfig.go`
- Modify: `internal/session/config_test.go`
- Modify: `internal/session/userconfig_test.go`
- Modify: `internal/session/storage_test.go`

- [ ] **Step 1: Add failing session path tests**

Add tests that assert:

```go
func TestGetUserConfigPath_UsesXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, "cfg")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	got, err := session.GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(xdg, "agent-deck", "config.toml")
	if got != want {
		t.Fatalf("GetUserConfigPath() = %q, want %q", got, want)
	}
}
```

Add a storage test with `NewStorageWithProfile("default")` and assert `storage.Path()` is under `$XDG_DATA_HOME/agent-deck/profiles/default/state.db`.

- [ ] **Step 2: Verify tests fail**

Run:

```bash
GOTOOLCHAIN=go1.25.11 GOPATH=/private/tmp/agent-deck-go \
GOCACHE=/private/tmp/agent-deck-go-build-cache GOTMPDIR=/private/tmp \
go test ./internal/session -run 'TestGetUserConfigPath_UsesXDGConfigHome|TestNewStorageWithProfile_UsesXDGDataHome' -count=1
```

Expected: tests fail because current code returns `HOME/.agent-deck`.

- [ ] **Step 3: Implement session wrappers**

Update `internal/session/config.go` to import `internal/agentpaths` and define:

```go
func GetAgentDeckDir() (string, error) {
	return agentpaths.EffectiveDataDir(
		ProfilesDirName,
		"sessions.json",
		"hooks",
		"events",
		"inboxes",
		"conductor",
		"watcher",
	)
}

func GetConfigPath() (string, error) {
	return agentpaths.EffectiveConfigPath(ConfigFileName)
}

func GetProfilesDir() (string, error) {
	dir, err := agentpaths.EffectiveDataDir(ProfilesDirName, "sessions.json")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ProfilesDirName), nil
}
```

Update `internal/session/userconfig.go`:

```go
func GetUserConfigPath() (string, error) {
	return agentpaths.EffectiveConfigPath(UserConfigFileName)
}
```

- [ ] **Step 4: Run focused tests**

Run:

```bash
GOTOOLCHAIN=go1.25.11 GOPATH=/private/tmp/agent-deck-go \
GOCACHE=/private/tmp/agent-deck-go-build-cache GOTMPDIR=/private/tmp \
go test ./internal/session -run 'TestGetUserConfigPath|TestUserConfig|TestNewStorageWithProfile|TestPersistence_' -count=1
```

Expected: focused session tests pass, except any known pre-existing `TestPersistence_CustomCommandResumesFromLatestJSONL` failure should be reported separately and not hidden.

- [ ] **Step 5: Commit**

```bash
git add internal/session/config.go internal/session/userconfig.go internal/session/*_test.go
git commit -m "feat(session): route config and profile storage through XDG paths"
```

### Task 4: Route Runtime and Durable State Paths

**Files:**
- Modify: `internal/session/hook_watcher.go`
- Modify: `internal/session/event_writer.go`
- Modify: `internal/session/inbox.go`
- Modify: `internal/session/inbox_consumer.go`
- Modify: `internal/session/inbox_stophook.go`
- Modify: `internal/session/transition_notifier.go`
- Modify: `internal/session/session_id_event_log.go`
- Modify: `internal/session/maintenance.go`
- Modify: `internal/session/plugin_install.go`
- Modify: `internal/session/skills_catalog.go`
- Modify: `internal/session/taskworker.go`
- Modify: `internal/session/worker_scratch.go`
- Modify: `internal/session/watcher_meta.go`
- Modify: `internal/session/conductor.go`
- Modify: `internal/session/instance_spawn_guard.go`
- Modify: `internal/session/idle_timeout_watcher.go`
- Modify: corresponding tests in `internal/session`

- [ ] **Step 1: Add failing focused path tests**

Add tests for representative public path functions:

```go
func TestRuntimeStateDirs_UseXDGDataHome(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "data")
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", data)

	if got := GetHooksDir(); got != filepath.Join(data, "agent-deck", "hooks") {
		t.Fatalf("GetHooksDir() = %q", got)
	}
	if got := GetEventsDir(); got != filepath.Join(data, "agent-deck", "events") {
		t.Fatalf("GetEventsDir() = %q", got)
	}
	if got := InboxDir(); got != filepath.Join(data, "agent-deck", "inboxes") {
		t.Fatalf("InboxDir() = %q", got)
	}
}
```

Add `WatcherDir` and `ConductorDir` assertions in their existing test files.

- [ ] **Step 2: Implement path helpers for state subdirectories**

Use `agentpaths.EffectiveDataPath` or a small session wrapper:

```go
func dataSubdir(name string, legacyMarkers ...string) string {
	p, err := agentpaths.EffectiveDataPath(name, append([]string{name}, legacyMarkers...)...)
	if err != nil {
		return filepath.Join(os.TempDir(), ".agent-deck", name)
	}
	return p
}
```

Route existing functions:

```go
func GetHooksDir() string { return dataSubdir("hooks") }
func GetEventsDir() string { return dataSubdir("events") }
func InboxDir() string { return dataSubdir("inboxes") }
func WatcherDir() (string, error) { return agentpaths.EffectiveDataPath("watcher", "watcher", "watchers") }
```

- [ ] **Step 3: Run focused state tests**

Run:

```bash
GOTOOLCHAIN=go1.25.11 GOPATH=/private/tmp/agent-deck-go \
GOCACHE=/private/tmp/agent-deck-go-build-cache GOTMPDIR=/private/tmp \
go test ./internal/session ./internal/watcher -run 'HooksDir|EventsDir|InboxDir|WatcherDir|ConductorDir|Transition|Maintenance|SpawnLock' -count=1
```

Expected: pass.

- [ ] **Step 4: Commit**

```bash
git add internal/session internal/watcher
git commit -m "feat(paths): route runtime state through XDG data dir"
```

### Task 5: Route Independent Packages Without Import Cycles

**Files:**
- Modify: `internal/tmux/tmux.go`
- Modify: `internal/tmux/badge_update.go`
- Modify: `internal/feedback/state.go`
- Modify: `internal/update/update.go`
- Modify: tests in `internal/tmux`, `internal/feedback`, `internal/update`

- [ ] **Step 1: Add package-specific failing tests**

Add or update tests for:

```go
func TestFeedbackStatePath_UsesXDGDataHome(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "data")
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", data)
	st := &feedback.State{FeedbackEnabled: true, MaxShows: 3}
	if err := feedback.SaveState(st); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(data, "agent-deck", "feedback-state.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("state not written to XDG data path %s: %v", want, err)
	}
}
```

Add `internal/tmux` assertions that `LogDir()`, `GetAckSignalPath()`, and `BadgeUpdatesDir()` use `$XDG_DATA_HOME/agent-deck/...`.

Add `internal/update` assertions that cache reads and writes use `$XDG_CACHE_HOME/agent-deck/update-cache.json`.

- [ ] **Step 2: Implement imports through `internal/agentpaths`**

Use `internal/agentpaths` directly in these packages. Do not import `internal/session` into `internal/tmux` or `internal/feedback`.

Examples:

```go
func LogDir() string {
	path, err := agentpaths.EffectiveDataPath("logs", "logs")
	if err != nil {
		return filepath.Join(os.TempDir(), ".agent-deck", "logs")
	}
	return path
}
```

```go
func statePath() (string, error) {
	return agentpaths.EffectiveDataPath("feedback-state.json", "feedback-state.json")
}
```

```go
func getCacheDir() (string, error) {
	return agentpaths.CacheDir()
}
```

- [ ] **Step 3: Run focused tests**

Run:

```bash
GOTOOLCHAIN=go1.25.11 GOPATH=/private/tmp/agent-deck-go \
GOCACHE=/private/tmp/agent-deck-go-build-cache GOTMPDIR=/private/tmp \
go test ./internal/tmux ./internal/feedback ./internal/update -run 'XDG|StatePath|LogDir|AckSignal|Badge|Cache' -count=1
```

Expected: pass.

- [ ] **Step 4: Commit**

```bash
git add internal/tmux internal/feedback internal/update
git commit -m "feat(paths): route tmux feedback and update paths through XDG"
```

### Task 6: Route CLI Entrypoints and Hook Handler

**Files:**
- Modify: `cmd/agent-deck/main.go`
- Modify: `cmd/agent-deck/hook_handler.go`
- Modify: `cmd/agent-deck/watcher_cmd_skills.go`
- Modify: `cmd/agent-deck/plugin_cmd.go`
- Modify: `cmd/agent-deck/plugin_cli.go`
- Modify: `cmd/agent-deck/try_cmd.go`
- Modify: `cmd/agent-deck/session_cmd.go`
- Modify: corresponding tests in `cmd/agent-deck`

- [ ] **Step 1: Add failing CLI path tests**

Add tests that run the binary helper with isolated `HOME`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, and `XDG_CACHE_HOME`:

```go
func TestCLI_ReadsXDGConfigForTmuxSocket(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, "cfg")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	configDir := filepath.Join(cfg, "agent-deck")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[tmux]\nsocket_name = \"agentdeck\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := session.GetTmuxSettings().SocketName
	if got != "agentdeck" {
		t.Fatalf("socket_name = %q, want agentdeck", got)
	}
}
```

Add hook handler tests for `getHooksDir()` and `getCostEventsDir()` using XDG data.

- [ ] **Step 2: Replace direct CLI path construction**

Update:

```go
cacheDir, _ := agentpaths.CacheDir()
costEventsDir, _ := agentpaths.EffectiveDataPath("cost-events", "cost-events")
debugDir, _ := agentpaths.CacheDir()
```

Use config path display helpers for user-facing text:

```go
func displayUserConfigPath() string {
	p, err := session.GetUserConfigPath()
	if err != nil {
		return "$XDG_CONFIG_HOME/agent-deck/config.toml"
	}
	return p
}
```

- [ ] **Step 3: Keep uninstall explicit**

Update `handleUninstall` so `--keep-data` describes and preserves all three XDG locations plus legacy. Dry-run should list every existing location:

```text
Config: <xdg config dir>
Data: <xdg data dir>
Cache: <xdg cache dir>
Legacy: ~/.agent-deck
```

When deleting, delete only paths that exist and are not symlinks escaping the user's home. Keep the existing confirmation flow.

- [ ] **Step 4: Run CLI tests**

Run:

```bash
GOTOOLCHAIN=go1.25.11 GOPATH=/private/tmp/agent-deck-go \
GOCACHE=/private/tmp/agent-deck-go-build-cache GOTMPDIR=/private/tmp \
go test ./cmd/agent-deck -run 'XDG|HookHandler|Uninstall|Plugin|Try|SessionFork|Config' -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/agent-deck
git commit -m "feat(cli): use XDG paths in commands and hook handlers"
```

### Task 7: Add `agent-deck migrate-paths`

**Files:**
- Create: `internal/agentpaths/migrate.go`
- Create: `internal/agentpaths/migrate_test.go`
- Modify: `cmd/agent-deck/main.go`
- Create: `cmd/agent-deck/migrate_paths_cmd_test.go`

- [ ] **Step 1: Add failing migration library tests**

Tests should cover:

```go
func TestMigrateLegacyLayout_CopiesSplitCategories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacy := filepath.Join(home, ".agent-deck")
	if err := os.MkdirAll(filepath.Join(legacy, "profiles", "default"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.toml"), []byte("theme = \"dark\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "profiles", "default", "state.db"), []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "update-cache.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := MigrateLegacyLayout(MigrationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Copied) == 0 {
		t.Fatal("expected copied items")
	}
	assertFileExists(t, filepath.Join(home, ".config", "agent-deck", "config.toml"))
	assertFileExists(t, filepath.Join(home, ".local", "share", "agent-deck", "profiles", "default", "state.db"))
	assertFileExists(t, filepath.Join(home, ".cache", "agent-deck", "update-cache.json"))
	assertFileExists(t, filepath.Join(legacy, "config.toml"))
}
```

Add a conflict test where the XDG destination already exists and `Force` is false. Expected: return a conflict error and leave both files unchanged.

- [ ] **Step 2: Implement migration library**

Use these public types:

```go
type PathCategory string

const (
	CategoryConfig PathCategory = "config"
	CategoryData   PathCategory = "data"
	CategoryCache  PathCategory = "cache"
)

type MigrationOptions struct {
	DryRun bool
	Force  bool
}

type MigrationItem struct {
	Name        string
	Category    PathCategory
	Source      string
	Destination string
	Directory   bool
}

type MigrationResult struct {
	DryRun    bool
	Copied    []MigrationItem
	Skipped   []MigrationItem
	Conflicts []MigrationItem
}
```

Implement `MigrateLegacyLayout(opts MigrationOptions) (*MigrationResult, error)` using copy-only semantics:

- Create destination parent dirs with `0o700`.
- Copy files with source mode preserved where possible.
- Copy directories recursively.
- Refuse destination overwrite unless `opts.Force` is true.
- Do not delete, rename, or mutate `~/.agent-deck`.

- [ ] **Step 3: Add CLI command**

Add `migrate-paths` to `cmd/agent-deck/main.go` command dispatch and help:

```text
agent-deck migrate-paths [--dry-run] [--force]
```

Output format:

```text
Migrating legacy ~/.agent-deck paths to XDG layout
copied config config.toml -> /.../.config/agent-deck/config.toml
copied data profiles -> /.../.local/share/agent-deck/profiles
copied cache update-cache.json -> /.../.cache/agent-deck/update-cache.json
legacy directory left untouched: /.../.agent-deck
```

For conflicts:

```text
conflict config config.toml already exists at /.../.config/agent-deck/config.toml
rerun with --force to overwrite destination files
```

- [ ] **Step 4: Run migration tests**

Run:

```bash
GOTOOLCHAIN=go1.25.11 GOPATH=/private/tmp/agent-deck-go \
GOCACHE=/private/tmp/agent-deck-go-build-cache GOTMPDIR=/private/tmp \
go test ./internal/agentpaths ./cmd/agent-deck -run 'MigrateLegacyLayout|migrate-paths|MigratePaths' -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/agentpaths cmd/agent-deck/main.go cmd/agent-deck/migrate_paths_cmd_test.go
git commit -m "feat(paths): add explicit XDG migration command"
```

### Task 8: Update User-Facing Text, Docs, and Scripts

**Files:**
- Modify: `README.md`
- Modify: `CHANGELOG.md`
- Modify: `cmd/agent-deck/plugin_cmd.go`
- Modify: `cmd/agent-deck/plugin_cli.go`
- Modify: `cmd/agent-deck/try_cmd.go`
- Modify: `cmd/agent-deck/session_cmd.go`
- Modify: `internal/ui/settings_panel.go`
- Modify: `internal/ui/mcp_dialog.go`
- Modify: `internal/ui/plugin_dialog.go`
- Modify: `internal/ui/skill_dialog.go`
- Modify: scripts that seed config under `$HOME/.agent-deck`
- Modify: eval/capability tests that seed config under `$HOME/.agent-deck`

- [ ] **Step 1: Update visible path strings**

Replace static path text like:

```text
~/.agent-deck/config.toml
```

with:

```text
$XDG_CONFIG_HOME/agent-deck/config.toml
```

When space allows, include the fallback:

```text
$XDG_CONFIG_HOME/agent-deck/config.toml (default ~/.config/agent-deck/config.toml)
```

Keep legacy text only when describing migration or backward compatibility.

- [ ] **Step 2: Update test harness config seeds**

For tests/scripts that should exercise new installs, seed:

```bash
export XDG_CONFIG_HOME="$HOME/.config"
mkdir -p "$XDG_CONFIG_HOME/agent-deck"
cat > "$XDG_CONFIG_HOME/agent-deck/config.toml" <<'EOF'
...
EOF
```

For tests that intentionally exercise legacy fallback, keep `HOME/.agent-deck` and name the test/comment as legacy fallback.

- [ ] **Step 3: Run docs and drift tests**

Run:

```bash
GOTOOLCHAIN=go1.25.11 GOPATH=/private/tmp/agent-deck-go \
GOCACHE=/private/tmp/agent-deck-go-build-cache GOTMPDIR=/private/tmp \
go test ./cmd/agent-deck ./internal/ui ./tests/eval/... -run 'XDG|Config|Plugin|Skill|Watcher|Update' -count=1
```

Expected: pass.

- [ ] **Step 4: Commit**

```bash
git add README.md CHANGELOG.md cmd/agent-deck internal/ui scripts tests
git commit -m "docs(paths): document XDG base directory layout"
```

### Task 9: Add Regression Guard Against Direct Legacy Path Construction

**Files:**
- Create: `internal/agentpaths/direct_legacy_path_lint_test.go`

- [ ] **Step 1: Write AST lint test**

Create a test that parses production `.go` files and fails on `filepath.Join(..., ".agent-deck", ...)` unless the file is in an allowlist:

```go
var allowedLegacyPathFiles = map[string]bool{
	"internal/agentpaths/paths.go":   true,
	"internal/agentpaths/migrate.go": true,
}
```

Use AST inspection of `ast.CallExpr` so comments, help strings, and migration text do not fail the test.

- [ ] **Step 2: Run lint test and fix remaining production constructors**

Run:

```bash
GOTOOLCHAIN=go1.25.11 GOPATH=/private/tmp/agent-deck-go \
GOCACHE=/private/tmp/agent-deck-go-build-cache GOTMPDIR=/private/tmp \
go test ./internal/agentpaths -run TestNoDirectHomeAgentDeckPathConstruction -count=1
```

Expected: fail first if any production direct constructors remain, then pass after routing those call sites through `agentpaths`.

- [ ] **Step 3: Commit**

```bash
git add internal/agentpaths/direct_legacy_path_lint_test.go internal cmd
git commit -m "test(paths): guard against direct legacy path construction"
```

### Task 10: Full Verification and PR Prep

**Files:**
- No new source files unless fixing verification failures.

- [ ] **Step 1: Run focused required suites**

Run:

```bash
GOTOOLCHAIN=go1.25.11 GOPATH=/private/tmp/agent-deck-go \
GOCACHE=/private/tmp/agent-deck-go-build-cache GOTMPDIR=/private/tmp \
go test ./internal/agentpaths ./internal/session ./internal/tmux ./internal/feedback ./internal/update ./internal/watcher ./cmd/agent-deck -count=1
```

Expected: pass, except document any already-known unrelated failures with reproduction on `upstream/main`.

- [ ] **Step 2: Run full suite**

Run:

```bash
GOTOOLCHAIN=go1.25.11 GOPATH=/private/tmp/agent-deck-go \
GOCACHE=/private/tmp/agent-deck-go-build-cache GOTMPDIR=/private/tmp \
go test ./... -count=1
```

Expected: pass or produce a clearly triaged list of pre-existing failures.

- [ ] **Step 3: Run diff hygiene**

Run:

```bash
git diff --check
git status --short --branch
```

Expected: no whitespace errors; branch contains only intended feature changes.

- [ ] **Step 4: Prepare PR description**

Include:

- Issue link: `Fixes #1272`.
- Before: config and state were hardcoded under `~/.agent-deck`.
- After: new installs use XDG config/data/cache dirs; legacy installs continue to work; explicit migration command copies data.
- Path mapping table.
- Verification commands and results.
- Note that `profiles/` was placed under data because it contains `state.db`.

## Self-Review

- Issue #1272 coverage: config path support, state/data support, cache support, direct bypass audit, UI/help text, conductor/watcher/templates, migration command, and tests are covered.
- Placeholder scan: no plan step relies on unresolved placeholders, unspecified tests, or vague error handling.
- Type consistency: `internal/agentpaths` owns path primitives; `internal/session` keeps compatibility wrappers; independent lower-level packages import `internal/agentpaths` directly to avoid import cycles.
