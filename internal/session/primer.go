// Session context injection (v1.16.0): every agent-deck session gets a small,
// honest, runtime-derived context primer so the agent inside knows what
// agent-deck is, who/where it is, and which cheap CLI paths exist — without
// Ashesh hand-explaining the deck in every prompt, and without a worker ever
// re-grepping a 1.8GB transcript corpus because it didn't know
// `agent-deck session search` exists.
//
// Design rules (SESSION-CONTEXT-PLAN.md):
//   - Every fact comes from the runtime. A fact that cannot be determined
//     prints "unknown" — never guessed, never omitted. "none" (no parent) and
//     "harness default" (no explicit model) are facts, not unknowns.
//   - Injection can NEVER fail a launch: any collection/render error degrades
//     to "inject nothing".
//   - Hard size budget, enforced by TestPrimerBudget: the primer must stay a
//     primer, not become a dumping ground. Adding a line means removing one or
//     raising the budget consciously in the same diff.
package session

import (
	"encoding/json"
	"fmt"
	"strings"

	"al.essio.dev/pkg/shellescape"
	"github.com/asheshgoplani/agent-deck/internal/git"
)

// Context levels. Empty string on a session/group/global means "inherit".
const (
	ContextLevelNone   = "none"
	ContextLevelPrimer = "primer"
	ContextLevelFull   = "full"
)

// Lifecycle values reported in the primer and AGENTDECK_LIFECYCLE.
const (
	LifecycleCreated = "created"
	LifecycleResumed = "resumed"
	LifecycleRevived = "revived"
	LifecycleUnknown = "unknown"
)

// Primer size budget. These are load-bearing: TestPrimerBudget fails the build
// when a rendered primer exceeds them, which is the hard line against the
// primer becoming a dumping ground. The char budgets are the WORST-CASE bound
// with every runtime field at its truncation cap below (pinned by
// TestPrimerBudget_WorstCase, which renders exactly that) — a live session
// cannot exceed them because RenderPrimer truncates each field (PR #2064
// round-2 P2: runtime titles/paths are unbounded; fixtures are not). A
// typical primer is ~1100 chars ≈ 280 tokens; the pathological bound is
// ≈400 tokens.
const (
	PrimerMaxLines = 16
	PrimerMaxChars = 1700
	FullMaxLines   = 26
	FullMaxChars   = 2400
)

// Per-field render caps (runes). Paths keep their TAIL (the identifying
// part); names keep their HEAD. Session/parent IDs are never truncated —
// they are agent-deck-generated (bounded) and the report-back command must
// stay paste-able.
const (
	primerCapName   = 48 // title, group
	primerCapPath   = 72 // dir, repo root
	primerCapBranch = 40
	primerCapSmall  = 32 // harness, model, account, profile, host, parent title
)

// clipHead keeps the first max runes, appending "…" when truncated.
func clipHead(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// clipTail keeps the last max runes, prepending "…" when truncated.
func clipTail(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return "…" + string(r[len(r)-max:])
}

// unknownFact is the literal printed for a fact that cannot be determined.
const unknownFact = "unknown"

// NormalizeContextLevel returns the canonical level string, or "" for
// empty/unrecognized input (callers treat "" as "inherit / not set").
func NormalizeContextLevel(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case ContextLevelNone:
		return ContextLevelNone
	case ContextLevelPrimer:
		return ContextLevelPrimer
	case ContextLevelFull:
		return ContextLevelFull
	}
	return ""
}

// ValidContextLevel reports whether v is an explicit valid level.
func ValidContextLevel(v string) bool { return NormalizeContextLevel(v) != "" }

// GetGroupContextLevel walks the group ancestor chain (exact path first, then
// parents) and returns the first explicit context_level, mirroring the
// GetGroupClaudeConfigDir inheritance semantics so nested groups inherit.
func (c *UserConfig) GetGroupContextLevel(groupPath string) string {
	if c == nil || groupPath == "" || c.Groups == nil {
		return ""
	}
	for p := groupPath; p != ""; p = getParentPath(p) {
		if groupCfg, ok := c.Groups[p]; ok {
			if lvl := NormalizeContextLevel(groupCfg.ContextLevel); lvl != "" {
				return lvl
			}
		}
	}
	return ""
}

// ResolveContextLevel resolves the effective context level for a session.
// Precedence (most specific wins): per-session field → group (ancestor walk)
// → global config → built-in default. The built-in default honors the
// standing ruling that orchestrators get `full` while workers get `primer`
// (the orchestrator material is never forced onto every worker).
//
// The source label ("session", "group", "global", "default", or
// "default-conductor") is for `session primer --json` diagnostics.
func ResolveContextLevel(cfg *UserConfig, inst *Instance) (level, source string) {
	if inst != nil {
		if lvl := NormalizeContextLevel(inst.ContextLevel); lvl != "" {
			return lvl, "session"
		}
	}
	if cfg != nil && inst != nil {
		if lvl := cfg.GetGroupContextLevel(inst.GroupPath); lvl != "" {
			return lvl, "group"
		}
	}
	if cfg != nil {
		if lvl := NormalizeContextLevel(cfg.ContextLevel); lvl != "" {
			return lvl, "global"
		}
	}
	if inst != nil && inst.IsConductor {
		return ContextLevelFull, "default-conductor"
	}
	return ContextLevelPrimer, "default"
}

// EffectiveContextLevel is the config-loading convenience over
// ResolveContextLevel for spawn-path callers. A broken config degrades to the
// built-in default chain — level resolution must never fail a launch.
func (i *Instance) EffectiveContextLevel() string {
	cfg, _ := LoadUserConfig() // nil on error is handled by ResolveContextLevel
	level, _ := ResolveContextLevel(cfg, i)
	return level
}

// LifecycleAtLaunch reports what the NEXT (or current) spawn of this instance
// means for the conversation: "created" (a fresh conversation will be minted)
// or "resumed" (a bound conversation id — or an always-continue tool with a
// recorded prior start — will be continued). This is the same signal the
// per-tool builders use to pick resume-vs-fresh, read through one tool-aware
// switch instead of guessed from heuristics.
//
// Known limitation, documented rather than papered over: for pi/cursor (which
// resume by `--continue`, not by id) a legacy row that predates
// last_started_at tracking reports "created" on its first tracked restart.
func (i *Instance) LifecycleAtLaunch() string {
	if i == nil {
		return LifecycleUnknown
	}
	switch {
	case IsClaudeCompatible(i.Tool):
		if i.ClaudeSessionID != "" {
			return LifecycleResumed
		}
	case i.Tool == "gemini":
		if i.GeminiSessionID != "" {
			return LifecycleResumed
		}
	case i.Tool == "opencode":
		if i.OpenCodeSessionID != "" {
			return LifecycleResumed
		}
	case IsCodexCompatible(i.Tool):
		if i.CodexSessionID != "" {
			return LifecycleResumed
		}
	case i.Tool == "copilot":
		if i.CopilotSessionID != "" {
			return LifecycleResumed
		}
	case i.Tool == "pi" || i.Tool == "cursor":
		// These tools always continue their previous conversation; a recorded
		// prior start means the next spawn is a resume.
		if !i.LastStartedAt.IsZero() {
			return LifecycleResumed
		}
	case i.Tool == "hermes" || i.Tool == "deepseek":
		// Conversation ids are re-discovered each start (json:"-"), so an id
		// in memory means a live resume; a prior start without one is
		// indeterminate — say so instead of guessing.
		if i.HermesSessionID != "" || i.DeepSeekSessionID != "" {
			return LifecycleResumed
		}
		if !i.LastStartedAt.IsZero() {
			return LifecycleUnknown
		}
	default:
		if i.GenericSessionID != "" {
			return LifecycleResumed
		}
	}
	return LifecycleCreated
}

// PrimerFacts is the runtime fact sheet the primer renders. Empty strings are
// rendered as their honest form by RenderPrimer ("unknown", "none", or
// "harness default" depending on the field's semantics).
type PrimerFacts struct {
	SessionID   string `json:"session_id"`
	Title       string `json:"title"`
	Group       string `json:"group"`
	Dir         string `json:"dir"`
	Host        string `json:"host"` // "local" or the SSH host
	IsWorktree  bool   `json:"is_worktree"`
	Branch      string `json:"branch,omitempty"` // live-probed; "unknown" on probe failure
	RepoRoot    string `json:"repo_root,omitempty"`
	Harness     string `json:"harness"`
	Model       string `json:"model,omitempty"` // "" = harness default
	Account     string `json:"account,omitempty"`
	Profile     string `json:"profile"`
	ParentID    string `json:"parent_id,omitempty"`
	ParentTitle string `json:"parent_title,omitempty"`
	Lifecycle   string `json:"lifecycle"`
	Level       string `json:"context_level"`
	LevelSource string `json:"context_level_source"`
}

// CollectPrimerFacts gathers the fact sheet from the instance plus live
// probes. parentTitle may be "" when the caller has no session list to
// resolve it against (the primer then names the parent by id alone, which is
// what `session send` needs anyway).
//
// The three documented truth traps are handled here:
//   - ProjectPath is a local placeholder for --ssh sessions (#1850-#1853):
//     LocationOf is consulted instead.
//   - WorktreeBranch is a creation-time snapshot: the branch is re-probed
//     live and prints "unknown" when the probe fails.
//   - An auto-named session's Title is a machine handle: the captured task
//     description, when present, is appended for context.
func CollectPrimerFacts(cfg *UserConfig, inst *Instance, parentTitle, lifecycle string) PrimerFacts {
	f := PrimerFacts{
		SessionID:   inst.ID,
		Title:       inst.GetTitleThreadSafe(),
		Group:       inst.GroupPath,
		Harness:     inst.contextEnvTool(),
		Model:       inst.LaunchModelID(),
		Account:     inst.Account,
		Profile:     GetEffectiveProfile(""),
		ParentID:    inst.ParentSessionID,
		ParentTitle: parentTitle,
		Lifecycle:   lifecycle,
	}
	if f.Lifecycle == "" {
		f.Lifecycle = LifecycleUnknown
	}
	if inst.GetAutoName() {
		if desc := inst.GetAutoNameDescription(); desc != "" {
			f.Title = f.Title + " (" + desc + ")"
		}
	}

	loc := LocationOf(inst)
	f.Dir = loc.Path
	if loc.IsLocal() {
		f.Host = "local"
	} else {
		f.Host = loc.Host
	}

	f.IsWorktree = inst.IsWorktree()
	if f.IsWorktree {
		f.RepoRoot = inst.WorktreeRepoRoot
	}
	// Branch: live probe on local dirs only. A remote session's branch is not
	// probeable from here — honest "unknown" when the session is marked as a
	// worktree, silence otherwise (a non-repo dir has no branch to report).
	switch {
	case loc.IsLocal() && f.Dir != "" && git.IsGitRepo(f.Dir):
		if branch, err := git.GetCurrentBranch(f.Dir); err == nil && branch != "" {
			f.Branch = branch
		} else {
			f.Branch = unknownFact
		}
	case f.IsWorktree:
		f.Branch = unknownFact
	}

	f.Level, f.LevelSource = ResolveContextLevel(cfg, inst)
	return f
}

// orFact returns v, or the given honest fallback when v is empty.
func orFact(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// RenderPrimer renders the injected context block for the given level.
// Returns "" for ContextLevelNone (and for any unrecognized level — degrade
// to nothing, never to a guess).
func RenderPrimer(f PrimerFacts, level string) string {
	level = NormalizeContextLevel(level)
	if level == "" || level == ContextLevelNone {
		return ""
	}

	// P2 (PR #2064 round 2): runtime fields are unbounded (deep worktree
	// paths, auto-generated titles); the budget is guaranteed by clipping
	// each field here, not merely asserted against short fixtures. IDs are
	// exempt (bounded, and the report-back command must stay paste-able).
	f.Title = clipHead(f.Title, primerCapName)
	f.Group = clipHead(f.Group, primerCapName)
	f.Dir = clipTail(f.Dir, primerCapPath)
	f.RepoRoot = clipTail(f.RepoRoot, primerCapPath)
	f.Branch = clipHead(f.Branch, primerCapBranch)
	f.Harness = clipHead(f.Harness, primerCapSmall)
	f.Model = clipHead(f.Model, primerCapSmall)
	f.Account = clipHead(f.Account, primerCapSmall)
	f.Profile = clipHead(f.Profile, primerCapSmall)
	f.Host = clipHead(f.Host, primerCapSmall)
	f.ParentTitle = clipHead(f.ParentTitle, primerCapSmall)

	var b strings.Builder
	b.WriteString("<agent-deck-context>\n")
	b.WriteString("You run inside agent-deck, a tmux session manager for AI coding agents; the `agent-deck` CLI is available.\n")
	fmt.Fprintf(&b, "Session: %s %q | group: %s | lifecycle: %s\n",
		orFact(f.SessionID, unknownFact), orFact(f.Title, unknownFact),
		orFact(f.Group, unknownFact), orFact(f.Lifecycle, unknownFact))
	if f.Lifecycle == LifecycleResumed || f.Lifecycle == LifecycleRevived {
		b.WriteString("This conversation existed before this launch: verify prior work (`git log`/`git status`, `agent-deck session output " + orFact(f.SessionID, unknownFact) + "`) before redoing anything; if already finished and reported, say so and stop.\n")
	}

	dirLine := "Dir: " + orFact(f.Dir, unknownFact)
	if f.IsWorktree {
		branch := orFact(f.Branch, unknownFact)
		if f.RepoRoot != "" {
			dirLine += fmt.Sprintf(" (git worktree of %s, branch %s)", f.RepoRoot, branch)
		} else {
			dirLine += fmt.Sprintf(" (git worktree, branch %s)", branch)
		}
	} else if f.Branch != "" {
		dirLine += fmt.Sprintf(" (git branch %s)", f.Branch)
	}
	dirLine += " | host: " + orFact(f.Host, unknownFact)
	b.WriteString(dirLine + "\n")

	fmt.Fprintf(&b, "Harness: %s | model: %s | account: %s | profile: %s\n",
		orFact(f.Harness, unknownFact), orFact(f.Model, "harness default"),
		orFact(f.Account, "default"), orFact(f.Profile, unknownFact))

	if f.ParentID != "" {
		parent := f.ParentID
		if f.ParentTitle != "" {
			parent += " " + fmt.Sprintf("%q", f.ParentTitle)
		}
		fmt.Fprintf(&b, "Parent: %s — report results with: agent-deck session send %s \"<message>\"\n", parent, f.ParentID)
	} else {
		b.WriteString("Parent: none (top-level session)\n")
	}

	b.WriteString("Cheap paths (prefer over raw alternatives):\n")
	b.WriteString("  agent-deck status --json   # fleet summary (not `list --json`)\n")
	b.WriteString("  agent-deck session search \"<query>\"   # indexed transcript search (never recursive-grep transcripts or $HOME)\n")
	b.WriteString("  agent-deck session children --follow --until-done   # wait on child sessions (not a poll loop)\n")
	b.WriteString("  agent-deck session output <id>  /  agent-deck session send <id> \"<msg>\"\n")
	b.WriteString("Current facts anytime: agent-deck session primer --json\n")

	if level == ContextLevelFull {
		b.WriteString("Orchestrator extras:\n")
		b.WriteString("  agent-deck launch <path> -c <tool> -m \"<task>\" --json   # spawn a child session (parent link is automatic)\n")
		b.WriteString("  Children assert completion by printing: ===AGENTDECK_DONE=== status=<ok|fail> summary=<one line>\n")
		b.WriteString("  Collect a child's answer with `agent-deck session output <id>`; check `agent-deck status --json` before mass-launching (groups may cap concurrency).\n")
	}

	b.WriteString("</agent-deck-context>")
	return b.String()
}

// BuildPrimerForLaunch renders the primer for this instance at its effective
// level, using LifecycleAtLaunch. Never fails: any problem returns "".
func (i *Instance) BuildPrimerForLaunch() string {
	if i == nil {
		return ""
	}
	cfg, _ := LoadUserConfig()
	level, _ := ResolveContextLevel(cfg, i)
	if level == ContextLevelNone {
		return ""
	}
	facts := CollectPrimerFacts(cfg, i, "", i.LifecycleAtLaunch())
	return RenderPrimer(facts, level)
}

// PrimerMessagePrefix returns the primer text to prepend to an initial
// message for tools WITHOUT a native injection channel. Claude-compatible
// tools return "" — their primer arrives via the SessionStart hook
// (additionalContext), which also re-fires on resume; prepending here too
// would double-inject. DeepSeek returns "" too: its headless profile's task
// IS the invocation and is replayed verbatim on restart (DeepSeekTask), so
// an embedded primer would replay with stale lifecycle facts — dsh gets the
// env spine only, stated plainly in the delivery table. Level none (or any
// render failure) returns "".
func (i *Instance) PrimerMessagePrefix() string {
	if i == nil || IsClaudeCompatible(i.Tool) || i.Tool == "deepseek" {
		return ""
	}
	return i.BuildPrimerForLaunch()
}

// contextEnvTool returns the tool name to report as AGENTDECK_TOOL / the
// primer's harness. For a Tool=="shell" subcommand-passthrough instance
// (#1800/#1821: `codex mcp list`, `claude remote-control …`) the ROUTING
// tool is "shell" but the process the pane actually runs is claude/codex —
// exporting "shell" would leak the routing name to subprocesses that key
// off AGENTDECK_TOOL (the exact contract TestIssue1800 pins). Same
// first-token-only classification as buildShellPassthroughCommand.
func (i *Instance) contextEnvTool() string {
	if i.SubcommandPassthrough {
		if fields := strings.Fields(i.Command); len(fields) > 0 {
			if matched := MatchTool(fields[0]); matched != "shell" {
				return matched
			}
		}
	}
	return i.Tool
}

// buildContextEnvExports emits the universal env-var fact spine, appended to
// buildEnvSourceCommand's source chain for EVERY tool on EVERY fresh start
// and resume. Env vars survive the bash -c / SSH / sandbox wrapping chains
// that host-side `tmux set-environment` values do not reach.
// Level "none" emits nothing (the red-path contract: none injects nothing).
func (i *Instance) buildContextEnvExports(cfg *UserConfig) string {
	level, _ := ResolveContextLevel(cfg, i)
	if level == ContextLevelNone {
		return ""
	}
	lifecycle := i.LifecycleAtLaunch()
	parts := []string{
		"export AGENTDECK_SESSION_ID=" + shellescape.Quote(i.ID),
		"export AGENTDECK_SESSION_TITLE=" + shellescape.Quote(i.GetTitleThreadSafe()),
		"export AGENTDECK_TOOL=" + shellescape.Quote(i.contextEnvTool()),
		"export AGENTDECK_GROUP=" + shellescape.Quote(i.GroupPath),
		"export AGENTDECK_LIFECYCLE=" + shellescape.Quote(lifecycle),
		"export AGENTDECK_CONTEXT_LEVEL=" + shellescape.Quote(level),
	}
	if i.ParentSessionID != "" {
		parts = append(parts, "export AGENTDECK_PARENT_ID="+shellescape.Quote(i.ParentSessionID))
	}
	return strings.Join(parts, " && ")
}

// ---- persistence (tool_data extras zone, mirrors idle_timeout_persist.go) ----

const toolDataContextLevelKey = "context_level"

// WriteContextLevelToToolData merges context_level into the tool_data blob.
// An empty level removes the key (blob shape identical to a pre-1.16 row, so
// downgrades stay clean and MergeToolDataExtras carries nothing stale).
func WriteContextLevelToToolData(td json.RawMessage, level string) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	if lvl := NormalizeContextLevel(level); lvl != "" {
		raw, _ := json.Marshal(lvl)
		m[toolDataContextLevelKey] = raw
	} else {
		delete(m, toolDataContextLevelKey)
	}
	out, _ := json.Marshal(m)
	return out
}

// ReadContextLevelFromToolData extracts context_level from the blob.
// Returns "" (inherit) for missing/malformed/legacy rows.
func ReadContextLevelFromToolData(td json.RawMessage) string {
	if len(td) == 0 {
		return ""
	}
	var blob struct {
		ContextLevel string `json:"context_level"`
	}
	_ = json.Unmarshal(td, &blob)
	return NormalizeContextLevel(blob.ContextLevel)
}
