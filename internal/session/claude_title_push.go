package session

import (
	"regexp"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// The deck→Claude half of title sync. ClaudeSessionNameIn (claude_title_reconcile.go)
// pulls Claude's own session name INTO the deck title; this file pushes the deck
// title OUT to Claude, so `ListAgents`/`SendMessage` address a session by the name
// the operator gave it in the deck rather than the cwd-derived placeholder Claude
// stamps as nameSource="derived".
//
// Two delivery points, deliberately different in kind:
//
//   - Launch (ClaudeLaunchName → buildClaudeExtraFlags): a `--name` flag baked
//     into every start/restart/resume command. Declarative, cannot race, and is
//     the path that eventually corrects any session the live push skipped.
//   - Rename (PushTitleToClaude → the FieldTitle postCommit): types `/name <slug>`
//     into the live pane so an in-flight session updates without a restart.
//
// Both are gated on push_title (default true) and both no-op when the operator
// supplies their own --name via extra-args, so an explicit flag always wins.

// claudeNameMaxLen bounds the pushed name. Claude accepts long and non-slug
// names without complaint (verified against 2.1.245: `--name "Deck Push Test_v2"`
// exits 0), so this is a readability bound for the deck's own display and the
// tmux window title, not a validation requirement.
const claudeNameMaxLen = 48

var (
	claudeNameSeparatorRE = regexp.MustCompile(`[^a-z0-9]+`)
	claudeNameValidRE     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

// ClaudeNameSlug normalizes a deck session title into the lowercase kebab form
// Claude uses for its own session names ("gitops-assistant-f4", "clear-conversation").
//
// Fail-safe by construction: anything that cannot be expressed in [a-z0-9-]
// collapses away, and a title that reduces to nothing (an emoji-only or
// entirely non-ASCII title) returns "" — the callers then emit no --name and
// send no /name rather than pushing a value nobody can address.
func ClaudeNameSlug(title string) string {
	s := claudeNameSeparatorRE.ReplaceAllString(strings.ToLower(strings.TrimSpace(title)), "-")
	s = strings.Trim(s, "-")
	if len(s) > claudeNameMaxLen {
		s = s[:claudeNameMaxLen]
		// Prefer a word boundary over a mid-word chop, but only when one
		// survives in the back half — trimming "a-verylongword..." back to "a"
		// loses more than the ragged edge does.
		if idx := strings.LastIndex(s, "-"); idx > claudeNameMaxLen/2 {
			s = s[:idx]
		}
		s = strings.Trim(s, "-")
	}
	if !claudeNameValidRE.MatchString(s) {
		return ""
	}
	return s
}

// extraArgsSupplyName reports whether the persisted --extra-arg tokens already
// carry a name override. Mirrors extraArgsSupplyModel: ValidateClaudeExtraArgToken
// forces flag and value apart, so the flag appears as a bare token. When present
// the operator's intent is explicit and unambiguous — emitting our own --name too
// would put two on the command line (harmless under claude's last-wins parsing,
// but confusing), and typing /name at them later would silently override the flag
// they chose.
func extraArgsSupplyName(extraArgs []string) bool {
	for _, tok := range extraArgs {
		if tok == "--name" || tok == "-n" || strings.HasPrefix(tok, "--name=") {
			return true
		}
	}
	return false
}

// pushTitleEnabled reads the global push_title switch. Defaults to enabled when
// the config can't be read, matching how the inbound sync treats an unreadable
// config (hook_name_sync.go): a transient load failure must not silently strand
// every session on a derived name.
func pushTitleEnabled() bool {
	cfg, err := LoadUserConfig()
	if err != nil || cfg == nil {
		return true
	}
	return cfg.GetPushTitle()
}

// claudePushName returns the slug this instance should carry on the Claude side,
// or "" when the push doesn't apply. Shared gate for both delivery points so the
// launch flag and the live rename can never disagree about what the name is.
func (i *Instance) claudePushName() string {
	if i == nil || !IsClaudeCompatible(i.Tool) {
		return ""
	}
	if extraArgsSupplyName(i.ExtraArgs) {
		return ""
	}
	if !pushTitleEnabled() {
		return ""
	}
	return ClaudeNameSlug(i.GetTitleThreadSafe())
}

// ClaudeLaunchName returns the value for the `--name` flag on this instance's
// next claude command, or "" to omit the flag entirely.
//
// Emitted on resume and restart as well as first start, which is what makes the
// launch path self-healing: a rename that PushTitleToClaude declined to deliver
// live (session busy, pane unreadable) lands the next time the session spawns.
// Verified against 2.1.245 that --name coexists with --continue/--resume.
func (i *Instance) ClaudeLaunchName() string {
	return i.claudePushName()
}

// PushTitleToClaude types `/name <slug>` into a live session's pane so a rename
// reaches Claude's session registry without waiting for a restart. Reports
// whether the keystrokes were sent.
//
// Best-effort and deliberately non-blocking — it runs from the FieldTitle
// postCommit, which the TUI invokes on its event loop, so it takes a single pane
// capture instead of polling WaitForAgentReady. Every refusal below is safe to
// take because ClaudeLaunchName re-asserts the same name on the next spawn:
//
//   - The pane reports "active". A busy Claude does not execute keystrokes as a
//     slash command; it queues them, and the queued line is then delivered to
//     the model as a literal "/name foo" message. Skipping is strictly better
//     than injecting a stray turn into someone's conversation.
//   - No empty composer on screen. This is the load-bearing gate, and it covers
//     two distinct hazards with one check: a composer holding a half-typed
//     operator draft would merge with our text and submit the union (#1409),
//     and a session sitting on an approval or trust dialog has no composer at
//     all — there, "/name x" plus Enter is an answer to whatever menu is up.
//   - Claude's registry already reports this name, so there is nothing to say.
//
// Note the deliberate absence of an i.Status check. That field is a persisted
// snapshot maintained by the TUI's status worker, so in a one-shot CLI process
// (`agent-deck rename`) it is whatever was last written to disk — a session
// that has long since settled still reads "waiting", and gating on it made the
// push a permanent no-op off the TUI. The pane is the only status that is true
// at the moment we are about to type into it.
func (i *Instance) PushTitleToClaude() bool {
	name := i.claudePushName()
	if name == "" {
		return false
	}
	tmuxSess := i.GetTmuxSession()
	if tmuxSess == nil || tmuxSess.Name == "" {
		return false
	}
	if ClaudeSessionName(i.ClaudeSessionID) == name {
		return false
	}
	if status, err := tmuxSess.GetStatus(); err != nil || status != "idle" && status != "waiting" {
		return false
	}
	raw, err := tmuxSess.CapturePaneFresh()
	if err != nil {
		return false
	}
	draft, composerVisible := send.ComposerDraft(raw, tmux.StripANSI)
	if !composerVisible || draft != "" {
		return false
	}
	return tmuxSess.SendKeysAndEnter("/name "+name) == nil
}
