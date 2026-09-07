package ctxtext

import "fmt"

// ActionableSentence is the payoff line: how many items the user can change,
// and what changing all of them would save today.
//
// The pitch for this feature is "see what is loaded so you can clean it up",
// and the default screen contained no verb, no action and no saving. The only
// actionable content was one drill-down away. This sentence puts the answer
// where the question is asked.
//
// amount is pre-rendered by the caller — each surface already owns the
// formatting that decides between a figure, a "≥" lower bound and an em dash —
// so this function never has to guess whether a number is certain. What it does
// own is the arithmetic of the claim:
//
//   - nothing actionable is stated as such, not implied by an absent line;
//   - a count whose items are all established to cost nothing today is not
//     dressed up as a saving, because it is not one;
//   - a count whose costs could not be established says so, rather than quoting
//     the zero that a failed measurement and an empty file produce alike;
//   - "act on", never "remove": most levers are an edit, and promising removal
//     for a file the user is meant to trim is a small lie in the one place the
//     screen is finally telling them to do something.
//
// The caller appends its own navigation hint — the CLI has a flag, the pager
// has a key — which is the only part that legitimately differs.
func ActionableSentence(n, tokens int, complete bool, amount string) string {
	switch {
	case n == 0:
		return "nothing in this report is under your control: all of it is harness internals or content agent-deck did not put there."
	case tokens == 0 && !complete:
		return fmt.Sprintf("you can act on %d item%s, but what they cost could not be established, so there is no saving to quote.",
			n, plural(n))
	case tokens == 0:
		return fmt.Sprintf("you can act on %d item%s — none of them costs anything on this turn, so removing them saves nothing today.",
			n, plural(n))
	default:
		return fmt.Sprintf("you can act on %d item%s, worth %s of the overhead above.", n, plural(n), amount)
	}
}

// plural returns the plural suffix for a count.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
