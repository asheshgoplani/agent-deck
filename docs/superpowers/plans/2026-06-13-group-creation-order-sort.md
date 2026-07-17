# Group Creation-Order Sort Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make sessions within a group display in creation order by default (honoring the existing K/J manual reorder), with the issue-#857 status/recency sort kept available behind a `group_sort` config toggle, and fix the genuine non-deterministic shuffling of orphaned sub-sessions.

**Architecture:** Add a `group_sort` field to `UserConfig` (`"creation"` default | `"actionable"`). A concurrency-safe package-level cache in `internal/session` holds the active mode; it is refreshed from `LoadUserConfig` (single funnel — covers TUI, web, CLI, and reloads). `SortInstancesByActionable` reads the cached mode once per call: in creation mode the normal band sorts by `Order` only; in actionable mode the existing status→recency→Order chain runs. Pin/Maestro bands are unchanged. Separately, `Flatten()` is made deterministic by sorting orphaned sub-sessions by `Order` instead of emitting them in Go map-iteration order.

**Tech Stack:** Go 1.24, BurntSushi/toml, `sync/atomic`, standard `testing`. Build/test via `make build` / `go test -race -count=1 ./...`.

> **Refinement over spec §2:** `SetGroupSortMode` is invoked once inside `LoadUserConfig` (the single config funnel) rather than at the `home.go`/`menu_snapshot_builder.go` call sites. This is simpler and uniformly covers reload, CLI, and web. Default mode is `"creation"`, which also covers the no-config / parse-error fallback paths.

---

## File Structure

| File | Responsibility | Change |
|------|----------------|--------|
| `internal/session/userconfig.go` | User TOML config | Add `GroupSort` field + `GetGroupSort()`; call `SetGroupSortMode` in `LoadUserConfig` |
| `internal/session/groups.go` | Group tree, in-group sort, flatten | Add cached mode (`groupSortMode`, `SetGroupSortMode`, `currentGroupSortMode`); gate normal-band sort; deterministic orphan emission |
| `internal/session/userconfig_test.go` | Config tests | `GetGroupSort` normalization + `LoadUserConfig` sets mode |
| `internal/session/groups_test.go` | Group/sort tests | Cached-mode tests; creation-order default test; orphan-determinism test |
| `internal/ui/issue857_sort_actionable_test.go` | #857 regression | Pin both tests to `actionable` mode |
| `internal/session/maestro_test.go` | Maestro band | Pin `TestSortInstancesByActionable_MaestroFirst` to `actionable` mode |
| `CHANGELOG.md` | Release notes | `## [Unreleased]` Added + Fixed bullets |
| `skills/agent-deck/references/config-reference.md` | Config docs | Document `group_sort` in Top-Level table |

---

## Task 1: Config field + accessor

**Files:**
- Modify: `internal/session/userconfig.go` (struct near `SyncTitle` ~line 75; accessor near `GetSyncTitle` ~line 1015)
- Test: `internal/session/userconfig_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/session/userconfig_test.go`:

```go
func TestUserConfig_GetGroupSort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "creation"},
		{"creation", "creation"},
		{"actionable", "actionable"},
		{"garbage", "creation"},
		{"ACTIONABLE", "creation"}, // case-sensitive; only exact "actionable" opts in
	}
	for _, c := range cases {
		cfg := &UserConfig{GroupSort: c.in}
		if got := cfg.GetGroupSort(); got != c.want {
			t.Errorf("GetGroupSort(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/session/ -run TestUserConfig_GetGroupSort`
Expected: FAIL — `cfg.GroupSort` undefined / `GetGroupSort` undefined.

- [ ] **Step 3: Add the field**

In the `UserConfig` struct, after the `SyncTitle *bool` field, add:

```go
	// GroupSort controls the order of sessions within a group.
	//   "creation"   (default) — fixed creation order; honors K/J manual reorder.
	//   "actionable"           — issue #857 status→recency→Order surfacing.
	// Empty or unrecognized values normalize to "creation".
	GroupSort string `toml:"group_sort"`
```

- [ ] **Step 4: Add the accessor**

After `GetSyncTitle` (~line 1020), add:

```go
// GetGroupSort returns the normalized within-group sort mode: "actionable" only
// when explicitly set, otherwise "creation" (the default).
func (c *UserConfig) GetGroupSort() string {
	if c.GroupSort == "actionable" {
		return "actionable"
	}
	return "creation"
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/session/ -run TestUserConfig_GetGroupSort`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/session/userconfig.go internal/session/userconfig_test.go
git commit -m "feat(session): add group_sort config field + GetGroupSort accessor"
```

---

## Task 2: Cached group-sort mode

**Files:**
- Modify: `internal/session/groups.go` (imports block lines 3-12; new code after `SortInstancesByActionable`, ~line 200)
- Test: `internal/session/groups_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/session/groups_test.go`:

```go
func TestGroupSortMode_DefaultAndSet(t *testing.T) {
	t.Cleanup(func() { SetGroupSortMode("creation") })

	SetGroupSortMode("creation") // normalize starting point
	if got := currentGroupSortMode(); got != "creation" {
		t.Fatalf("default/creation mode = %q, want creation", got)
	}
	SetGroupSortMode("actionable")
	if got := currentGroupSortMode(); got != "actionable" {
		t.Fatalf("after set actionable = %q, want actionable", got)
	}
	SetGroupSortMode("garbage")
	if got := currentGroupSortMode(); got != "creation" {
		t.Fatalf("garbage normalizes to %q, want creation", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/session/ -run TestGroupSortMode_DefaultAndSet`
Expected: FAIL — `SetGroupSortMode` / `currentGroupSortMode` undefined.

- [ ] **Step 3: Add `sync/atomic` to the imports**

Change the import block in `internal/session/groups.go` to:

```go
import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"unicode"

	"github.com/asheshgoplani/agent-deck/internal/git"
)
```

- [ ] **Step 4: Add the cached mode**

Immediately after the `SortInstancesByActionable` function (after its closing `}` at ~line 200), add:

```go
// groupSortMode caches the active within-group sort mode ("creation" or
// "actionable"). It is refreshed from LoadUserConfig on every config (re)load,
// so SortInstancesByActionable can read it without a disk hit and without
// threading a parameter through the tree constructors. Defaults to "creation"
// until SetGroupSortMode is first called.
var groupSortMode atomic.Value // holds string

// SetGroupSortMode updates the cached within-group sort mode. Any value other
// than "actionable" normalizes to "creation".
func SetGroupSortMode(mode string) {
	if mode != "actionable" {
		mode = "creation"
	}
	groupSortMode.Store(mode)
}

// currentGroupSortMode returns the cached mode, defaulting to "creation" when
// it has never been set.
func currentGroupSortMode() string {
	if v, ok := groupSortMode.Load().(string); ok && v != "" {
		return v
	}
	return "creation"
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/session/ -run TestGroupSortMode_DefaultAndSet`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/session/groups.go internal/session/groups_test.go
git commit -m "feat(session): cached group-sort mode (SetGroupSortMode/currentGroupSortMode)"
```

---

## Task 3: Wire mode into LoadUserConfig

**Files:**
- Modify: `internal/session/userconfig.go` (`LoadUserConfig` success path, just before `userConfigCache = &config`, ~line 2357)
- Test: `internal/session/userconfig_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/session/userconfig_test.go`:

```go
func TestLoadUserConfig_SetsGroupSortMode(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()
	defer ClearUserConfigCache()
	t.Cleanup(func() { SetGroupSortMode("creation") })

	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configPath := filepath.Join(agentDeckDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("group_sort = \"actionable\"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := LoadUserConfig(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := currentGroupSortMode(); got != "actionable" {
		t.Fatalf("LoadUserConfig did not apply group_sort: mode = %q, want actionable", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/session/ -run TestLoadUserConfig_SetsGroupSortMode`
Expected: FAIL — mode stays `"creation"` because `LoadUserConfig` does not call `SetGroupSortMode`.

- [ ] **Step 3: Apply the mode on the success path**

In `LoadUserConfig`, the success path currently ends with:

```go
	normalizeUIHiddenTools(&config.UI, config.Tools)

	userConfigCache = &config
	userConfigCacheMtime = currentMtime
	return userConfigCache, nil
}
```

Insert the `SetGroupSortMode` call so it becomes:

```go
	normalizeUIHiddenTools(&config.UI, config.Tools)

	// Keep the in-group sort mode in lockstep with the loaded config. This is
	// the single funnel for TUI, web, and CLI; ReloadUserConfig routes through
	// here too, so an external edit to group_sort takes effect on next load.
	SetGroupSortMode(config.GetGroupSort())

	userConfigCache = &config
	userConfigCacheMtime = currentMtime
	return userConfigCache, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/session/ -run TestLoadUserConfig_SetsGroupSortMode`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/session/userconfig.go internal/session/userconfig_test.go
git commit -m "feat(session): apply group_sort mode from LoadUserConfig"
```

---

## Task 4: Gate the normal-band sort (flip default to creation order)

**Files:**
- Modify: `internal/session/groups.go` (`SortInstancesByActionable`, lines 175-200)
- Test: `internal/session/groups_test.go` (new creation-order test)
- Modify (pin to actionable): `internal/ui/issue857_sort_actionable_test.go`, `internal/session/maestro_test.go`

- [ ] **Step 1: Write the failing creation-order test**

Add to `internal/session/groups_test.go`:

```go
func TestSortInstancesByActionable_CreationOrderDefault(t *testing.T) {
	t.Cleanup(func() { SetGroupSortMode("creation") })
	SetGroupSortMode("creation")
	now := time.Now()

	// Statuses + recency are arranged so an actionable sort would reorder these,
	// but creation mode must keep strict Order ascending.
	instances := []*Instance{
		{ID: "a", GroupPath: "g", Order: 0, Status: StatusStopped, LastAccessedAt: now.Add(-5 * time.Hour)},
		{ID: "b", GroupPath: "g", Order: 1, Status: StatusError, LastAccessedAt: now},
		{ID: "c", GroupPath: "g", Order: 2, Status: StatusWaiting, LastAccessedAt: now.Add(-1 * time.Minute)},
	}
	tree := NewGroupTree(instances)
	got := []string{}
	for _, s := range tree.Groups["g"].Sessions {
		got = append(got, s.ID)
	}
	want := []string{"a", "b", "c"}
	if !equalStrings(got, want) {
		t.Fatalf("creation mode must order by Order asc; got %v want %v", got, want)
	}
}
```

> Note: `equalStrings` and the `time` import already exist in this package's tests (used by `pin_test.go`). If `time` is not yet imported in `groups_test.go`, add it.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/session/ -run TestSortInstancesByActionable_CreationOrderDefault`
Expected: FAIL — current code always runs the actionable chain, producing `[b, c, a]`.

- [ ] **Step 3: Gate the normal band on the mode**

Replace the body of `SortInstancesByActionable` (lines 175-200) with:

```go
func SortInstancesByActionable(insts []*Instance) {
	mode := currentGroupSortMode()
	sort.SliceStable(insts, func(i, j int) bool {
		// Outermost key is the band: maestro (-1), pin-top (0), normal (1),
		// pin-bottom (2) — see pinZone.
		zi, zj := pinZone(insts[i]), pinZone(insts[j])
		if zi != zj {
			return zi < zj
		}
		// Maestro, pin-top and pin-bottom bands are fully fixed: Order only.
		if zi != 1 {
			return insts[i].Order < insts[j].Order
		}
		// Normal band. In actionable mode (issue #857) the status→recency tiers
		// apply before Order; in creation mode (default) Order alone decides, so
		// sessions keep their creation order (or K/J manual order).
		if mode == "actionable" {
			pi, pj := actionablePriority(insts[i].Status), actionablePriority(insts[j].Status)
			if pi != pj {
				return pi < pj
			}
			ai, aj := insts[i].LastAccessedAt, insts[j].LastAccessedAt
			if !ai.Equal(aj) {
				return ai.After(aj)
			}
		}
		return insts[i].Order < insts[j].Order
	})
}
```

- [ ] **Step 4: Run the new test to verify it passes**

Run: `go test ./internal/session/ -run TestSortInstancesByActionable_CreationOrderDefault`
Expected: PASS

- [ ] **Step 5: Pin the #857 UI regression tests to actionable mode**

In `internal/ui/issue857_sort_actionable_test.go`, at the very start of the body of BOTH `TestSessionList_SortByActionable_RegressionFor857` and `TestSessionList_SortByActionable_TimestampTieBreak`, add:

```go
	session.SetGroupSortMode("actionable")
	t.Cleanup(func() { session.SetGroupSortMode("creation") })
```

(Insert directly after the opening `{` of each function, before `now := time.Now()`.)

- [ ] **Step 6: Pin the maestro actionable test**

In `internal/session/maestro_test.go`, at the start of `TestSortInstancesByActionable_MaestroFirst` (which asserts non-maestro rows keep actionable order), add:

```go
	SetGroupSortMode("actionable")
	t.Cleanup(func() { SetGroupSortMode("creation") })
```

(Insert directly after the opening `{`, before `now := time.Now()`.)

- [ ] **Step 7: Run the full suite and pin any remaining actionable-order failures**

Run: `go test -count=1 ./internal/session/... ./internal/ui/...`
Expected: PASS. If any *other* test fails because it asserts status/recency normal-band order (it will show a reordered `got` vs `want`), add the same two lines at the start of that test:

```go
	SetGroupSortMode("actionable")              // session package
	t.Cleanup(func() { SetGroupSortMode("creation") })
```

or, in the `ui` package, the `session.`-qualified form from Step 5. Do **not** add these to tests that pass — most pin/timestamp tests are unaffected.

- [ ] **Step 8: Commit**

```bash
git add internal/session/groups.go internal/session/groups_test.go \
        internal/ui/issue857_sort_actionable_test.go internal/session/maestro_test.go
git commit -m "feat(session): default within-group sort to creation order (group_sort toggle)"
```

---

## Task 5: Deterministic orphan sub-session ordering in Flatten

**Files:**
- Modify: `internal/session/groups.go` (`Flatten`, orphan tail at lines 617-630)
- Test: `internal/session/groups_test.go`

- [ ] **Step 1: Write the failing determinism test**

Add to `internal/session/groups_test.go`:

```go
func TestFlatten_OrphanSubSessionsDeterministic(t *testing.T) {
	t.Cleanup(func() { SetGroupSortMode("creation") })
	SetGroupSortMode("creation")

	// Three sub-sessions whose parent lives in a DIFFERENT group than they do,
	// so they render as orphaned top-level rows in group "g". Their Order
	// values fix the expected display order.
	mk := func(id string, order int) *Instance {
		return &Instance{ID: id, Title: id, GroupPath: "g", Order: order, ParentSessionID: "absent-parent"}
	}
	instances := []*Instance{mk("s0", 0), mk("s1", 1), mk("s2", 2)}

	tree := NewGroupTree(instances)

	var first []string
	for _, it := range tree.Flatten() {
		if it.Type == ItemTypeSession {
			first = append(first, it.Session.ID)
		}
	}
	if !equalStrings(first, []string{"s0", "s1", "s2"}) {
		t.Fatalf("orphan order = %v, want [s0 s1 s2]", first)
	}
	// Repeat many times — map-iteration nondeterminism would surface a
	// different order on some iteration.
	for i := 0; i < 50; i++ {
		var got []string
		for _, it := range tree.Flatten() {
			if it.Type == ItemTypeSession {
				got = append(got, it.Session.ID)
			}
		}
		if !equalStrings(got, first) {
			t.Fatalf("Flatten order not stable across calls: iter %d got %v, want %v", i, got, first)
		}
	}
}
```

> Note: `IsSubSession()` returns true when `ParentSessionID != ""`. Since `"absent-parent"` is not present in group `g`, these rows take the orphan path in `Flatten`.

- [ ] **Step 2: Run test to verify it fails (or flakes)**

Run: `go test ./internal/session/ -run TestFlatten_OrphanSubSessionsDeterministic -count=20`
Expected: FAIL on at least one run — orphans are emitted in random Go map order.

- [ ] **Step 3: Make orphan emission deterministic**

In `Flatten`, replace the orphan tail (currently):

```go
			// Add any orphaned sub-sessions (parent not in this group)
			for _, subs := range subSessionsByParent {
				for _, sub := range subs {
					topLevelIndex++
					items = append(items, Item{
						Type:          ItemTypeSession,
						Session:       sub,
						Level:         groupLevel + 1,
						Path:          group.Path,
						IsLastInGroup: topLevelIndex == topLevelCount,
						IsSubSession:  true, // Still a sub-session, just orphaned in this group
					})
				}
			}
```

with a version that flattens the remaining map into a slice and sorts by `Order`
before emitting, so the order is independent of Go's randomized map iteration:

```go
			// Add any orphaned sub-sessions (parent not in this group). Collect
			// the remaining map entries into a slice and sort by Order so the
			// emission order is deterministic — iterating subSessionsByParent
			// directly would use Go's randomized map order and shuffle these
			// rows between renders.
			orphans := make([]*Instance, 0, len(subSessionsByParent))
			for _, subs := range subSessionsByParent {
				orphans = append(orphans, subs...)
			}
			sort.SliceStable(orphans, func(i, j int) bool {
				return orphans[i].Order < orphans[j].Order
			})
			for _, sub := range orphans {
				topLevelIndex++
				items = append(items, Item{
					Type:          ItemTypeSession,
					Session:       sub,
					Level:         groupLevel + 1,
					Path:          group.Path,
					IsLastInGroup: topLevelIndex == topLevelCount,
					IsSubSession:  true, // Still a sub-session, just orphaned in this group
				})
			}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/session/ -run TestFlatten_OrphanSubSessionsDeterministic -count=50`
Expected: PASS on every iteration.

- [ ] **Step 5: Commit**

```bash
git add internal/session/groups.go internal/session/groups_test.go
git commit -m "fix(session): deterministic orphan sub-session order in Flatten"
```

---

## Task 6: Documentation (CHANGELOG + config reference)

**Files:**
- Modify: `CHANGELOG.md` (`## [Unreleased]` section, top of file ~line 8)
- Modify: `skills/agent-deck/references/config-reference.md` (Top-Level section ~lines 29-39)

- [ ] **Step 1: Add the CHANGELOG entry**

Under `## [Unreleased]` (replace the empty section), add:

```markdown
## [Unreleased]

### Added

- **`group_sort` config option to choose within-group session ordering.** Sessions inside a group now display in **creation order by default** (honoring the `K`/`J` manual reorder), instead of the status/recency "actionable" sort. Set `group_sort = "actionable"` in `config.toml` to restore the issue [#857](https://github.com/asheshgoplani/agent-deck/issues/857) most-recently-actionable-first behavior. Pin-top/pin-bottom and the Maestro supervisor still surface as before in both modes.

### Fixed

- **Orphaned sub-sessions no longer shuffle position between renders.** Sub-sessions whose parent session lives in a different group were emitted in Go's randomized map-iteration order, so they jumped around on each redraw. They now render in a stable order (by persisted `Order`).
```

- [ ] **Step 2: Document `group_sort` in the config reference**

In `skills/agent-deck/references/config-reference.md`, in the Top-Level example block (~line 32) add a line:

```toml
group_sort   = "creation"  # within-group order: "creation" (default) or "actionable"
```

and add a row to the Top-Level table (after the `sync_title` row, ~line 39):

```markdown
| `group_sort` | string | `"creation"` | Order of sessions within a group. `"creation"` (default) keeps the order sessions were created in, and respects the `K`/`J` manual reorder. `"actionable"` restores the issue #857 sort that surfaces the most recently actionable sessions (error → waiting → running → idle → stopped, then recency) to the top of each group. Pin and Maestro rows are unaffected by this setting. |
```

- [ ] **Step 3: Commit**

```bash
git add CHANGELOG.md skills/agent-deck/references/config-reference.md
git commit -m "docs: document group_sort config and orphan-order fix"
```

---

## Task 7: Full local CI gate (definition of done)

**Files:** none (verification only)

- [ ] **Step 1: Format and vet**

Run: `go fmt ./... && go vet ./internal/session/... ./internal/ui/... ./internal/web/...`
Expected: no diff from `go fmt`, no vet errors.

- [ ] **Step 2: Lint**

Run: `golangci-lint run`
Expected: no findings in the changed files.

- [ ] **Step 3: Race-enabled test suite**

Run: `go test -race -count=1 ./...`
Expected: PASS (the global `groupSortMode` is exercised by serial order tests with `t.Cleanup` resets; no test in these packages uses `t.Parallel`).

- [ ] **Step 4: Build**

Run: `go build -o /dev/null ./cmd/agent-deck/`
Expected: success.

- [ ] **Step 5 (optional): Full local CI**

Run: `make ci`
Expected: all lefthook `pre-push` commands pass (css-verify, lint, build, test, release-tests YAML lint).

---

## Self-Review Notes

- **Spec coverage:** config field (Task 1), cached mode (Task 2), wiring (Task 3), normal-band creation-order default + actionable toggle (Task 4), orphan determinism (Task 5), CHANGELOG + docs (Task 6), `make ci` DoD (Task 7). Pins/Maestro left untouched per spec.
- **Known actionable-order tests pinned:** the two `issue857_sort_actionable_test.go` tests and `TestSortInstancesByActionable_MaestroFirst`; Task 4 Step 7 sweeps any other suite failures via the same one-liner.
- **Global-state safety:** every test that sets the mode also registers `t.Cleanup(func(){ SetGroupSortMode("creation") })`; none of the affected files use `t.Parallel`.
