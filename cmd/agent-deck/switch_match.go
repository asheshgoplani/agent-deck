package main

import (
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/sessionmatch"
)

// The spoken-name matcher lives in internal/sessionmatch so the voice desk
// (`agent-deck voice do`) resolves names with the same rules. These names
// keep `agent-deck switch` and its tests unchanged.
type (
	switchTarget      = sessionmatch.Target
	switchCandidate   = sessionmatch.Candidate
	switchMatchResult = sessionmatch.Result
)

const (
	switchMatchOne       = sessionmatch.MatchOne
	switchMatchAmbiguous = sessionmatch.MatchAmbiguous
	switchMatchNone      = sessionmatch.MatchNone
)

// switchTargets keeps the sessions a switch may go to: archived ones are left out.
func switchTargets(instances []*session.InstanceData) []switchTarget {
	out := make([]switchTarget, 0, len(instances))
	for _, inst := range instances {
		if inst == nil || !inst.ArchivedAt.IsZero() {
			continue
		}
		out = append(out, switchTarget{ID: inst.ID, Title: inst.Title, Group: inst.GroupPath, Path: inst.ProjectPath, Status: string(inst.Status)})
	}
	return out
}

// matchSwitchTarget resolves a spoken or typed query (sessionmatch.Match).
func matchSwitchTarget(query string, targets []switchTarget) switchMatchResult {
	return sessionmatch.Match(query, targets)
}

func switchTitleScore(query, title string) float64 { return sessionmatch.TitleScore(query, title) }

func levenshtein(a, b string) int { return sessionmatch.Levenshtein(a, b) }
