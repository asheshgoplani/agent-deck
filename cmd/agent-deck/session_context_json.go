package main

import (
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
)

// contextSchemaVersion is the version of the `session context --json` payload.
//
// It is bumped whenever a field is removed or changes meaning; additive fields
// do not bump it. Golden fixtures assert on this document byte-for-byte, so a
// harness format change surfaces as a CI failure rather than as a panel that
// silently reports empty categories.
const contextSchemaVersion = 1

// contextJSON is the stable machine surface of `session context --json`.
//
// Field order is the marshalled order, so the document is byte-stable for a
// given report. Every token figure inside Report carries its own provenance
// (an unknown encodes as null, never 0), and the derived totals below carry a
// completeness flag rather than silently presenting a lower bound as a total.
type contextJSON struct {
	SchemaVersion int `json:"schema_version"`
	// Session identifies what was inspected, in agent-deck's terms.
	Session contextSessionJSON `json:"session"`
	// Warnings are resolution problems found before inspection ran.
	Warnings []string `json:"warnings,omitempty"`
	// TokenAccounting states up front whether any number in this document can
	// exist, so a consumer does not have to infer it from a page of nulls.
	TokenAccounting contextAccountingJSON `json:"token_accounting"`
	// Totals are the derived figures a consumer must not recompute: the
	// rollup and unknown-propagation rules live in the engine.
	Totals contextTotalsJSON `json:"totals"`
	// Report is the full inspection result.
	Report *ctxinspect.Report `json:"report"`
}

// contextSessionJSON identifies the inspected session.
type contextSessionJSON struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Profile string `json:"profile"`
	Tool    string `json:"tool"`
	Path    string `json:"path,omitempty"`
	Ref     string `json:"ref,omitempty"`
}

// contextAccountingJSON says whether token figures are obtainable at all for
// this harness, and why not when they are not.
type contextAccountingJSON struct {
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
}

// contextTotalJSON is one derived total.
//
// Complete is false when a contributing count was unknown, in which case Tokens
// is a lower bound and must be rendered as such. A consumer that ignores the
// flag will under-report; one that recomputes the sum itself will get the
// unknown-propagation rules wrong, which is why the figure is published here.
type contextTotalJSON struct {
	Tokens   int  `json:"tokens"`
	Complete bool `json:"complete"`
	// UpperBound says the figure covers more than it names, so the true value
	// is at most this large.
	//
	// Complete and UpperBound answer different questions and a consumer needs
	// both: Complete false means "something is missing from this sum", while
	// UpperBound true means "nothing is missing and something extra is
	// included". The fixed total inherits it from the anchor, because when the
	// measured request also carried earlier turns the gauge number is an
	// over-estimate of the fixed prefix — by up to 28,948 tokens on a real
	// corpus — and a consumer that trusts it as exact is the reason the flag is
	// on the wire rather than only in a caveat's prose.
	UpperBound bool   `json:"upper_bound,omitempty"`
	Note       string `json:"note,omitempty"`
}

// contextTotalsJSON groups the derived figures.
type contextTotalsJSON struct {
	// Fixed is the gauge number: attributed costs plus the residual.
	Fixed contextTotalJSON `json:"fixed"`
	// Attributed is the sum of every category's actual cost.
	Attributed contextTotalJSON `json:"attributed"`
	// Potential is what deferred content would cost if fully loaded. It is
	// never part of Fixed.
	Potential *contextTotalJSON `json:"potential,omitempty"`
	// WindowPercent is Fixed as a share of the context window. It is nil when
	// no window size was established: a percentage of an unknown denominator
	// is exactly the plausible-looking lie this feature exists to prevent.
	WindowPercent *float64 `json:"window_percent,omitempty"`
}

// contextItemJSON is the payload of `session context --item <id> --json`.
type contextItemJSON struct {
	SchemaVersion int                `json:"schema_version"`
	Session       contextSessionJSON `json:"session"`
	Category      string             `json:"category"`
	Item          ctxinspect.Item    `json:"item"`
	// Badge is the collapsed two-axis provenance, published so a consumer
	// renders the same grade the TUI does instead of re-deriving it.
	Badge ctxinspect.Badge `json:"badge"`
}

// contextCapabilitiesJSON is the payload of `session context --capabilities
// --json`. It is answerable without inspecting anything, which is what makes it
// the honest screen for a harness that cannot be measured or has never run.
type contextCapabilitiesJSON struct {
	SchemaVersion int                     `json:"schema_version"`
	Session       contextSessionJSON      `json:"session"`
	Capabilities  ctxinspect.Capabilities `json:"capabilities"`
}

// buildContextJSON assembles the full report document.
func buildContextJSON(v contextView) contextJSON {
	rep := v.Report

	fixed, fixedComplete := rep.FixedTotal()
	attributed, attributedComplete := rep.AttributedTotal()

	doc := contextJSON{
		SchemaVersion: contextSchemaVersion,
		Session:       contextSessionJSON{Title: v.Title, Profile: v.Profile, Tool: v.Tool, Path: rep.ProjectPath, Ref: v.Ref},
		Warnings:      v.Warnings,
		Totals: contextTotalsJSON{
			Fixed: contextTotalJSON{
				Tokens:     fixed,
				Complete:   fixedComplete,
				UpperBound: rep.Anchor.IsUpperBound(),
				Note:       fixedTotalNote(rep, fixedComplete),
			},
			Attributed: contextTotalJSON{
				Tokens:   attributed,
				Complete: attributedComplete,
				Note:     totalNote(attributedComplete, "sum of every category's actual cost"),
			},
		},
		Report: rep,
	}
	doc.Session.ID = rep.SessionID

	reason, unsupported := contextTokenAccountingUnsupported(rep)
	doc.TokenAccounting = contextAccountingJSON{Supported: !unsupported, Reason: reason}

	if potential, any := rep.PotentialTotal(); any {
		doc.Totals.Potential = &contextTotalJSON{
			Tokens:   potential,
			Complete: true,
			Note:     "what deferred content would cost if fully loaded; never part of the fixed total",
		}
	}
	if pct, ok := rep.Window.Percent(fixed); ok {
		doc.Totals.WindowPercent = &pct
	}
	return doc
}

// totalNote annotates a total, marking a lower bound as one.
func totalNote(complete bool, what string) string {
	if complete {
		return what
	}
	return "LOWER BOUND — " + what + "; at least one contributing item has no token count"
}

// fixedTotalNote is [totalNote] for the gauge figure, with the anchor's
// upper-bound qualifier folded in.
//
// A total can be both: incomplete because an item has no count, and an
// over-estimate because the measurement that produced its residual covered more
// than the fixed prefix. Saying only one of the two would let a consumer read
// the other direction as certain.
func fixedTotalNote(rep *ctxinspect.Report, complete bool) string {
	const what = "attributed costs plus the unattributed remainder"
	note := totalNote(complete, what)
	if !rep.Anchor.IsUpperBound() {
		return note
	}
	reason := strings.TrimSpace(rep.Anchor.UpperBoundReason)
	if reason == "" {
		reason = "the measurement it derives from covers more than the fixed prefix"
	}
	return "UPPER BOUND — " + note + "; " + reason
}

// buildContextItemJSON assembles the single-item document.
func buildContextItemJSON(v contextView, ri rankedItem) contextItemJSON {
	doc := contextItemJSON{
		SchemaVersion: contextSchemaVersion,
		Session:       contextSessionJSON{ID: v.Report.SessionID, Title: v.Title, Profile: v.Profile, Tool: v.Tool, Path: v.Report.ProjectPath, Ref: v.Ref},
		Category:      ri.Category,
		Item:          ri.Item,
		Badge:         ri.Item.Badge(),
	}
	return doc
}

// buildContextCapabilitiesJSON assembles the capabilities-only document.
func buildContextCapabilitiesJSON(v contextView, caps ctxinspect.Capabilities) contextCapabilitiesJSON {
	sess := contextSessionJSON{Title: v.Title, Profile: v.Profile, Tool: v.Tool, Ref: v.Ref}
	if v.Report != nil {
		sess.ID = v.Report.SessionID
		sess.Path = v.Report.ProjectPath
	}
	return contextCapabilitiesJSON{
		SchemaVersion: contextSchemaVersion,
		Session:       sess,
		Capabilities:  caps,
	}
}

// contextCapabilitiesView builds the minimal view the capabilities screen needs
// when no inspection has run.
func contextCapabilitiesView(ref, title, profile, tool string) contextView {
	return contextView{
		Ref:     strings.TrimSpace(ref),
		Title:   strings.TrimSpace(title),
		Profile: strings.TrimSpace(profile),
		Tool:    strings.TrimSpace(tool),
	}
}
