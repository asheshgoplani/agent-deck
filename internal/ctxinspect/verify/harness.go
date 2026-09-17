package verify

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// ErrNoAccounting is returned when a captured pane contains nothing this
// package can read as a token figure.
//
// It is deliberately an error rather than an empty report: "the harness told us
// nothing" and "the harness told us zero" are different facts, and only one of
// them is ever true. A caller that turned this into an empty [HarnessReport]
// would produce a parity table full of confident zeroes.
var ErrNoAccounting = errors.New("ctxinspect/verify: no token accounting found in the captured pane")

// Figure is one labelled token figure read from a harness's own panel.
type Figure struct {
	// Label is the normalized label: lower-cased, whitespace collapsed,
	// decoration trimmed. Used for lookup.
	Label string `json:"label"`
	// Raw is the label exactly as the harness printed it, so output can quote
	// the harness rather than our normalization of it.
	Raw string `json:"raw"`
	// Tokens is the figure's value.
	Tokens int `json:"tokens"`
	// Slack is half the printed precision: the largest error the harness's own
	// abbreviation can hide. "2.3k" is somewhere in [2250, 2350), so it carries
	// a slack of 50; "155k" carries 500; an exact "22,813" carries 0. It is
	// added to the comparison tolerance for this row, so the harness's rounding
	// is never reported as our disagreement.
	Slack int `json:"slack"`
	// Percent is the percentage the harness printed beside the figure.
	Percent float64 `json:"percent,omitempty"`
	// HasPercent reports whether Percent was printed at all.
	HasPercent bool `json:"has_percent,omitempty"`
}

// HarnessReport is what a harness's own accounting command reported about
// itself, as read off the pane.
type HarnessReport struct {
	// Harness is the tool the panel came from, e.g. "claude".
	Harness string `json:"harness"`
	// Command is the slash command that produced the panel.
	Command string `json:"command"`
	// Model is the model identifier the panel named, when it named one.
	Model string `json:"model,omitempty"`
	// Window is the context window the panel named. Zero means it named none.
	Window int `json:"window,omitempty"`
	// Used is the occupancy the panel named. Zero means it named none.
	Used int `json:"used,omitempty"`
	// UsedSlack is the rounding slack on Used, in tokens.
	UsedSlack int `json:"used_slack,omitempty"`
	// UsedDerived reports that Used was computed from a printed percentage
	// rather than read directly, so it is only as precise as that percentage.
	UsedDerived bool `json:"used_derived,omitempty"`
	// Figures are the per-row figures, in the order the panel printed them.
	Figures []Figure `json:"figures"`
	// Unrecognized holds lines that looked like a labelled token figure but did
	// not parse. They are surfaced rather than dropped: a harness that renamed
	// or reshaped a row must be visible in the output, because a row that
	// quietly stops parsing narrows the diff instead of failing it.
	Unrecognized []string `json:"unrecognized,omitempty"`
}

// Figure returns the figure with the given normalized label.
func (h *HarnessReport) Figure(label string) (Figure, bool) {
	if h == nil {
		return Figure{}, false
	}
	for _, f := range h.Figures {
		if f.Label == label {
			return f, true
		}
	}
	return Figure{}, false
}

// Labels returns every normalized label the panel printed, in panel order.
func (h *HarnessReport) Labels() []string {
	if h == nil {
		return nil
	}
	out := make([]string, 0, len(h.Figures))
	for _, f := range h.Figures {
		out = append(out, f.Label)
	}
	return out
}

// ansiEscape matches ANSI control sequences. Panes are captured with colour
// preserved, and an escape in the middle of a label would defeat every match
// below.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// stripSGR removes ANSI escapes from captured pane text.
func stripSGR(s string) string { return ansiEscape.ReplaceAllString(s, "") }

// figureLine matches "<label>: <amount>[k|M] [tokens] [(<pct>%)]" anywhere in a
// line, which is what both panels print once their decorative gutter is
// ignored.
//
// The label charset excludes ':' and the amount must end on a word boundary, so
// neither a path ("Path: /Users/…") nor an identifier ("Session ID:
// 019fa876-…") can be read as a number.
var figureLine = regexp.MustCompile(`([A-Za-z][A-Za-z0-9 /&'._+-]{0,48}?)\s*:\s*([0-9][0-9,]*(?:\.[0-9]+)?)\s*([kKmM])?\b\s*(?:tokens?\b)?\s*(?:\(\s*([0-9]+(?:\.[0-9]+)?)\s*%\s*\))?`)

// figureShaped matches a line that was *meant* to be a labelled token figure:
// a label, a colon, a number, and then either a magnitude suffix, the word
// "tokens", a percent sign, or end of line. Only such a line is worth reporting
// as unrecognized; "Session ID: 019fa876-…" is not one.
var figureShaped = regexp.MustCompile(`(?i)[A-Za-z][^:]{0,60}:\s*[0-9][0-9,]*(?:\.[0-9]+)?\s*(?:[km]\b|tokens?\b|%|$)`)

// windowLine matches the "116k/200k tokens" occupancy summary Claude prints in
// its panel header.
var windowLine = regexp.MustCompile(`([0-9][0-9,]*(?:\.[0-9]+)?)\s*([kKmM])?\s*/\s*([0-9][0-9,]*(?:\.[0-9]+)?)\s*([kKmM])?\s*tokens?\b`)

// percentLeftLine matches the "78% context left" / "context left: 78%"
// phrasings, from which occupancy can be derived once the window is known.
var percentLeftLine = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*%\s*(?:of\s+)?context\s+left|context\s+left\s*:?\s*([0-9]+(?:\.[0-9]+)?)\s*%`)

// parseAmount converts a printed amount plus an optional magnitude suffix into
// a token count and the rounding slack that abbreviation implies.
//
// The slack is derived from the printed precision rather than assumed: "93k"
// and "93.0k" are the same number to ten times different accuracy, and treating
// them alike would either hide a real disagreement or invent one.
func parseAmount(digits, suffix string) (tokens, slack int, ok bool) {
	clean := strings.ReplaceAll(digits, ",", "")
	v, err := strconv.ParseFloat(clean, 64)
	if err != nil || v < 0 {
		return 0, 0, false
	}
	unit := 1.0
	switch strings.ToLower(suffix) {
	case "k":
		unit = 1000
	case "m":
		unit = 1000000
	default:
		// A bare decimal with no magnitude suffix ("1.5 tokens") is not a token
		// count any harness prints; treat it as unparseable rather than
		// rounding it into something plausible.
		if strings.Contains(clean, ".") {
			return 0, 0, false
		}
	}
	decimals := 0
	if dot := strings.IndexByte(clean, '.'); dot >= 0 {
		decimals = len(clean) - dot - 1
	}
	step := unit / math.Pow(10, float64(decimals))
	return int(math.Round(v * unit)), int(step / 2), true
}

// normalizeLabel lower-cases a printed label and collapses its whitespace and
// decoration so lookups do not depend on the harness's punctuation.
func normalizeLabel(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.Trim(s, "•·-–—*⛁⛶⛝ \t")
	s = strings.Join(strings.Fields(s), " ")
	return s
}

// parseFigures extracts every figure from a pane, in printed order.
//
// A line contributes at most one figure: both panels print one row per line,
// and accepting several would let a wrapped line contribute a duplicate. Lines
// that look like a labelled token figure but do not parse are returned
// separately so the caller can report them instead of losing them.
func parseFigures(pane string) (figures []Figure, unrecognized []string) {
	seen := make(map[string]bool)
	for _, line := range strings.Split(stripSGR(pane), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		m := figureLine.FindStringSubmatch(trimmed)
		if m == nil {
			if figureShaped.MatchString(trimmed) {
				unrecognized = append(unrecognized, trimmed)
			}
			continue
		}
		tokens, slack, ok := parseAmount(m[2], m[3])
		if !ok {
			unrecognized = append(unrecognized, trimmed)
			continue
		}
		label := normalizeLabel(m[1])
		if label == "" {
			unrecognized = append(unrecognized, trimmed)
			continue
		}
		if seen[label] {
			// A repeated label means the panel was captured twice (a redraw, or
			// scrollback plus the live pane); the first occurrence stands.
			continue
		}
		seen[label] = true
		f := Figure{Label: label, Raw: strings.TrimSpace(m[1]), Tokens: tokens, Slack: slack}
		if m[4] != "" {
			if pct, err := strconv.ParseFloat(m[4], 64); err == nil {
				f.Percent, f.HasPercent = pct, true
			}
		}
		figures = append(figures, f)
	}
	return figures, unrecognized
}

// parseWindowSummary extracts the "<used>/<window> tokens" summary, when the
// panel printed one.
//
// The scan is line-by-line rather than over the whole pane: every pattern here
// allows internal whitespace, and in Go's regexp \s matches a newline, so a
// whole-pane scan can stitch a number from one line to a slash on another and
// report a window nobody printed.
func parseWindowSummary(pane string) (used, usedSlack, window int, ok bool) {
	for _, line := range strings.Split(stripSGR(pane), "\n") {
		m := windowLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		u, uslack, uok := parseAmount(m[1], m[2])
		w, _, wok := parseAmount(m[3], m[4])
		if !uok || !wok || w <= 0 {
			continue
		}
		return u, uslack, w, true
	}
	return 0, 0, 0, false
}

// parsePercentLeft extracts a "N% context left" phrasing and returns the
// remaining share as a percentage.
func parsePercentLeft(pane string) (pct float64, ok bool) {
	for _, line := range strings.Split(stripSGR(pane), "\n") {
		m := percentLeftLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		raw := m[1]
		if raw == "" {
			raw = m[2]
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || v < 0 || v > 100 {
			continue
		}
		return v, true
	}
	return 0, false
}

// describeFigures renders parsed figures for a diagnostic message.
func describeFigures(figs []Figure) string {
	if len(figs) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(figs))
	for _, f := range figs {
		parts = append(parts, fmt.Sprintf("%s=%d", f.Label, f.Tokens))
	}
	return strings.Join(parts, ", ")
}
