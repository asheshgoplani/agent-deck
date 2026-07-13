# Context Budget: Per-Session Token Warnings + Autonomous Handoff — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an absolute-token context budget that escalates warnings at 150k/200k/250k for every session, and for autonomous sessions performs a graceful fork-to-new-session handoff before 250k so no work is lost to auto-compaction.

**Architecture:** Pure, table-testable core in `internal/session` (config parse, threshold→level mapping, handoff state machine) consumed by thin adapters in `internal/ui` that wire into the existing 2s `backgroundStatusUpdate()` loop. Persistence rides the existing `tool_data` JSON blob via targeted `json_set`/`json_extract` UPDATEs — no schema-version bump. The state machine sees only injected values (tokens, file-exists, idle, clock), so it is unit-testable without tmux or a live agent.

**Tech Stack:** Go, BurntSushi/toml, SQLite (`modernc.org/sqlite` via `internal/statedb`), Bubble Tea + lipgloss TUI.

## Global Constraints

- Module import prefix is `github.com/asheshgoplani/agent-deck` (verbatim) — every new import uses it.
- LOCAL schema version is currently **13** and **MUST NOT** be bumped by this work — handoff state is persisted in the existing `tool_data` TEXT column. (See memory: schema-version-divergence.)
- All thresholds measure against `SessionAnalytics.CurrentContextTokens` (last turn input + cache-read), **never** cumulative `InputTokens`.
- Level boundaries are inclusive at the lower bound: exactly 150000 = `warn`, 149999 = `normal`.
- Default values, verbatim: `enabled=true`, `warn_tokens=150000`, `high_tokens=200000`, `ceiling_tokens=250000`, `autonomous_handoff=true`, `handoff_timeout_seconds=300`.
- The failsafe **never** auto-`/clear`s — it pauses (stops) the session and raises the loudest alert.
- Every actuation (warning notification, wrap-up injection) fires **once per crossing**, debounced like the existing conductor `clearOnCompactSent` map — never every 2s tick.
- Config writes go through the existing `SaveUserConfig`/atomic-write safeguards; per-session state writes go through targeted single-row UPDATEs (never full-table `SaveInstances`), per the archive-save-abort hazard.
- **Test env caveat (this sandbox):** `internal/session` JSONL+python3 tests, `internal/ui` zoxide tests, and `internal/tmux` PTY tests fail regardless of changes. ALWAYS run new tests with a targeted `-run <regex>` so a pre-existing flaky failure is never attributed to this work. Reconcile against a clean checkout before blaming your diff.

---

## File Structure

| File | Responsibility |
|------|----------------|
| `internal/session/userconfig.go` (modify) | Add `ContextBudgetSettings` struct, the `UserConfig.ContextBudget` field, and the `GetContextBudget()` accessor (pointer-bool default pattern). |
| `internal/session/context_budget.go` (create) | `BudgetLevel` type + constants + `(*SessionAnalytics).BudgetLevel(cfg)` threshold→level mapping. Pure. |
| `internal/session/context_handoff.go` (create) | `HandoffState`/`HandoffAction` enums, `HandoffInputs`/`HandoffDecision`, pure `NextHandoffState(...)`. No I/O. |
| `internal/statedb/statedb.go` (modify) | `WriteHandoffState`/`ReadHandoffState` targeted `tool_data` JSON updates (mirror `WriteClaudeSessionBinding`). |
| `internal/ui/analytics_panel.go` (modify) | Budget-aware context bar/label driven by `BudgetLevel`. |
| `internal/ui/home.go` (modify) | Session-list budget badge in `renderSessionItem`; warning + handoff evaluation wired into `backgroundStatusUpdate()`; in-memory state maps; continuation-fork + failsafe adapters. |
| `internal/ui/context_budget_ui.go` (create) | The warning/handoff evaluation methods on `*Home` (keeps `home.go` churn contained; same package). |

Tasks 1–4 deliver the **warnings** increment (shippable on its own). Tasks 5–8 add the **autonomous handoff**.

---

### Task 1: Context-budget config section

**Files:**
- Modify: `internal/session/userconfig.go` (add struct near `NotificationsConfig` ~line 979; add field to `UserConfig` near line 172; add accessor near `GetConductor`-style accessors)
- Test: `internal/session/context_budget_config_test.go` (create)

**Interfaces:**
- Produces:
  - `type ContextBudgetSettings struct { Enabled *bool; WarnTokens int; HighTokens int; CeilingTokens int; AutonomousHandoff *bool; HandoffTimeoutSeconds int }`
  - `func (c ContextBudgetSettings) GetEnabled() bool`
  - `func (c ContextBudgetSettings) GetAutonomousHandoff() bool`
  - `func (c *UserConfig) GetContextBudget() ContextBudgetSettings` — returns a copy with all zero/nil fields defaulted.

- [ ] **Step 1: Write the failing test**

Create `internal/session/context_budget_config_test.go`:

```go
package session

import "testing"

func TestGetContextBudget_DefaultsWhenUnset(t *testing.T) {
	c := &UserConfig{} // no [context_budget] section present
	got := c.GetContextBudget()

	if !got.GetEnabled() {
		t.Errorf("Enabled default = false, want true")
	}
	if !got.GetAutonomousHandoff() {
		t.Errorf("AutonomousHandoff default = false, want true")
	}
	if got.WarnTokens != 150000 {
		t.Errorf("WarnTokens = %d, want 150000", got.WarnTokens)
	}
	if got.HighTokens != 200000 {
		t.Errorf("HighTokens = %d, want 200000", got.HighTokens)
	}
	if got.CeilingTokens != 250000 {
		t.Errorf("CeilingTokens = %d, want 250000", got.CeilingTokens)
	}
	if got.HandoffTimeoutSeconds != 300 {
		t.Errorf("HandoffTimeoutSeconds = %d, want 300", got.HandoffTimeoutSeconds)
	}
}

func TestGetContextBudget_RespectsExplicitValues(t *testing.T) {
	disabled := false
	c := &UserConfig{ContextBudget: ContextBudgetSettings{
		Enabled:               &disabled,
		WarnTokens:            100000,
		HighTokens:            120000,
		CeilingTokens:         140000,
		HandoffTimeoutSeconds: 60,
	}}
	got := c.GetContextBudget()

	if got.GetEnabled() {
		t.Errorf("Enabled = true, want false (explicit)")
	}
	if got.WarnTokens != 100000 || got.HighTokens != 120000 || got.CeilingTokens != 140000 {
		t.Errorf("explicit thresholds not preserved: %+v", got)
	}
	if got.HandoffTimeoutSeconds != 60 {
		t.Errorf("HandoffTimeoutSeconds = %d, want 60", got.HandoffTimeoutSeconds)
	}
	// AutonomousHandoff left nil -> defaults to true even when other fields explicit.
	if !got.GetAutonomousHandoff() {
		t.Errorf("AutonomousHandoff default = false, want true")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/session/ -run TestGetContextBudget -v`
Expected: FAIL — `undefined: ContextBudgetSettings` and `c.ContextBudget`.

- [ ] **Step 3: Add the struct, field, and accessor**

In `internal/session/userconfig.go`, add the field to the `UserConfig` struct (next to `Notifications NotificationsConfig` around line 172):

```go
	ContextBudget ContextBudgetSettings `toml:"context_budget,omitempty"`
```

Add the struct + accessors (place near `NotificationsConfig`, ~line 979). The pointer-bool pattern matches `MCPPoolSettings.GetAutoStart` (defaults that should be `true` when unset); int fields use `omitzero` like `MCPPoolSettings`:

```go
// ContextBudgetSettings configures absolute-token context warnings and the
// autonomous fork-on-budget handoff. Thresholds measure CurrentContextTokens
// (last-turn input + cache-read), i.e. real context-window occupancy.
type ContextBudgetSettings struct {
	// Enabled turns the whole feature on (default: true). Pointer so an unset
	// section still defaults to enabled.
	Enabled *bool `toml:"enabled,omitempty"`
	// WarnTokens is the soft-warning threshold (default: 150000).
	WarnTokens int `toml:"warn_tokens,omitzero"`
	// HighTokens is the loud-warning + autonomous wrap-up trigger (default: 200000).
	HighTokens int `toml:"high_tokens,omitzero"`
	// CeilingTokens is the hard ceiling that must not be crossed (default: 250000).
	CeilingTokens int `toml:"ceiling_tokens,omitzero"`
	// AutonomousHandoff enables the fork-new-session handoff on autonomous
	// sessions (default: true). Pointer for the same unset-defaults-true reason.
	AutonomousHandoff *bool `toml:"autonomous_handoff,omitempty"`
	// HandoffTimeoutSeconds is the failsafe window for the wrap-up to produce
	// its PROMPT.md (default: 300).
	HandoffTimeoutSeconds int `toml:"handoff_timeout_seconds,omitzero"`
}

// GetEnabled returns Enabled, defaulting to true when unset.
func (c ContextBudgetSettings) GetEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// GetAutonomousHandoff returns AutonomousHandoff, defaulting to true when unset.
func (c ContextBudgetSettings) GetAutonomousHandoff() bool {
	if c.AutonomousHandoff == nil {
		return true
	}
	return *c.AutonomousHandoff
}

// GetContextBudget returns the context-budget settings with zero/nil fields
// filled in with documented defaults. Mirrors the GetConductor accessor.
func (c *UserConfig) GetContextBudget() ContextBudgetSettings {
	cfg := c.ContextBudget
	if cfg.WarnTokens == 0 {
		cfg.WarnTokens = 150000
	}
	if cfg.HighTokens == 0 {
		cfg.HighTokens = 200000
	}
	if cfg.CeilingTokens == 0 {
		cfg.CeilingTokens = 250000
	}
	if cfg.HandoffTimeoutSeconds == 0 {
		cfg.HandoffTimeoutSeconds = 300
	}
	return cfg
}
```

> Note: if `GetConductor()` in this file takes `c.mu.RLock()`, match it — copy the lock/unlock lines from `GetConductor` into `GetContextBudget`. The exploration found two accessor flavors; follow whichever the surrounding accessors use.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/session/ -run TestGetContextBudget -v`
Expected: PASS (both tests).

- [ ] **Step 5: Verify build and vet**

Run: `go build ./... && go vet ./internal/session/`
Expected: no output (success).

- [ ] **Step 6: Commit**

```bash
git add internal/session/userconfig.go internal/session/context_budget_config_test.go
git commit -m "feat(context-budget): add [context_budget] config section + GetContextBudget()"
```

---

### Task 2: BudgetLevel threshold mapping

**Files:**
- Create: `internal/session/context_budget.go`
- Test: `internal/session/context_budget_test.go`

**Interfaces:**
- Consumes: `ContextBudgetSettings` (Task 1), `SessionAnalytics.CurrentContextTokens` (existing, `analytics.go:21`).
- Produces:
  - `type BudgetLevel int` with `BudgetNormal`, `BudgetWarn`, `BudgetHigh`, `BudgetOver`.
  - `func (l BudgetLevel) String() string`
  - `func BudgetLevelForTokens(tokens int, cfg ContextBudgetSettings) BudgetLevel`
  - `func (a *SessionAnalytics) BudgetLevel(cfg ContextBudgetSettings) BudgetLevel`

> Design note: `BudgetLevel` maps tokens→level only. The "no token signal" gate (non-Claude tools) is the **caller's** job — it checks `session.IsClaudeCompatible(tool)` and a non-nil `*SessionAnalytics` before calling. A brand-new Claude session at 0 tokens is legitimately `BudgetNormal`.

- [ ] **Step 1: Write the failing test**

Create `internal/session/context_budget_test.go`:

```go
package session

import "testing"

func boundaryCfg() ContextBudgetSettings {
	// Use the defaults via GetContextBudget on an empty config.
	return (&UserConfig{}).GetContextBudget()
}

func TestBudgetLevelForTokens_Boundaries(t *testing.T) {
	cfg := boundaryCfg()
	cases := []struct {
		tokens int
		want   BudgetLevel
	}{
		{0, BudgetNormal},
		{149999, BudgetNormal},
		{150000, BudgetWarn},
		{199999, BudgetWarn},
		{200000, BudgetHigh},
		{249999, BudgetHigh},
		{250000, BudgetOver},
		{500000, BudgetOver},
	}
	for _, tc := range cases {
		if got := BudgetLevelForTokens(tc.tokens, cfg); got != tc.want {
			t.Errorf("BudgetLevelForTokens(%d) = %v, want %v", tc.tokens, got, tc.want)
		}
	}
}

func TestSessionAnalytics_BudgetLevel(t *testing.T) {
	cfg := boundaryCfg()
	a := &SessionAnalytics{CurrentContextTokens: 200000}
	if got := a.BudgetLevel(cfg); got != BudgetHigh {
		t.Errorf("BudgetLevel = %v, want BudgetHigh", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/session/ -run 'TestBudgetLevel|TestSessionAnalytics_BudgetLevel' -v`
Expected: FAIL — `undefined: BudgetLevelForTokens` / `BudgetNormal`.

- [ ] **Step 3: Write the implementation**

Create `internal/session/context_budget.go`:

```go
package session

// BudgetLevel classifies a session's context-window occupancy against the
// configured absolute-token thresholds. Lower bounds are inclusive.
type BudgetLevel int

const (
	// BudgetNormal: tokens < WarnTokens.
	BudgetNormal BudgetLevel = iota
	// BudgetWarn: WarnTokens <= tokens < HighTokens.
	BudgetWarn
	// BudgetHigh: HighTokens <= tokens < CeilingTokens.
	BudgetHigh
	// BudgetOver: tokens >= CeilingTokens.
	BudgetOver
)

func (l BudgetLevel) String() string {
	switch l {
	case BudgetWarn:
		return "warn"
	case BudgetHigh:
		return "high"
	case BudgetOver:
		return "over"
	default:
		return "normal"
	}
}

// BudgetLevelForTokens maps an absolute context-token count to a BudgetLevel
// using inclusive lower bounds (exactly WarnTokens => warn).
func BudgetLevelForTokens(tokens int, cfg ContextBudgetSettings) BudgetLevel {
	switch {
	case tokens >= cfg.CeilingTokens:
		return BudgetOver
	case tokens >= cfg.HighTokens:
		return BudgetHigh
	case tokens >= cfg.WarnTokens:
		return BudgetWarn
	default:
		return BudgetNormal
	}
}

// BudgetLevel returns the budget level for this session's current context-window
// occupancy. Callers must first confirm a usable token signal exists (Claude-
// compatible tool + non-nil analytics); a zero CurrentContextTokens maps to
// BudgetNormal.
func (a *SessionAnalytics) BudgetLevel(cfg ContextBudgetSettings) BudgetLevel {
	return BudgetLevelForTokens(a.CurrentContextTokens, cfg)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/session/ -run 'TestBudgetLevel|TestSessionAnalytics_BudgetLevel' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/session/context_budget.go internal/session/context_budget_test.go
git commit -m "feat(context-budget): add BudgetLevel threshold mapping"
```

---

### Task 3: Budget-aware context bar in the analytics panel

**Files:**
- Modify: `internal/ui/analytics_panel.go` (`renderContextBar` ~line 340-389)
- Test: `internal/ui/context_budget_panel_test.go` (create)

**Interfaces:**
- Consumes: `BudgetLevel` (Task 2), `session.SessionAnalytics`.
- Produces: `func budgetBarColor(level session.BudgetLevel) lipgloss.Color` and an updated `renderContextBar` that, when a budget level is supplied, colors by absolute level instead of the 60/80% breakpoints.

> The existing `renderContextBar(percent float64, width int)` colors by percent. We add a level-aware sibling so the panel reflects absolute tokens. Keep the percent-only function working (other callers) but route the Claude context section through the level-aware color.

- [ ] **Step 1: Write the failing test**

Create `internal/ui/context_budget_panel_test.go`:

```go
package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestBudgetBarColor(t *testing.T) {
	cases := []struct {
		level session.BudgetLevel
		want  string // hex of expected color
	}{
		{session.BudgetNormal, string(ColorGreen)},
		{session.BudgetWarn, string(ColorYellow)},
		{session.BudgetHigh, string(ColorRed)},
		{session.BudgetOver, string(ColorRed)},
	}
	for _, tc := range cases {
		if got := string(budgetBarColor(tc.level)); got != tc.want {
			t.Errorf("budgetBarColor(%v) = %s, want %s", tc.level, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ui/ -run TestBudgetBarColor -v`
Expected: FAIL — `undefined: budgetBarColor`.

- [ ] **Step 3: Implement the level→color helper and wire the bar**

In `internal/ui/analytics_panel.go`, add the helper (near `renderContextBar`):

```go
// budgetBarColor maps an absolute-token BudgetLevel to a bar color. Warn is
// yellow; high and over are both red (over is distinguished by the louder
// label/banner, not the bar color).
func budgetBarColor(level session.BudgetLevel) lipgloss.Color {
	switch level {
	case session.BudgetWarn:
		return ColorYellow
	case session.BudgetHigh, session.BudgetOver:
		return ColorRed
	default:
		return ColorGreen
	}
}
```

Then, in the Claude context section that calls `renderContextBar(percent, barWidth)` (the exploration located this in `renderModelContextSection` / the panel's Claude branch), color the bar by budget level. Locate the block that computes `percent := analytics.ContextPercent(...)` and renders the bar, and change the bar color source. Concretely, add a `barColor` derived from the level and pass it through. The minimal change: introduce a level-aware variant and call it. Replace the Claude bar render call with:

```go
	cfg := session.GetContextBudgetSettings() // see note below
	level := analytics.BudgetLevel(cfg)
	contextBar := renderContextBarColored(percent, barWidth, budgetBarColor(level))
```

And add `renderContextBarColored` next to `renderContextBar` (extract the existing body, parameterizing the color):

```go
// renderContextBarColored renders the bar with an explicit fill color (used by
// the absolute-token budget path). renderContextBar delegates here using the
// percent-based color for non-budget callers.
func renderContextBarColored(percent float64, width int, barColor lipgloss.Color) string {
	if width < 10 {
		width = 10
	}
	filledWidth := int(float64(width) * percent / 100)
	if filledWidth > width {
		filledWidth = width
	}
	if filledWidth < 0 {
		filledWidth = 0
	}
	emptyWidth := width - filledWidth
	barStyle := lipgloss.NewStyle().Foreground(barColor)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	return barStyle.Render(strings.Repeat("█", filledWidth)) +
		dimStyle.Render(strings.Repeat("░", emptyWidth))
}
```

Update the original `renderContextBar(percent, width)` body to delegate, preserving its current percent→color behavior for any other callers:

```go
func renderContextBar(percent float64, width int) string {
	var barColor lipgloss.Color
	switch {
	case percent < 60:
		barColor = ColorGreen
	case percent < 80:
		barColor = ColorYellow
	default:
		barColor = ColorRed
	}
	return renderContextBarColored(percent, width, barColor)
}
```

> Note on `session.GetContextBudgetSettings()`: add a tiny package-level convenience in `internal/session/context_budget.go` so UI code doesn't reload config each render:
> ```go
> // GetContextBudgetSettings loads the user config (cached) and returns the
> // context-budget settings with defaults applied. Convenience for UI callers.
> func GetContextBudgetSettings() ContextBudgetSettings {
> 	cfg, err := LoadUserConfig()
> 	if err != nil || cfg == nil {
> 		return (&UserConfig{}).GetContextBudget()
> 	}
> 	return cfg.GetContextBudget()
> }
> ```
> Add this to Task 2's file. `LoadUserConfig()` is mtime-cached, so per-render calls are cheap.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/ui/ -run TestBudgetBarColor -v`
Expected: PASS.

- [ ] **Step 5: Verify build/vet**

Run: `go build ./... && go vet ./internal/ui/`
Expected: success.

- [ ] **Step 6: Commit**

```bash
git add internal/session/context_budget.go internal/ui/analytics_panel.go internal/ui/context_budget_panel_test.go
git commit -m "feat(context-budget): color analytics context bar by absolute budget level"
```

---

### Task 4: Session-list budget badge

**Files:**
- Modify: `internal/ui/home.go` (`renderSessionItem` ~line 14656; badge assembly ~14900-14984)
- Test: `internal/ui/context_budget_badge_test.go` (create)

**Interfaces:**
- Consumes: `session.BudgetLevel` (Task 2), `h.getAnalyticsForSession(inst)` (existing, returns nil on cache miss).
- Produces: `func budgetBadge(level session.BudgetLevel, selected bool) string` — returns `""` for `BudgetNormal`, otherwise a colored ` [ctx:warn|high|over]` chip.

- [ ] **Step 1: Write the failing test**

Create `internal/ui/context_budget_badge_test.go`:

```go
package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestBudgetBadge(t *testing.T) {
	if got := budgetBadge(session.BudgetNormal, false); got != "" {
		t.Errorf("normal badge = %q, want empty", got)
	}
	for _, lvl := range []session.BudgetLevel{session.BudgetWarn, session.BudgetHigh, session.BudgetOver} {
		got := budgetBadge(lvl, false)
		if got == "" {
			t.Errorf("level %v produced empty badge", lvl)
		}
		if !strings.Contains(got, lvl.String()) {
			t.Errorf("badge %q does not contain level name %q", got, lvl.String())
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ui/ -run TestBudgetBadge -v`
Expected: FAIL — `undefined: budgetBadge`.

- [ ] **Step 3: Implement the badge + wire it into the row**

In `internal/ui/home.go`, add the helper (near the other badge helpers / `renderSessionItem`):

```go
// budgetBadge renders the per-session context-budget chip for the session list.
// Empty for BudgetNormal so unaffected rows are unchanged.
func budgetBadge(level session.BudgetLevel, selected bool) string {
	if level == session.BudgetNormal {
		return ""
	}
	color := ColorYellow
	if level == session.BudgetHigh || level == session.BudgetOver {
		color = ColorRed
	}
	style := lipgloss.NewStyle().Foreground(color).Bold(true)
	if selected {
		style = SessionStatusSelStyle
	}
	return style.Render(" [ctx:" + level.String() + "]")
}
```

In `renderSessionItem`, compute the level near the other badges (e.g. just before `timestampBadge` ~line 14900). Only Claude-compatible tools with cached analytics get a badge (the no-signal gate):

```go
	ctxBadge := ""
	if session.IsClaudeCompatible(inst.Tool) {
		if a := h.getAnalyticsForSession(inst); a != nil {
			ctxBadge = budgetBadge(a.BudgetLevel(session.GetContextBudgetSettings()), selected)
		}
	}
```

Add `ctxBadge` to the row `fmt.Sprintf` format and argument list (the assembly at ~14967). Append `%s` after `timestampBadge`'s verb and pass `ctxBadge` as the final argument. Also include its width in the `cellWidth(...)` budget accumulation at ~14954:

```go
	... + cellWidth(timestampBadge) + cellWidth(ctxBadge)
```

> Confirm the exact format string and arg order by reading the `row := fmt.Sprintf(` block at home.go:14967 before editing — append, don't reorder existing verbs.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/ui/ -run TestBudgetBadge -v`
Expected: PASS.

- [ ] **Step 5: Verify build/vet**

Run: `go build ./... && go vet ./internal/ui/`
Expected: success.

- [ ] **Step 6: Commit**

```bash
git add internal/ui/home.go internal/ui/context_budget_badge_test.go
git commit -m "feat(context-budget): show per-session budget badge in session list"
```

> **Warnings increment is now shippable.** Tasks 5–8 add the autonomous handoff.

---

### Task 5: Handoff state machine (pure)

**Files:**
- Create: `internal/session/context_handoff.go`
- Test: `internal/session/context_handoff_test.go`

**Interfaces:**
- Consumes: `ContextBudgetSettings` (Task 1).
- Produces:
  - `type HandoffState string` with `HandoffNormal=""`, `HandoffWrapRequested="wrap_requested"`, `HandoffWaiting="wait_handoff"`, `HandoffDone="done"`, `HandoffFailsafe="failsafe"`.
  - `type HandoffAction int` with `ActionNone`, `ActionRequestWrap`, `ActionFork`, `ActionFailsafe`.
  - `type HandoffInputs struct { Tokens int; PromptReady bool; AgentIdle bool; Now time.Time; TriggeredAt time.Time }`
  - `type HandoffDecision struct { Next HandoffState; Action HandoffAction }`
  - `func NextHandoffState(cur HandoffState, in HandoffInputs, cfg ContextBudgetSettings) HandoffDecision`

State transitions (mirrors the design's diagram):
- `HandoffNormal`: tokens ≥ HighTokens → `{HandoffWrapRequested, ActionRequestWrap}`; else `{HandoffNormal, ActionNone}`.
- `HandoffWrapRequested` / `HandoffWaiting`:
  - ceiling crossed (tokens ≥ CeilingTokens) OR timeout elapsed → `{HandoffFailsafe, ActionFailsafe}`.
  - else PromptReady AND AgentIdle → `{HandoffDone, ActionFork}`.
  - else → `{HandoffWaiting, ActionNone}` (normalizes WrapRequested→Waiting after the one-time wrap injection).
- `HandoffDone` / `HandoffFailsafe` (terminal): `{cur, ActionNone}`.
- timeout elapsed := `!TriggeredAt.IsZero() && cfg.HandoffTimeoutSeconds > 0 && Now.Sub(TriggeredAt) >= cfg.HandoffTimeoutSeconds * time.Second`.

- [ ] **Step 1: Write the failing test**

Create `internal/session/context_handoff_test.go`:

```go
package session

import (
	"testing"
	"time"
)

func handoffCfg() ContextBudgetSettings {
	return (&UserConfig{}).GetContextBudget() // high=200000, ceiling=250000, timeout=300s
}

func TestNextHandoffState_Table(t *testing.T) {
	cfg := handoffCfg()
	base := time.Unix(1_000_000, 0)
	trig := base.Add(-10 * time.Second) // 10s into wrap

	cases := []struct {
		name       string
		cur        HandoffState
		in         HandoffInputs
		wantNext   HandoffState
		wantAction HandoffAction
	}{
		{"normal stays normal below high", HandoffNormal,
			HandoffInputs{Tokens: 199999, Now: base}, HandoffNormal, ActionNone},
		{"normal triggers wrap at high", HandoffNormal,
			HandoffInputs{Tokens: 200000, Now: base}, HandoffWrapRequested, ActionRequestWrap},
		{"wrap waits when not ready", HandoffWrapRequested,
			HandoffInputs{Tokens: 210000, Now: base, TriggeredAt: trig}, HandoffWaiting, ActionNone},
		{"waiting forks when ready+idle", HandoffWaiting,
			HandoffInputs{Tokens: 210000, PromptReady: true, AgentIdle: true, Now: base, TriggeredAt: trig},
			HandoffDone, ActionFork},
		{"waiting holds when ready but busy", HandoffWaiting,
			HandoffInputs{Tokens: 210000, PromptReady: true, AgentIdle: false, Now: base, TriggeredAt: trig},
			HandoffWaiting, ActionNone},
		{"ceiling crossed -> failsafe", HandoffWaiting,
			HandoffInputs{Tokens: 250000, PromptReady: false, Now: base, TriggeredAt: trig},
			HandoffFailsafe, ActionFailsafe},
		{"timeout -> failsafe", HandoffWaiting,
			HandoffInputs{Tokens: 210000, PromptReady: false, Now: base, TriggeredAt: base.Add(-301 * time.Second)},
			HandoffFailsafe, ActionFailsafe},
		{"done is terminal", HandoffDone,
			HandoffInputs{Tokens: 300000, Now: base}, HandoffDone, ActionNone},
		{"failsafe is terminal", HandoffFailsafe,
			HandoffInputs{Tokens: 300000, Now: base}, HandoffFailsafe, ActionNone},
	}
	for _, tc := range cases {
		got := NextHandoffState(tc.cur, tc.in, cfg)
		if got.Next != tc.wantNext || got.Action != tc.wantAction {
			t.Errorf("%s: NextHandoffState = {%v,%v}, want {%v,%v}",
				tc.name, got.Next, got.Action, tc.wantNext, tc.wantAction)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/session/ -run TestNextHandoffState -v`
Expected: FAIL — `undefined: HandoffState` etc.

- [ ] **Step 3: Implement the state machine**

Create `internal/session/context_handoff.go`:

```go
package session

import "time"

// HandoffState is the persisted per-session position in the autonomous handoff
// state machine. The zero value ("") is HandoffNormal so an unset session is
// implicitly normal.
type HandoffState string

const (
	HandoffNormal        HandoffState = ""
	HandoffWrapRequested HandoffState = "wrap_requested"
	HandoffWaiting       HandoffState = "wait_handoff"
	HandoffDone          HandoffState = "done"
	HandoffFailsafe      HandoffState = "failsafe"
)

// HandoffAction is the side effect the caller must perform on a transition.
type HandoffAction int

const (
	ActionNone HandoffAction = iota
	// ActionRequestWrap: create the handoff dir, inject the wrap-up instruction,
	// pause new work, and record TriggeredAt = now.
	ActionRequestWrap
	// ActionFork: read PROMPT.md, fork the continuation session, archive the old one.
	ActionFork
	// ActionFailsafe: pause/stop the old session and raise the loudest alert.
	ActionFailsafe
)

// HandoffInputs are the injected observations the pure state machine reasons
// over. No I/O happens inside the machine.
type HandoffInputs struct {
	Tokens      int       // CurrentContextTokens
	PromptReady bool      // handoff/<id>/PROMPT.md exists
	AgentIdle   bool      // session is waiting/idle (not actively generating)
	Now         time.Time // current clock
	TriggeredAt time.Time // when WRAP_REQUESTED was entered (zero before that)
}

// HandoffDecision is the machine's output for one tick.
type HandoffDecision struct {
	Next   HandoffState
	Action HandoffAction
}

func timeoutElapsed(in HandoffInputs, cfg ContextBudgetSettings) bool {
	if in.TriggeredAt.IsZero() || cfg.HandoffTimeoutSeconds <= 0 {
		return false
	}
	return in.Now.Sub(in.TriggeredAt) >= time.Duration(cfg.HandoffTimeoutSeconds)*time.Second
}

// NextHandoffState advances the handoff state machine by one tick. Pure.
func NextHandoffState(cur HandoffState, in HandoffInputs, cfg ContextBudgetSettings) HandoffDecision {
	switch cur {
	case HandoffDone, HandoffFailsafe:
		return HandoffDecision{Next: cur, Action: ActionNone}

	case HandoffWrapRequested, HandoffWaiting:
		if in.Tokens >= cfg.CeilingTokens || timeoutElapsed(in, cfg) {
			return HandoffDecision{Next: HandoffFailsafe, Action: ActionFailsafe}
		}
		if in.PromptReady && in.AgentIdle {
			return HandoffDecision{Next: HandoffDone, Action: ActionFork}
		}
		return HandoffDecision{Next: HandoffWaiting, Action: ActionNone}

	default: // HandoffNormal
		if in.Tokens >= cfg.HighTokens {
			return HandoffDecision{Next: HandoffWrapRequested, Action: ActionRequestWrap}
		}
		return HandoffDecision{Next: HandoffNormal, Action: ActionNone}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/session/ -run TestNextHandoffState -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/session/context_handoff.go internal/session/context_handoff_test.go
git commit -m "feat(context-budget): add pure autonomous-handoff state machine"
```

---

### Task 6: Persist handoff state in tool_data

**Files:**
- Modify: `internal/statedb/statedb.go` (add methods near `WriteClaudeSessionBinding` ~line 1271)
- Test: `internal/statedb/context_handoff_state_test.go` (create)

**Interfaces:**
- Produces:
  - `func (s *StateDB) WriteHandoffState(id, state string, triggeredAt time.Time) error`
  - `func (s *StateDB) ReadHandoffState(id string) (state string, triggeredAt time.Time, err error)`

> Uses `json_set`/`json_extract` on the `tool_data` column — a targeted single-row UPDATE that bypasses the full-table `SaveInstances` external-change guard (archive-save-abort hazard) and survives full saves because `SaveInstances` merges extra `tool_data` keys via `MergeToolDataExtras`. No `MarshalToolData`/`UnmarshalToolData` signature changes, no schema bump.

- [ ] **Step 1: Write the failing test**

Create `internal/statedb/context_handoff_state_test.go`:

```go
package statedb

import (
	"testing"
	"time"
)

func TestHandoffState_RoundTrip(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	row := &InstanceRow{
		ID:          "sess-1",
		Title:       "worker",
		ProjectPath: "/tmp/p",
		GroupPath:   "my-sessions",
		Tool:        "claude",
		Status:      "running",
		CreatedAt:   time.Unix(1000, 0),
	}
	if err := db.SaveInstances([]*InstanceRow{row}); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}

	// Unset: empty state, zero time.
	gotState, gotAt, err := db.ReadHandoffState("sess-1")
	if err != nil {
		t.Fatalf("ReadHandoffState(unset): %v", err)
	}
	if gotState != "" || !gotAt.IsZero() {
		t.Errorf("unset = (%q,%v), want empty/zero", gotState, gotAt)
	}

	trig := time.Unix(1700000000, 0)
	if err := db.WriteHandoffState("sess-1", "wait_handoff", trig); err != nil {
		t.Fatalf("WriteHandoffState: %v", err)
	}
	gotState, gotAt, err = db.ReadHandoffState("sess-1")
	if err != nil {
		t.Fatalf("ReadHandoffState: %v", err)
	}
	if gotState != "wait_handoff" {
		t.Errorf("state = %q, want wait_handoff", gotState)
	}
	if gotAt.Unix() != trig.Unix() {
		t.Errorf("triggeredAt = %v, want %v", gotAt, trig)
	}
}

func TestHandoffState_SurvivesFullSave(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	row := &InstanceRow{ID: "s", Title: "t", ProjectPath: "/p", GroupPath: "g", Tool: "claude", Status: "running", CreatedAt: time.Unix(1, 0)}
	if err := db.SaveInstances([]*InstanceRow{row}); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}
	if err := db.WriteHandoffState("s", "wrap_requested", time.Unix(50, 0)); err != nil {
		t.Fatalf("WriteHandoffState: %v", err)
	}
	// A subsequent full-table save (e.g. status change) must not clobber the key.
	row.Status = "waiting"
	if err := db.SaveInstances([]*InstanceRow{row}); err != nil {
		t.Fatalf("SaveInstances#2: %v", err)
	}
	gotState, _, err := db.ReadHandoffState("s")
	if err != nil {
		t.Fatalf("ReadHandoffState: %v", err)
	}
	if gotState != "wrap_requested" {
		t.Errorf("state after full save = %q, want wrap_requested", gotState)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/statedb/ -run TestHandoffState -v`
Expected: FAIL — `undefined: WriteHandoffState`.

> If `TestHandoffState_SurvivesFullSave` fails because `MergeToolDataExtras` does not preserve the keys, that means the merge does not carry unknown keys; in that case keep `WriteHandoffState` but document that handoff state must be re-read each tick (it is — Task 8 reads it lazily) and remove the survives-full-save assertion. Run the round-trip test first to confirm the core write/read works regardless.

- [ ] **Step 3: Implement the targeted updates**

In `internal/statedb/statedb.go`, add near `WriteClaudeSessionBinding` (~line 1271). Mirror its `json_set` + `withBusyRetry` shape:

```go
// WriteHandoffState atomically updates the autonomous-handoff state and trigger
// time inside the tool_data JSON column for one instance, via a targeted
// json_set UPDATE. Like WriteClaudeSessionBinding it avoids a whole-row INSERT
// OR REPLACE so it cannot clobber concurrent writes to other tool_data fields,
// and it sidesteps the full-table save's external-change guard. A zero
// triggeredAt clears the trigger timestamp ($.handoff_triggered_at => 0).
func (s *StateDB) WriteHandoffState(id, state string, triggeredAt time.Time) error {
	var at int64
	if !triggeredAt.IsZero() {
		at = triggeredAt.Unix()
	}
	return withBusyRetry(func() error {
		_, err := s.db.Exec(
			`UPDATE instances
			   SET tool_data = json_set(
			         COALESCE(tool_data, '{}'),
			         '$.handoff_state', ?,
			         '$.handoff_triggered_at', ?)
			 WHERE id = ?`,
			state, at, id,
		)
		return err
	})
}

// ReadHandoffState returns the persisted handoff state and trigger time for an
// instance. An unset key yields ("", zero time, nil) so a fresh session reads
// as HandoffNormal.
func (s *StateDB) ReadHandoffState(id string) (string, time.Time, error) {
	var state sql.NullString
	var at sql.NullInt64
	err := s.db.QueryRow(
		`SELECT json_extract(tool_data, '$.handoff_state'),
		        json_extract(tool_data, '$.handoff_triggered_at')
		   FROM instances WHERE id = ?`,
		id,
	).Scan(&state, &at)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", time.Time{}, nil
		}
		return "", time.Time{}, err
	}
	var t time.Time
	if at.Valid && at.Int64 > 0 {
		t = time.Unix(at.Int64, 0)
	}
	return state.String, t, nil
}
```

> Confirm `database/sql` and `time` are already imported in `statedb.go` (they are — `sql.NullString`/`sql.NullInt64` and `time.Time` are used throughout). If `Close()` does not exist on `*StateDB`, replace `defer db.Close()` in the test with the package's teardown helper, or drop it for in-memory DBs.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/statedb/ -run TestHandoffState -v`
Expected: PASS (round-trip definitely; survives-full-save per the Step 2 note).

- [ ] **Step 5: Commit**

```bash
git add internal/statedb/statedb.go internal/statedb/context_handoff_state_test.go
git commit -m "feat(context-budget): persist handoff state in tool_data (targeted json_set)"
```

---

### Task 7: Warning evaluation wired into the background loop

**Files:**
- Create: `internal/ui/context_budget_ui.go`
- Modify: `internal/ui/home.go` (add debounce maps to the `Home` struct + init them; call the evaluator from `backgroundStatusUpdate()`)
- Test: `internal/ui/context_budget_eval_test.go` (create)

**Interfaces:**
- Consumes: `session.BudgetLevel`, `session.GetContextBudgetSettings`, `h.getAnalyticsForSession`.
- Produces:
  - `func (h *Home) budgetWarnState(inst *session.Instance, cfg session.ContextBudgetSettings) (session.BudgetLevel, bool)` — returns the level and whether a usable signal exists.
  - `func shouldNotifyBudgetCrossing(prev, cur session.BudgetLevel) bool` — pure: true only when crossing **up** into `BudgetHigh`/`BudgetOver` for the first time.

> The warning surface is: visual badge/bar (Tasks 3–4, already live) + a debounced one-shot structured-log/notification at the high/over crossings. We keep a per-session "last notified level" map so notifications fire once per upward crossing, mirroring `clearOnCompactSent`.

- [ ] **Step 1: Write the failing test**

Create `internal/ui/context_budget_eval_test.go`:

```go
package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestShouldNotifyBudgetCrossing(t *testing.T) {
	cases := []struct {
		prev, cur session.BudgetLevel
		want      bool
	}{
		{session.BudgetNormal, session.BudgetWarn, false}, // warn is bar/badge only
		{session.BudgetWarn, session.BudgetHigh, true},    // first cross into high
		{session.BudgetHigh, session.BudgetHigh, false},   // no re-fire
		{session.BudgetHigh, session.BudgetOver, true},    // escalate to over
		{session.BudgetOver, session.BudgetHigh, false},   // dropping back: no fire
		{session.BudgetHigh, session.BudgetWarn, false},   // dropping back
	}
	for _, tc := range cases {
		if got := shouldNotifyBudgetCrossing(tc.prev, tc.cur); got != tc.want {
			t.Errorf("shouldNotifyBudgetCrossing(%v,%v) = %v, want %v", tc.prev, tc.cur, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ui/ -run TestShouldNotifyBudgetCrossing -v`
Expected: FAIL — `undefined: shouldNotifyBudgetCrossing`.

- [ ] **Step 3: Implement the evaluator**

Create `internal/ui/context_budget_ui.go`:

```go
package ui

import (
	"log/slog"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// shouldNotifyBudgetCrossing reports whether a one-time notification should fire
// for an upward transition into BudgetHigh or BudgetOver. Warn is intentionally
// bar/badge-only; dropping back never notifies.
func shouldNotifyBudgetCrossing(prev, cur session.BudgetLevel) bool {
	if cur <= prev {
		return false
	}
	return cur == session.BudgetHigh || cur == session.BudgetOver
}

// budgetWarnState returns a session's budget level and whether a usable context
// token signal exists (Claude-compatible tool + cached analytics). When ok is
// false, callers must not warn or act.
func (h *Home) budgetWarnState(inst *session.Instance, cfg session.ContextBudgetSettings) (session.BudgetLevel, bool) {
	if !session.IsClaudeCompatible(inst.Tool) {
		return session.BudgetNormal, false
	}
	a := h.getAnalyticsForSession(inst)
	if a == nil {
		return session.BudgetNormal, false
	}
	return a.BudgetLevel(cfg), true
}

// evaluateContextBudgetWarnings runs once per background tick over all sessions,
// firing a debounced one-shot notification on each upward crossing into
// high/over. Visual treatments (bar/badge) are handled in render.
func (h *Home) evaluateContextBudgetWarnings(instances []*session.Instance) {
	cfg := session.GetContextBudgetSettings()
	if !cfg.GetEnabled() {
		return
	}
	for _, inst := range instances {
		level, ok := h.budgetWarnState(inst, cfg)
		if !ok {
			continue
		}
		prev := h.budgetLastLevel[inst.ID] // zero value = BudgetNormal
		if shouldNotifyBudgetCrossing(prev, level) {
			h.notifyBudgetCrossing(inst, level)
		}
		h.budgetLastLevel[inst.ID] = level
	}
}

// notifyBudgetCrossing emits the one-time alert for a high/over crossing. It
// logs at WARN always and, when notifications are enabled, is the seam for an
// OS push. Debounce is handled by the caller's per-session last-level map.
func (h *Home) notifyBudgetCrossing(inst *session.Instance, level session.BudgetLevel) {
	uiLog.Warn("context_budget_crossing",
		slog.String("session", inst.Title),
		slog.String("id", inst.ID),
		slog.String("level", level.String()))
	// OS push (best-effort, gated by the existing notifications settings).
	if session.GetNotificationsSettings().GetEnabled() {
		// Reuse the same desktop-notification path the rest of the UI uses for
		// session alerts. If no such helper is wired for arbitrary messages,
		// leaving this as the WARN log above is acceptable for the MVP.
		_ = inst // placeholder seam; see note
	}
}
```

> The `notifyBudgetCrossing` OS-push body is the one integration seam without a confirmed single-call helper. Before finalizing, grep for how transition alerts reach the desktop (`grep -rn "func.*Desktop\|notify-send\|terminal-notifier\|osascript" internal/`). If a one-call helper exists, call it; otherwise the WARN log + visual badge is the shipped behavior and the OS-push line is removed (do not leave a dead `_ = inst`).

In `internal/ui/home.go`, add the maps to the `Home` struct (next to `clearOnCompactSent` ~line 360):

```go
	budgetLastLevel map[string]session.BudgetLevel // instanceID -> last seen budget level (notification debounce)
```

Initialize it where `clearOnCompactSent` is initialized (~line 1124):

```go
		budgetLastLevel: make(map[string]session.BudgetLevel),
```

Call the evaluator from `backgroundStatusUpdate()` right after the conductor clear-on-compact block (after home.go:3836), using the same `instances` snapshot:

```go
	// Context-budget warnings (all sessions): debounced one-shot on high/over crossing.
	h.evaluateContextBudgetWarnings(instances)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/ui/ -run TestShouldNotifyBudgetCrossing -v`
Expected: PASS.

- [ ] **Step 5: Verify build/vet**

Run: `go build ./... && go vet ./internal/ui/`
Expected: success.

- [ ] **Step 6: Commit**

```bash
git add internal/ui/context_budget_ui.go internal/ui/home.go internal/ui/context_budget_eval_test.go
git commit -m "feat(context-budget): debounced high/over warning notifications in bg loop"
```

---

### Task 8: Autonomous handoff wired into the background loop

**Files:**
- Modify: `internal/ui/context_budget_ui.go` (add the handoff evaluator + adapters)
- Modify: `internal/ui/home.go` (add the in-memory handoff-state maps to `Home` + init; call the evaluator from `backgroundStatusUpdate()`)
- Test: `internal/ui/context_budget_handoff_test.go` (create)

**Interfaces:**
- Consumes: `session.NextHandoffState`, `session.HandoffState`/`HandoffAction`, `statedb.WriteHandoffState`/`ReadHandoffState`, `inst.GetTmuxSession()`, `inst.Status`, `inst.Kill()`, `inst.StartWithMessage`, `h.forkSessionCmdWithOptions`, `h.program.Send`, `statedb.GetGlobal()`.
- Produces:
  - `func isAutonomousSession(inst *session.Instance) bool` — conductor (`IsConductor` or `GroupPath=="conductor"`) OR a child started with a prompt (`ParentSessionID != ""`).
  - `func handoffDir(id string) string` — `~/.agent-deck/handoff/<id>` (use `agentpaths`/home-dir resolution consistent with the codebase).
  - `func (h *Home) evaluateContextBudgetHandoff(instances []*session.Instance)`
  - adapters: `requestWrap`, `forkContinuation`, `failsafePause` (methods on `*Home`).

> The pure decisions are already tested in Task 5. This task's test covers `isAutonomousSession` and `handoffAgentIdle` (the input-derivation helpers); the adapters are integration glue exercised manually (see Step 6 verification) and kept off env-flaky deps.

- [ ] **Step 1: Write the failing test**

Create `internal/ui/context_budget_handoff_test.go`:

```go
package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestIsAutonomousSession(t *testing.T) {
	cases := []struct {
		name string
		inst *session.Instance
		want bool
	}{
		{"conductor flag", &session.Instance{IsConductor: true}, true},
		{"conductor group", &session.Instance{GroupPath: "conductor"}, true},
		{"parented child", &session.Instance{ParentSessionID: "parent-1"}, true},
		{"plain interactive", &session.Instance{GroupPath: "my-sessions"}, false},
	}
	for _, tc := range cases {
		if got := isAutonomousSession(tc.inst); got != tc.want {
			t.Errorf("%s: isAutonomousSession = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestHandoffAgentIdle(t *testing.T) {
	if !handoffAgentIdle(&session.Instance{Status: session.StatusWaiting}) {
		t.Errorf("waiting session should be idle")
	}
	if handoffAgentIdle(&session.Instance{Status: session.StatusRunning}) {
		t.Errorf("running session should not be idle")
	}
}
```

> Confirm the exact `Status` constant names before running (`grep -n "StatusWaiting\|StatusRunning\|StatusIdle" internal/session/instance.go`). Use the real "waiting/idle" constant(s) — adjust the test and `handoffAgentIdle` to whatever the enum actually exposes.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ui/ -run 'TestIsAutonomousSession|TestHandoffAgentIdle' -v`
Expected: FAIL — `undefined: isAutonomousSession`.

- [ ] **Step 3: Implement detection + idle helpers + the evaluator/adapters**

Append to `internal/ui/context_budget_ui.go`:

```go
// isAutonomousSession reports whether agent-deck launched this session non-
// interactively: a conductor, or a parented/fleet child. Only autonomous
// sessions get the auto wrap-up/fork; interactive sessions get warnings only.
func isAutonomousSession(inst *session.Instance) bool {
	if inst.IsConductor || inst.GroupPath == "conductor" {
		return true
	}
	return inst.ParentSessionID != ""
}

// handoffAgentIdle reports whether the agent is idle/waiting (safe to fork).
func handoffAgentIdle(inst *session.Instance) bool {
	return inst.Status == session.StatusWaiting
}

// handoffDir returns the per-session handoff directory.
func handoffDir(id string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agent-deck", "handoff", id)
}

// evaluateContextBudgetHandoff drives the per-session handoff state machine for
// autonomous sessions. State is persisted in tool_data and resumed across
// restarts. Runs once per background tick.
func (h *Home) evaluateContextBudgetHandoff(instances []*session.Instance) {
	cfg := session.GetContextBudgetSettings()
	if !cfg.GetEnabled() || !cfg.GetAutonomousHandoff() {
		return
	}
	db := statedb.GetGlobal()
	if db == nil {
		return
	}
	for _, inst := range instances {
		if !isAutonomousSession(inst) {
			continue
		}
		level, ok := h.budgetWarnState(inst, cfg)
		if !ok {
			continue
		}
		a := h.getAnalyticsForSession(inst)
		if a == nil {
			continue
		}

		// Resume persisted state lazily (survives restart mid-wrap).
		cur, trig := h.handoffState[inst.ID], h.handoffTriggeredAt[inst.ID]
		if _, seen := h.handoffState[inst.ID]; !seen {
			if pState, pAt, err := db.ReadHandoffState(inst.ID); err == nil {
				cur = session.HandoffState(pState)
				trig = pAt
				h.handoffState[inst.ID] = cur
				h.handoffTriggeredAt[inst.ID] = pAt
			}
		}
		_ = level // level available for finer-grained logging if desired

		in := session.HandoffInputs{
			Tokens:      a.CurrentContextTokens,
			PromptReady: fileExists(filepath.Join(handoffDir(inst.ID), "PROMPT.md")),
			AgentIdle:   handoffAgentIdle(inst),
			Now:         time.Now(),
			TriggeredAt: trig,
		}
		dec := session.NextHandoffState(cur, in, cfg)
		if dec.Next == cur && dec.Action == session.ActionNone {
			continue // no change
		}

		switch dec.Action {
		case session.ActionRequestWrap:
			now := time.Now()
			h.handoffTriggeredAt[inst.ID] = now
			h.requestWrap(inst)
			_ = db.WriteHandoffState(inst.ID, string(dec.Next), now)
		case session.ActionFork:
			h.forkContinuation(inst)
			_ = db.WriteHandoffState(inst.ID, string(dec.Next), h.handoffTriggeredAt[inst.ID])
		case session.ActionFailsafe:
			h.failsafePause(inst)
			_ = db.WriteHandoffState(inst.ID, string(dec.Next), h.handoffTriggeredAt[inst.ID])
		default:
			_ = db.WriteHandoffState(inst.ID, string(dec.Next), h.handoffTriggeredAt[inst.ID])
		}
		h.handoffState[inst.ID] = dec.Next
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// requestWrap creates the handoff dir and injects the wrap-up instruction.
func (h *Home) requestWrap(inst *session.Instance) {
	dir := handoffDir(inst.ID)
	_ = os.MkdirAll(dir, 0o755)
	ts := inst.GetTmuxSession()
	if ts == nil {
		return
	}
	prompt := filepath.Join(dir, "PROMPT.md")
	msg := "Context budget reached. Finish and save your current work now, then write a continuation prompt for a fresh session to " +
		prompt + " (and any work notes alongside it). Do not start new work. When PROMPT.md is written, stop and wait."
	safego.Go(uiLog, "context_budget_wrapup", func() {
		time.Sleep(500 * time.Millisecond)
		_ = ts.SendKeysAndEnter(msg)
	})
}

// forkContinuation reads PROMPT.md, forks a continuation session inheriting the
// old session's tool/profile/path/group/parent/worktree, seeds it with a short
// preamble + the handoff prompt, and archives the old session.
func (h *Home) forkContinuation(inst *session.Instance) {
	promptPath := filepath.Join(handoffDir(inst.ID), "PROMPT.md")
	data, err := os.ReadFile(promptPath)
	if err != nil {
		uiLog.Warn("handoff_prompt_read_failed", slog.String("id", inst.ID), slog.Any("err", err))
		h.failsafePause(inst)
		return
	}
	preamble := "You are a continuation of a previous session that reached its context budget. " +
		"Resume from this handoff prompt:\n\n"
	seed := preamble + string(data)

	// Inherit worktree fields when the source ran in a worktree.
	var opts *session.ClaudeOptions
	if inst.WorktreePath != "" {
		opts = &session.ClaudeOptions{
			WorktreePath:     inst.WorktreePath,
			WorktreeRepoRoot: inst.WorktreeRepoRoot,
			WorktreeBranch:   inst.WorktreeBranch,
		}
	}
	cmd := h.forkSessionCmdWithOptions(
		inst,
		inst.Title+" (cont.)",
		inst.GroupPath,
		forkToggles{},
		opts,
		git.WorktreeStateOptions{},
		inst.ParentSessionID,
		inst.ParentProjectPath,
		"",
	)
	if cmd == nil {
		h.failsafePause(inst)
		return
	}
	safego.Go(uiLog, "context_budget_fork", func() {
		msg := cmd() // executes the fork; returns sessionForkedMsg
		fm, ok := msg.(sessionForkedMsg)
		if !ok || fm.err != nil || fm.instance == nil {
			h.failsafePause(inst)
			return
		}
		// Register the new session via the existing handler (UI goroutine).
		if h.program != nil {
			h.program.Send(fm)
		}
		// Seed the continuation prompt once the new pane is live.
		time.Sleep(2 * time.Second)
		if ts := fm.instance.GetTmuxSession(); ts != nil {
			_ = ts.SendKeysAndEnter(seed)
		}
		// Archive (pause) the old session for history — targeted, idempotent.
		_ = inst.Kill()
		inst.ArchivedAt = time.Now().UTC()
		if db := statedb.GetGlobal(); db != nil {
			_ = db.SetArchived(inst.ID, inst.ArchivedAt)
		}
	})
}

// failsafePause stops the old session (no data loss) and raises the loudest
// alert. Never auto-/clears.
func (h *Home) failsafePause(inst *session.Instance) {
	uiLog.Error("context_budget_failsafe",
		slog.String("session", inst.Title),
		slog.String("id", inst.ID),
		slog.String("action", "paused; manual handoff required"))
	safego.Go(uiLog, "context_budget_failsafe_pause", func() {
		_ = inst.Kill()
	})
	h.notifyBudgetCrossing(inst, session.BudgetOver)
}
```

Add the required imports to `context_budget_ui.go` (`os`, `path/filepath`, `time`, `git "...internal/git"`, `"...internal/statedb"`, `"...internal/safego"` — match the exact import paths used elsewhere in `internal/ui`). Confirm `forkToggles`, `session.ClaudeOptions`, `git.WorktreeStateOptions`, `sessionForkedMsg`, `safego.Go`, and `uiLog` symbol names against home.go before building.

In `internal/ui/home.go`, add the maps to the `Home` struct (near `budgetLastLevel`):

```go
	handoffState       map[string]session.HandoffState // instanceID -> persisted handoff state (in-memory mirror)
	handoffTriggeredAt map[string]time.Time            // instanceID -> wrap trigger time
```

Initialize them next to `budgetLastLevel`:

```go
		handoffState:       make(map[string]session.HandoffState),
		handoffTriggeredAt: make(map[string]time.Time),
```

Call the evaluator from `backgroundStatusUpdate()` right after `evaluateContextBudgetWarnings`:

```go
	// Autonomous context-budget handoff (conductor + parented children only).
	h.evaluateContextBudgetHandoff(instances)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ui/ -run 'TestIsAutonomousSession|TestHandoffAgentIdle' -v`
Expected: PASS.

- [ ] **Step 5: Verify build/vet across the module**

Run: `go build ./... && go vet ./...`
Expected: success. (Per the upstream-merge-silent-conflicts memory, also run `node --check` on any touched JS — none here — and confirm no duplicate handlers were introduced.)

- [ ] **Step 6: Manual integration verification (no auto-merge of behavior into the success claim)**

Because the fork/archive adapters touch real tmux and a live agent, verify behavior manually with low thresholds rather than asserting it in env-flaky unit tests:

```bash
# In a scratch config, set tiny thresholds so a normal session crosses fast:
#   [context_budget]
#   warn_tokens = 200
#   high_tokens = 400
#   ceiling_tokens = 100000
#   handoff_timeout_seconds = 120
# Launch a conductor or parented child, let it accrue context, and confirm:
#  1. badge escalates ctx:warn -> ctx:high in the list,
#  2. a wrap-up instruction is injected and handoff/<id>/PROMPT.md appears,
#  3. a "<title> (cont.)" session spawns seeded with the prompt and the old one archives,
#  4. deleting/withholding PROMPT.md until timeout triggers the failsafe (old session paused + error log), never a /clear.
```

Report the observed outcome of each of the four checks honestly (including any that did not fire) before claiming completion.

- [ ] **Step 7: Commit**

```bash
git add internal/ui/context_budget_ui.go internal/ui/home.go internal/ui/context_budget_handoff_test.go
git commit -m "feat(context-budget): autonomous fork-on-budget handoff state machine wiring"
```

---

## Self-Review

**1. Spec coverage**

| Design requirement | Task |
|---|---|
| `[context_budget]` config + defaults | Task 1 |
| Budget level from `CurrentContextTokens`, inclusive lower bounds | Task 2 |
| Absolute-token-aware bar/label | Task 3 |
| Session-list badge | Task 4 |
| Warnings at 150k (bar/badge) / 200k (loud + one-time notif) / 250k (loudest) | Tasks 3, 4, 7 |
| Warnings only when token signal exists | Tasks 4, 7 (`budgetWarnState` gate) |
| Handoff state machine NORMAL→WRAP_REQUESTED→WAIT_HANDOFF→FORK + FAILSAFE | Task 5 |
| Trigger at high_tokens; ~50k headroom | Task 5 (`HandoffNormal`→`ActionRequestWrap` at HighTokens) |
| Handoff dir + wrap-up injection + pause work | Task 8 (`requestWrap`) |
| Poll PROMPT.md + agent idle before fork | Tasks 5 (inputs), 8 (`fileExists`, `handoffAgentIdle`) |
| Fork inherits tool/profile/path/group/parent/worktree, seeds preamble+prompt, archives old, title `(cont.)` | Task 8 (`forkContinuation`) |
| Failsafe on timeout OR ceiling-crossed: pause + alert, never /clear | Tasks 5, 8 (`failsafePause`) |
| Persisted state resumes across restart | Tasks 6, 8 (Read/WriteHandoffState + lazy resume) |
| Debounce once-per-crossing | Tasks 7 (`budgetLastLevel`), 8 (state transitions only act on change) |
| Autonomous = conductor + parented/fleet children | Task 8 (`isAutonomousSession`) |
| No schema bump; tool_data targeted updates; no save-abort clobber | Task 6 |
| Interactive sessions: warnings only, never auto-acted | Task 8 (`isAutonomousSession` gate) |

**2. Placeholder scan** — One acknowledged seam: the OS-push body in `notifyBudgetCrossing` (Task 7, Step 3 note) has no confirmed single-call helper; the instruction is to wire the real helper if found or remove the dead line (WARN log + badge is the shipped fallback). Everything else carries complete code. The `_ = level` / `_ = inst` lines are flagged to be removed or used at finalize time, not shipped as dead code.

**3. Type consistency** — `BudgetLevel`/`BudgetNormal..BudgetOver`, `HandoffState`/`HandoffNormal..HandoffFailsafe`, `HandoffAction`/`ActionNone..ActionFailsafe`, `ContextBudgetSettings`, `NextHandoffState`, `WriteHandoffState`/`ReadHandoffState`, `budgetBadge`, `budgetBarColor`, `shouldNotifyBudgetCrossing`, `isAutonomousSession`, `handoffAgentIdle` are used identically across the tasks that define and consume them. Unconfirmed external symbols are explicitly flagged for grep-before-edit: `Status` enum constant names (Task 8 Step 1 note), `cellWidth`/row `fmt.Sprintf` arg order (Task 4 Step 3 note), `GetConductor` lock style (Task 1 note), `MergeToolDataExtras` survival (Task 6 Step 2 note), and the import paths for `git`/`safego`/`forkToggles`/`ClaudeOptions` (Task 8 Step 3).
