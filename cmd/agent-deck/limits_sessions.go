package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// limitSessionJSON is one session `limits` names on its Claude account: one
// whose account is refused (account-blocked) or that waits on a cap
// (model-limit, usage-limit), with the command that moves it on.
type limitSessionJSON struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Substate string `json:"substate"`
	Detail   string `json:"detail,omitempty"`
	Fix      string `json:"fix"`
}

// limitSessionRow is the slice of a live session annotateClaudeLimits needs.
// Account is the stored slot ("" means the default login); Model is the
// session's launch model override ("" means the tool default).
type limitSessionRow struct {
	ID, Title, Account, Model, Substate, Detail string
	// EffectiveModel is the model the session actually launches with
	// (override, conductor/group model, then [claude].default_model); ""
	// when none is configured.
	EffectiveModel string
	// ConfigDir is the session's resolved Claude config dir (account,
	// conductor, group, env, profile, global or default): what decides
	// which slot's quota the session spends.
	ConfigDir string
}

// limitsBlockedReason is the blocked_reason when the session carries no detail.
const limitsBlockedReason = "organization disabled Claude subscription access"

// limitsOtherSlot is the placeholder fix commands name when no slot can take
// the session; it is left unquoted so it reads as a blank to fill in.
const limitsOtherSlot = "<other slot>"

// limitsFallbackModel is the model the model-cap fix switches to.
const limitsFallbackModel = "claude-opus-5-5"

// accountLimitHit reports a fresh, unexpired window at or above 100%, the
// per-account test limitReachedFrom applies.
func accountLimitHit(a limitAccountJSON) bool {
	for _, w := range a.Windows {
		if w.UsedPct >= 100 && !a.Stale && !w.Expired {
			return true
		}
	}
	return false
}

// fresh7d returns the account's fresh, unexpired 7d used_pct.
func fresh7d(a limitAccountJSON) (float64, bool) {
	if a.Stale {
		return 0, false
	}
	for _, w := range a.Windows {
		if w.Window == "7d" && !w.Expired {
			return w.UsedPct, true
		}
	}
	return 0, false
}

func isFableModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "fable")
}

func isOpusModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "opus")
}

// canonicalLimitsDir normalises a Claude config dir for slot matching: ~
// expanded, cleaned, symlinks resolved when the path exists (the same
// canonical form ClaudeUsageFeedSlots dedupes slots by). "" stays "".
func canonicalLimitsDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	dir = session.ExpandPath(dir)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return resolved
	}
	return filepath.Clean(dir)
}

// annotateClaudeLimits marks each Claude account that a live session reports
// account-blocked as blocked, and lists on each account the sessions that are
// account-blocked, model-limited or usage-limited, each with its fix. A
// session belongs to the slot owning its resolved config dir, else to its
// stored account, else (no account) to the default login entry. Pure:
// accounts is copied, never mutated in place.
func annotateClaudeLimits(accounts []limitAccountJSON, rows []limitSessionRow) []limitAccountJSON {
	out := make([]limitAccountJSON, len(accounts))
	copy(out, accounts)
	index := map[string]int{}
	byDir := map[string]int{}
	defaultIdx := -1
	for i, a := range out {
		if a.Harness != "claude" {
			continue
		}
		if a.Default {
			defaultIdx = i
			continue
		}
		index[a.Name] = i
		if dir := canonicalLimitsDir(a.ConfigDir); dir != "" {
			if _, taken := byDir[dir]; !taken {
				byDir[dir] = i
			}
		}
	}
	// A session spends the quota of the slot that owns its resolved config
	// dir, however that dir was chosen (account, conductor, group, env). The
	// default login only gets the sessions no slot owns.
	accountOf := func(r limitSessionRow) int {
		if dir := canonicalLimitsDir(r.ConfigDir); dir != "" {
			if i, ok := index[r.Account]; ok && canonicalLimitsDir(out[i].ConfigDir) == dir {
				return i
			}
			if i, ok := byDir[dir]; ok {
				return i
			}
		}
		if r.Account == "" {
			return defaultIdx
		}
		if i, ok := index[r.Account]; ok {
			return i
		}
		return -1
	}

	// Pass 1: blocked accounts.
	for _, r := range rows {
		if r.Substate != string(session.SubstateAccountBlocked) {
			continue
		}
		if i := accountOf(r); i >= 0 && !out[i].Blocked {
			out[i].Blocked = true
			out[i].BlockedReason = r.Detail
			if out[i].BlockedReason == "" {
				out[i].BlockedReason = limitsBlockedReason
			}
		}
	}

	// The slot a blocked account's sessions move to: a configured slot that is
	// neither blocked nor at a limit, lowest fresh 7d first.
	moveTarget := func(from int) string {
		best, bestPct := "", 0.0
		for i, a := range out {
			if i == from || a.Harness != "claude" || a.Default || a.Blocked || accountLimitHit(a) {
				continue
			}
			pct, ok := fresh7d(a)
			if !ok || pct >= 100 {
				continue
			}
			if best == "" || pct < bestPct {
				best, bestPct = a.Name, pct
			}
		}
		if best == "" {
			return limitsOtherSlot
		}
		return best
	}

	// Pass 2: the sessions to act on.
	for _, r := range rows {
		i := accountOf(r)
		if i < 0 {
			continue
		}
		acct := out[i].Name
		s := limitSessionJSON{ID: r.ID, Title: r.Title, Substate: r.Substate, Detail: r.Detail}
		capModel := r.Model
		if capModel == "" {
			capModel = r.Detail
		}
		runModel := r.EffectiveModel
		if runModel == "" {
			runModel = r.Model
		}
		q := shellescape.Quote
		qSlot := func(slot string) string {
			if slot == limitsOtherSlot {
				return slot
			}
			return q(slot)
		}
		modelFix := func(model, detail string) string {
			if model == "" {
				model = "model"
			}
			// Already on the fallback, or capped on Opus: a model switch
			// would be a no-op, so wait out the cap or move account.
			if r.Model == limitsFallbackModel || isOpusModel(capModel) {
				return fmt.Sprintf("wait for reset (%s) or move account: agent-deck session set %s account %s --intended %s && agent-deck session restart %s", detail, q(r.ID), qSlot(moveTarget(i)), q(model+" cap"), q(r.ID))
			}
			return fmt.Sprintf("agent-deck session set %s model %s --intended %s && agent-deck session restart %s", q(r.ID), q(limitsFallbackModel), q(model+" cap"), q(r.ID))
		}
		switch r.Substate {
		case string(session.SubstateAccountBlocked):
			s.Fix = fmt.Sprintf("agent-deck session set %s account %s --intended %s && agent-deck session restart %s", q(r.ID), qSlot(moveTarget(i)), q(acct+" blocked"), q(r.ID))
		case string(session.SubstateModelLimit):
			s.Fix = modelFix(capModel, r.Detail)
		case string(session.SubstateUsageLimit):
			// Fable's own weekly cap, not the account's (bug 4): only when the
			// 429 names the weekly window and no fresh account window (5h
			// included) is at its limit.
			if pct, ok := fresh7d(out[i]); ok && pct < 100 && isFableModel(runModel) &&
				strings.HasPrefix(r.Detail, "weekly") && !accountLimitHit(out[i]) {
				s.Substate = string(session.SubstateModelLimit)
				s.Detail = "Fable weekly (account has room)"
				s.Fix = modelFix(runModel, s.Detail)
			} else {
				s.Fix = fmt.Sprintf("wait for reset (%s) or move account", r.Detail)
			}
		default:
			continue
		}
		out[i].Sessions = append(out[i].Sessions, s)
	}
	return out
}

// limitsTextNotes renders the extra text lines `limits` prints under an
// account: the blocked verdict with the sessions to move, then each
// model-limited session with its fix.
func limitsTextNotes(a limitAccountJSON) []string {
	var lines []string
	if a.Blocked {
		var titles []string
		for _, s := range a.Sessions {
			if s.Substate == string(session.SubstateAccountBlocked) {
				titles = append(titles, s.Title)
			}
		}
		lines = append(lines, fmt.Sprintf("  BLOCKED (org disabled access): move %d sessions: %s", len(titles), strings.Join(titles, ", ")))
	}
	for _, s := range a.Sessions {
		if s.Substate == string(session.SubstateModelLimit) {
			lines = append(lines, fmt.Sprintf("  MODEL CAP %s (%s): %s", s.Title, s.Detail, s.Fix))
		}
	}
	return lines
}

// liveClaudeSessionRows loads the profile's Claude-compatible, non-stopped
// sessions read-only (nothing is saved) with their transcript limit verdict
// and resolved config dir. It never calls Substate: that captures panes and
// persists hook-lag samples, neither of which a report may do.
func liveClaudeSessionRows(profile string) []limitSessionRow {
	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		return nil
	}
	var rows []limitSessionRow
	for _, inst := range session.VisibleInstances(instances) {
		if inst == nil || !session.IsClaudeCompatible(inst.Tool) || inst.GetStatusThreadSafe() == session.StatusStopped {
			continue
		}
		// The transcript verdict alone: no pane capture, nothing written.
		sub, detail := inst.TranscriptLimit()
		switch sub {
		case session.SubstateAccountBlocked, session.SubstateModelLimit, session.SubstateUsageLimit:
		default:
			continue
		}
		rows = append(rows, limitSessionRow{
			ID:             inst.ID,
			Title:          inst.GetTitleThreadSafe(),
			Account:        inst.GetAccountThreadSafe(),
			Model:          inst.LaunchModelID(),
			EffectiveModel: inst.EffectiveClaudeModel(),
			Substate:       string(sub),
			Detail:         detail,
			ConfigDir:      session.GetClaudeConfigDirForInstance(inst),
		})
	}
	return rows
}
