package tmux

import (
	"fmt"
	"runtime"
	"unicode"
)

// Indic spacing vowel signs (#2334).
//
// Claude Code measures text with Bun.stringWidth, which sizes a consonant
// plus its spacing vowel sign (का, कि, को; the sign is Unicode category Mc)
// as ONE cell, as do tmux builds linked with utf8proc (Homebrew's). tmux
// built on glibc wcwidth (every Linux distro package) gives the vowel sign a
// cell of its own. Inside an agent-deck session tmux therefore holds a Hindi
// reply wider than the program that wrote it: it wraps lines Claude Code did
// not wrap ("… क्यों sho" / "w हो रहा है?"), and Claude Code's cursor moves,
// computed on its own widths, land on the wrong cells, so the pane grid
// itself ends up with displaced spaces ("काम की बात" captured as
// "का मकी बा त") and stale rows. Every client and capture-pane (the deck
// preview) sees that corrupted grid.
//
// tmux 3.6 added the server option codepoint-widths. Setting the Indic Mc
// code points to width 0 makes tmux combine each one into the cell before
// it, exactly as the utf8proc build does. Measured on the test box: tmux 3.6a
// on glibc puts the cursor at column 116 after the reported line, 105 with
// these entries, which is Bun.stringWidth's figure and what tmux
// 3.7b/utf8proc reports on macOS. That keeps the tmux layer transparent: an
// attached session sees the same widths Claude Code uses when it runs
// directly in the terminal.
//
// Scope is the ten Brahmic blocks U+0900–U+0DFF (Devanagari through Sinhala),
// the scripts the reports carry. Entries go into fixed high slots with
// set-option -o, so they are idempotent across sessions and processes and
// never overwrite a value already in the same slot. codepoint-widths entries
// are applied in index order, so an agent-deck entry wins over a user entry
// for the same code point at a lower index; a [tmux] options
// "codepoint-widths" override opts out entirely.
const (
	codepointWidthsMinTmuxMajor = 3
	codepointWidthsMinTmuxMinor = 6

	// codepointWidthsBaseIndex is the first of agent-deck's reserved slots in
	// the server-wide codepoint-widths array (sparse, like the
	// terminal-features slot). A code point's slot is base + (r -
	// indicBlocksFirst), so it never moves when newer Unicode tables add or
	// reclassify marks, and the whole block range stays below the int32 limit.
	codepointWidthsBaseIndex = 2147482000

	indicBlocksFirst = 0x0900
	indicBlocksLast  = 0x0DFF
)

// indicSpacingMarks lists the Mc code points in the Brahmic blocks, derived
// from the Unicode tables of the Go toolchain.
var indicSpacingMarks = func() []rune {
	var marks []rune
	for r := rune(indicBlocksFirst); r <= indicBlocksLast; r++ {
		if unicode.Is(unicode.Mc, r) {
			marks = append(marks, r)
		}
	}
	return marks
}()

// tmuxSupportsCodepointWidths reports whether ver (as parseTmuxVersion
// returns it) has the codepoint-widths option. Like the other version gates,
// an unparseable version ("master", "next-3.8") counts as new; -q keeps an
// unknown option quiet anyway.
func tmuxSupportsCodepointWidths(ver string) bool {
	major, minor, _, ok := splitTmuxVersion(ver)
	if !ok {
		return true
	}
	return major > codepointWidthsMinTmuxMajor ||
		(major == codepointWidthsMinTmuxMajor && minor >= codepointWidthsMinTmuxMinor)
}

// complexScriptWidthArgs returns the ";"-chained set-option chunks that give
// the Indic spacing vowel signs zero width on the session's tmux server, or
// nil when the user overrides codepoint-widths or tmux predates the option.
func complexScriptWidthArgs(overrides map[string]string, tmuxVersion string) []string {
	if _, ok := overrides["codepoint-widths"]; ok || tmuxVersion == "" || !tmuxSupportsCodepointWidths(tmuxVersion) {
		return nil
	}
	args := make([]string, 0, 4*len(indicSpacingMarks))
	for _, r := range indicSpacingMarks {
		args = append(args, ";", "set-option", "-soq",
			fmt.Sprintf("codepoint-widths[%d]", codepointWidthsBaseIndex+int(r-indicBlocksFirst)), fmt.Sprintf("U+%04X=0", r))
	}
	return args
}

// ComplexScriptWidthCheck is the `agent-deck doctor` verdict on whether this
// host's tmux can size Indic vowel signs the way Claude Code does.
type ComplexScriptWidthCheck struct {
	State       string `json:"state"` // "ok", "unknown" or "warn"
	TmuxVersion string `json:"tmux_version,omitempty"`
	Detail      string `json:"detail"`
}

// CheckComplexScriptWidths reports whether sessions on this host keep
// Hindi/Bengali/Tamil text in the pane at the widths Claude Code wrote it.
func CheckComplexScriptWidths() ComplexScriptWidthCheck {
	return complexScriptWidthCheck(hostTmuxVersionString(), runtime.GOOS)
}

func complexScriptWidthCheck(ver, goos string) ComplexScriptWidthCheck {
	c := ComplexScriptWidthCheck{TmuxVersion: ver}
	switch {
	case ver == "":
		c.State, c.Detail = "unknown", "tmux version unknown; cannot tell how it sizes Indic vowel signs"
	case tmuxSupportsCodepointWidths(ver):
		c.State, c.Detail = "ok", "agent-deck sets codepoint-widths so Indic vowel signs (ा ि ी ो) share their consonant's cell, as Claude Code measures them"
	case goos != "linux":
		c.State, c.Detail = "ok", "tmux "+ver+" on "+goos+" is normally built with utf8proc, which already sizes Indic vowel signs as Claude Code does"
	default:
		c.State = "warn"
		c.Detail = "tmux " + ver + " (glibc) gives Indic vowel signs (ा ि ी ो) their own cell, unlike Claude Code: Hindi/Bengali/Tamil replies wrap and misplace in the pane; install tmux 3.6+ and agent-deck aligns it"
	}
	return c
}
