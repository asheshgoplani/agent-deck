package main

import (
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Session context primer over the Claude SessionStart hook (v1.16.0 session
// context injection). SessionStart is the one edge that fires on EVERY way a
// claude conversation (re)opens — fresh startup, --resume, /clear, and
// post-compaction — so the primer is recomputed from live runtime facts at
// exactly the moments it can go stale. This is why claude's primer is
// hook-injected instead of prepended to the first message: an initial message
// does not survive a resume; a SessionStart hook does, natively.
//
// Level "none" (resolved session → group → global → default) injects nothing.
// Opt out of just the hook path with AGENTDECK_NO_PRIMER_CONTEXT=1.

// lifecycleFromHookSource maps Claude Code's SessionStart `source` payload
// field to a primer lifecycle. Empty for unknown sources — the caller falls
// back to the instance-derived proxy rather than guessing.
func lifecycleFromHookSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "startup":
		return session.LifecycleCreated
	case "resume":
		return session.LifecycleResumed
	case "clear":
		// /clear mints a fresh conversation in the same pane.
		return session.LifecycleCreated
	case "compact":
		// The conversation continues with compacted history — treat as
		// resumed so the agent re-verifies rather than redoes prior work.
		return session.LifecycleResumed
	default:
		return ""
	}
}

// buildPrimerContextSummary renders the primer for the session that fired the
// hook, or "" when the level is none / the instance is unknown / anything
// fails — the hook must never break a turn over context sugar.
func buildPrimerContextSummary(instanceID, source string) string {
	// #1790/#1822: pass "" straight through (see buildChildrenContextSummary).
	storage, instances, _, err := loadSessionData("")
	if err != nil {
		return ""
	}
	defer func() { _ = storage.Close() }()

	var inst *session.Instance
	for _, candidate := range instances {
		if candidate.ID == instanceID {
			inst = candidate
			break
		}
	}
	if inst == nil {
		return ""
	}

	cfg, _ := session.LoadUserConfig() // nil degrades to default resolution
	level, _ := session.ResolveContextLevel(cfg, inst)
	if level == session.ContextLevelNone {
		return ""
	}

	lifecycle := lifecycleFromHookSource(source)
	if lifecycle == "" {
		// At hook time a claude conversation id is already bound, so the
		// launch proxy would always claim "resumed"; without a trustworthy
		// source field the honest answer is unknown.
		lifecycle = session.LifecycleUnknown
	}

	parentTitle := ""
	if inst.ParentSessionID != "" {
		for _, candidate := range instances {
			if candidate.ID == inst.ParentSessionID {
				parentTitle = candidate.GetTitleThreadSafe()
				break
			}
		}
	}

	facts := session.CollectPrimerFacts(cfg, inst, parentTitle, lifecycle)
	return session.RenderPrimer(facts, level)
}
