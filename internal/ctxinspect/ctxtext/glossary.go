package ctxtext

// The context inspector had grown a private vocabulary. A reviewer who had
// never seen it met, on one screen, the words adapter, basis, anchor,
// projected, residual, unattributed remainder and reconciliation, and the codes
// CAPT, RECON, ABSENT, POTENTIAL, ~est and 🔒 — and not one of them was defined
// anywhere on the screen, in the help, or in the flags.
//
// The column legend (each surface renders its own, at its own width) covers the
// codes that appear in a table cell. It cannot also carry the concepts without
// becoming the screen. So the concepts live here, in one list both surfaces
// print, reachable by `--glossary` from the CLI and from the Verify tab in the
// pager.
//
// The rule the wording follows is the feature's own: say what a word means, and
// where a word marks an uncertainty, say which direction the uncertainty runs.
// "≥" is not decoration, it is the difference between a total and a floor.

// GlossaryTerm is one entry: the word as it appears on screen, and what it
// means. Def is one sentence, because a glossary nobody finishes is a glossary
// nobody read.
type GlossaryTerm struct {
	Term string
	Def  string
}

// Glossary returns every word the inspector puts on a screen and does not
// define in place, in reading order rather than alphabetical: a reader arrives
// here from the top of a report, not from an index.
func Glossary() []GlossaryTerm {
	return []GlossaryTerm{
		{"fixed startup overhead", "the bytes loaded into every single turn before you type anything: instruction files, skills, agents, tool schemas. This is the only thing this report measures. It is not your conversation."},
		{"context window", "the total the model can hold. It is the denominator of the gauge, and when it cannot be established no percentage is printed at all."},
		{"adapter", "the piece of agent-deck that knows one harness's on-disk layout. `claude` reads Claude Code's records, `codex` reads Codex's. A harness with no adapter is reported as an inventory with no figures rather than guessed at."},
		{"basis", "where the report came from. OBSERVED means agent-deck parsed records this session actually wrote. PROJECTED means no such records exist, so it computed what would load from configuration and the filesystem — no ground truth, and no anchor."},
		{"anchor", "the one token figure the provider itself measured, read back from the harness's own record of a real turn. Every other total is reconciled against it. Without an anchor nothing here is measured, only estimated."},
		{"attributed", "the part of the anchor agent-deck can point at a named file, skill or server."},
		{"unattributed remainder", "the rest: anchor minus attributed. It is the harness's own system prompt and built-in tool schemas — real, usually the largest single figure in the report, and nothing you can do removes it. Also called the residual, because it is obtained by subtraction rather than by measuring anything."},
		{"reconciliation / self-check", "the report grading its own arithmetic: does attributed plus remainder equal the anchor, and does every figure obey the rules the report claims for it. OK means it does. FAILED means it does not, and nothing on the screen should be trusted."},
		{"lever", "the specific thing you can do about one item: edit a file, delete a directory, or run a named command. An item with no lever is marked 🔒."},
		{"CAPT", "text axis: captured verbatim from a record the harness itself wrote. The strongest thing this report can say."},
		{"RECON", "text axis: reconstructed by re-running the harness's own discovery rules over the files as they are RIGHT NOW. Two things follow. It should match what the harness loaded, but a change on their side could make it differ; and a session that is already running is still sending the copy it booted with, so editing one of these files moves the figure here before it moves anything the model receives — restart the session for the two to agree. (In the footer, `self-check` is the arithmetic verdict — a different thing entirely, which is why it no longer shares this abbreviation.)"},
		{"ABSENT", "text axis: known to be in the context window, and not readable from disk. The cost may still be known even when the bytes are not."},
		{"~est", "token axis: estimated by agent-deck rather than measured by the provider. The footer states the method and, when there is a measured figure to check it against, the error bound."},
		{"POTENTIAL", "what a deferred item would cost if it were fully loaded. It is never added to any total, because today it costs what the TOKENS column says."},
		{"≤", "an upper bound: the true figure is this or less. It appears when the anchor covers more than the fixed prefix, and everything derived from that anchor inherits it."},
		{"≥", "a lower bound: the true figure is this or more. It appears on a total when at least one of its parts is unknown, so the sum covers only the known part."},
		{"—", "not knowable from what the harness wrote down. It is never a zero, and a zero is never one of these."},
		{"🔒", "you cannot remove it: harness internals, or content agent-deck did not put there."},
	}
}

// GlossaryLines renders the glossary as flat lines with the term on its own
// line and the definition indented under it.
//
// Two lines per term rather than a table, because the definitions are sentences
// and a term column padded to the width of "reconciliation / self-check" leaves
// a 40-column terminal nothing to put them in. Each surface wraps them at its
// own width; nothing here assumes one.
func GlossaryLines() []string {
	terms := Glossary()
	out := make([]string, 0, len(terms)*2+2)
	out = append(out,
		"glossary — the words on these screens",
		"",
	)
	for _, t := range terms {
		out = append(out, t.Term, "    "+t.Def)
	}
	return out
}
