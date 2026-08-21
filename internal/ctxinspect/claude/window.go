package claude

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/ctxtext"
)

// contextWindowEnv is the override agent-deck honours when a user knows their
// deployment's window better than a model-name table can. It is unset by
// default; when it is set it wins, because a user-supplied fact beats an
// inference.
//
// It is an alias, not a second literal: every screen that meets an unknown
// window prints "set AGENTDECK_CONTEXT_WINDOW" as the remedy, and a remedy
// naming a variable this reader does not consult is worse than no remedy.
const contextWindowEnv = ctxtext.WindowEnvVar

// modelWindows maps model-identifier prefixes to context-window sizes,
// most-specific first.
//
// The table mirrors the one agent-deck already ships in internal/session, which
// is the project's maintained record of these sizes. What differs is the failure
// mode: this lookup returns "unknown" for a family it does not recognise, where
// the existing one returns 200,000 for anything. A denominator invented for an
// unrecognised model turns an honest numerator into a misleading percentage, and
// a missing gauge is better than a wrong one.
//
// Detail carries provenance, never advice. The remedy — "set
// AGENTDECK_CONTEXT_WINDOW" — is owned by [ctxtext] and printed by every
// surface, so spelling it here too would put the same sentence on screen twice
// and let one copy rot.
//
// family marks an entry that stands for a whole model family rather than one
// release. A family entry answers for a minor version it has not been taught by
// assuming the window of the closest taught release at or below it — see
// [assumeFamilyWindow] — and marks the answer [ctxinspect.WindowModelFamily] so
// no surface can present it as established. Every minor version that HAS been
// released still deserves its own row: an assumption is the fallback, not the
// plan, and leaving a shipped model to fall through to it is how a whole live
// fleet ends up reading a hedge instead of a fact.
var modelWindows = []struct {
	prefix string
	size   int
	family bool
}{
	// 5-series. Every current model carries a 1M window.
	{prefix: "claude-fable-5", size: 1000000, family: true},
	{prefix: "claude-mythos-5", size: 1000000, family: true},
	{prefix: "claude-opus-5", size: 1000000, family: true},
	{prefix: "claude-sonnet-5", size: 1000000, family: true},
	// 4.6-and-later minor versions: 1M.
	{prefix: "claude-opus-4-8", size: 1000000},
	{prefix: "claude-opus-4-7", size: 1000000},
	{prefix: "claude-opus-4-6", size: 1000000},
	{prefix: "claude-sonnet-4-6", size: 1000000},
	// Released 4.x minor versions that predate the 1M window. Named
	// explicitly so the family guard below does not refuse a model this
	// build actually knows.
	{prefix: "claude-opus-4-5", size: 200000},
	{prefix: "claude-opus-4-1", size: 200000},
	{prefix: "claude-sonnet-4-5", size: 200000},
	{prefix: "claude-haiku-4-5", size: 200000},
	// Family fallbacks: answer exactly for the base release, and answer by
	// assumption for a minor version no row above names.
	{prefix: "claude-opus-4", size: 200000, family: true},
	{prefix: "claude-sonnet-4", size: 200000, family: true},
	{prefix: "claude-haiku-4", size: 200000, family: true},
	{prefix: "claude-3-5", size: 200000, family: true},
	{prefix: "claude-3-opus", size: 200000, family: true},
}

// minorVersion reports the minor-version number an identifier adds to a family
// prefix, when it adds one.
//
// The segment after the family tells the cases apart: a short number is a minor
// version (claude-opus-4-8), a long number is a release date
// (claude-opus-4-20250514), and a word is a family variant
// (claude-3-5-sonnet). Only the first names a release of this family that the
// table may or may not have been taught.
func minorVersion(id, family string) (int, bool) {
	rest := strings.TrimPrefix(strings.TrimPrefix(id, family), "-")
	if rest == "" {
		return 0, false
	}
	if idx := strings.IndexByte(rest, '-'); idx >= 0 {
		rest = rest[:idx]
	}
	if len(rest) == 0 || len(rest) > 2 {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return n, true
}

// hasUnknownMinorVersion reports whether an identifier extends a family prefix
// with a minor version the table does not list explicitly.
func hasUnknownMinorVersion(id, family string) bool {
	_, ok := minorVersion(id, family)
	return ok
}

// matchesPrefix reports whether an identifier is the entry's model or a variant
// of it, rather than merely sharing its opening characters.
//
// The boundary matters more than it looks: "claude-opus-4-10" begins with the
// literal text "claude-opus-4-1", and a bare strings.HasPrefix would hand a
// tenth minor version the window of the first — a confidently wrong denominator
// produced by exactly the table that exists to prevent them.
func matchesPrefix(id, prefix string) bool {
	if !strings.HasPrefix(id, prefix) {
		return false
	}
	rest := id[len(prefix):]
	return rest == "" || rest[0] == '-' || rest[0] == '['
}

// assumeFamilyWindow picks the window to assume for a release of a known family
// that this build has never seen, and names the release the figure came from.
//
// It takes the closest taught minor version at or below the unknown one, and
// falls back to the family's own entry when there is none. The rule follows the
// only property of these windows that has held across every release: within a
// family the window never shrinks. So the figure it returns is a LOWER bound on
// the true window, which makes the percentage derived from it an OVER-estimate
// — the safe direction for a number whose job is to warn you that you are
// running out of room.
//
// Assuming the family's base size instead would have been the plausible lie:
// claude-opus-4 carries 200,000, while every taught 4.6-and-later minor carries
// 1M, so an unseen claude-opus-4-9 would have been reported as five times
// fuller than it is.
func assumeFamilyWindow(id, family string, base int) (int, string) {
	minor, ok := minorVersion(id, family)
	if !ok {
		return base, family
	}
	size, from, bestMinor := base, family, -1
	for _, entry := range modelWindows {
		if entry.family || !strings.HasPrefix(entry.prefix, family+"-") {
			continue
		}
		taught, ok := minorVersion(entry.prefix, family)
		if !ok || taught > minor || taught <= bestMinor {
			continue
		}
		size, from, bestMinor = entry.size, entry.prefix, taught
	}
	return size, from
}

// longContextSuffixes mark an explicitly long-context variant of a model. The
// suffix is part of the identifier the harness records, so it is evidence
// rather than inference.
var longContextSuffixes = []string{"[1m]", "-1m"}

// ResolveWindow establishes the context-window size by precedence:
//
//  1. the AGENTDECK_CONTEXT_WINDOW environment override, when it parses;
//  2. an explicit long-context suffix on the model identifier;
//  3. the model-prefix table;
//  4. for an unseen release of a known family, that family's assumed window,
//     marked [ctxinspect.WindowModelFamily];
//  5. unknown.
//
// It never falls back to a global default size. Step 5 renders as "window
// unknown", which is the honest outcome for a family this build has not been
// taught: there is nothing to interpolate from, and a guess would be a guess.
func ResolveWindow(model string, env func(string) string) ctxinspect.WindowInfo {
	if env != nil {
		if raw := strings.TrimSpace(env(contextWindowEnv)); raw != "" {
			if v, err := strconv.Atoi(raw); err == nil && v > 0 {
				return ctxinspect.WindowInfo{
					Tokens: v,
					Source: ctxinspect.WindowEnvOverride,
					Detail: contextWindowEnv + "=" + raw,
				}
			}
		}
	}

	id := strings.TrimSpace(model)
	if id == "" {
		// Not "this session recorded no model": that is a claim about the
		// session, and it was false on every transcript whose model identifier
		// the reader simply failed to reach. What is certain is only that no
		// identifier arrived here.
		return ctxinspect.WindowInfo{
			Source: ctxinspect.WindowUnknown,
			Detail: "no model identifier reached the inspector — none was read from the transcript and none was supplied by the caller — so the window cannot be resolved",
		}
	}

	lower := strings.ToLower(id)
	for _, suffix := range longContextSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return ctxinspect.WindowInfo{
				Tokens: 1000000,
				Source: ctxinspect.WindowHarnessReported,
				Detail: fmt.Sprintf("the model identifier %q carries the long-context suffix %q", id, suffix),
			}
		}
	}
	for _, entry := range modelWindows {
		if !matchesPrefix(lower, entry.prefix) {
			continue
		}
		if entry.family && hasUnknownMinorVersion(lower, entry.prefix) {
			size, from := assumeFamilyWindow(lower, entry.prefix, entry.size)
			return ctxinspect.WindowInfo{
				Tokens: size,
				Source: ctxinspect.WindowModelFamily,
				Detail: fmt.Sprintf("%q is a release of the %s family this build has never seen, so the window shown is the one %q carries — assumed on the grounds that a window has never shrunk within a family",
					id, entry.prefix, from),
			}
		}
		return ctxinspect.WindowInfo{
			Tokens: entry.size,
			Source: ctxinspect.WindowModelDefault,
			Detail: fmt.Sprintf("model prefix %q", entry.prefix),
		}
	}
	return ctxinspect.WindowInfo{
		Source: ctxinspect.WindowUnknown,
		Detail: fmt.Sprintf("no context-window size is known for model %q, and this build has never been taught its family either", id),
	}
}
