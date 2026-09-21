package tmux

import (
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"sync"
	"unicode"
)

// Opt-in Indic zero-width marks (#2334), [tmux] indic_zero_width_marks.
//
// Claude Code measures text with Bun.stringWidth, which sizes a consonant plus
// its spacing vowel sign (का, कि, को; the sign is Unicode category Mc) as ONE
// cell, as do tmux builds linked with utf8proc (Homebrew's). tmux built on
// glibc wcwidth (Linux distro packages) gives the vowel sign a cell of its
// own, so a Hindi reply from Claude Code wraps early and lands on the wrong
// cells inside the pane. Measured on tmux 3.6a/glibc: the reported line puts
// the cursor at column 116, 105 with these entries, Bun's figure.
//
// It is OFF by default and must stay that way. codepoint-widths is a SERVER
// option: on a default install it lands on the user's own tmux server, and it
// misaligns every program that measures per code point the other way, which
// is most of them: Codex and other ratatui TUIs (Rust unicode-width), bash and
// readline, vim, less. Turning it on trades those for Claude Code.
//
// When on (tmux >= 3.6), each Mc code point in the Brahmic blocks U+0900 to
// U+0DFF gets width 0 in a fixed slot, set with -o so a value already in the
// slot is never overwritten. When off, the first Start per socket in this
// process removes the slots still holding exactly agent-deck's value, so
// switching the key off takes effect on a running server.
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

func indicMarkSlot(r rune) int { return codepointWidthsBaseIndex + int(r-indicBlocksFirst) }

func indicMarkValue(r rune) string { return fmt.Sprintf("U+%04X=0", r) }

// tmuxSupportsCodepointWidths reports whether ver (as parseTmuxVersion
// returns it) has the codepoint-widths option. An empty (unknown) version is
// unsupported; an unparseable one ("master", "next-3.8") counts as new, like
// the other version gates.
func tmuxSupportsCodepointWidths(ver string) bool {
	if ver == "" {
		return false
	}
	major, minor, _, ok := splitTmuxVersion(ver)
	if !ok {
		return true
	}
	return major > codepointWidthsMinTmuxMajor ||
		(major == codepointWidthsMinTmuxMajor && minor >= codepointWidthsMinTmuxMinor)
}

// indicZeroWidthArgs returns the ";"-chained set-option chunks that give the
// Indic spacing vowel signs zero width, or nil unless the user opted in, tmux
// has codepoint-widths, and no [tmux] options "codepoint-widths" override
// takes the option over.
func indicZeroWidthArgs(enabled bool, overrides map[string]string, tmuxVersion string) []string {
	if _, ok := overrides["codepoint-widths"]; ok || !enabled || !tmuxSupportsCodepointWidths(tmuxVersion) {
		return nil
	}
	args := make([]string, 0, 5*len(indicSpacingMarks))
	for _, r := range indicSpacingMarks {
		args = append(args, ";", "set-option", "-soq",
			fmt.Sprintf("codepoint-widths[%d]", indicMarkSlot(r)), indicMarkValue(r))
	}
	return args
}

var codepointWidthsEntry = regexp.MustCompile(`(?m)^codepoint-widths\[(\d+)\] "?([^"\n]*)"?$`)

// ownedIndicZeroWidthSlots returns the slots in `show-options -s
// codepoint-widths` output that hold exactly the value agent-deck writes
// there. A slot in our range holding anything else belongs to someone else.
func ownedIndicZeroWidthSlots(indexed []byte) []int {
	var owned []int
	for _, m := range codepointWidthsEntry.FindAllSubmatch(indexed, -1) {
		slot, err := strconv.Atoi(string(m[1]))
		if err != nil || slot < codepointWidthsBaseIndex || slot > codepointWidthsBaseIndex+indicBlocksLast-indicBlocksFirst {
			continue
		}
		if r := rune(slot - codepointWidthsBaseIndex + indicBlocksFirst); string(m[2]) == indicMarkValue(r) {
			owned = append(owned, slot)
		}
	}
	return owned
}

// indicZeroWidthUnsetArgs turns owned slots into one ";"-chained command.
func indicZeroWidthUnsetArgs(slots []int) []string {
	var args []string
	for i, slot := range slots {
		if i > 0 {
			args = append(args, ";")
		}
		args = append(args, "set-option", "-suq", fmt.Sprintf("codepoint-widths[%d]", slot))
	}
	return args
}

// indicZeroWidthCleanupOnce limits the removal read to one tmux call per
// socket per process, keeping per-session tmux calls within budget.
var indicZeroWidthCleanupOnce sync.Map // socket name -> *sync.Once

// removeOwnedIndicZeroWidthMarks is the off path: it removes the entries a
// previous opt-in left on the server, and nothing else.
func removeOwnedIndicZeroWidthMarks(socketName, tmuxVersion string) {
	if !tmuxSupportsCodepointWidths(tmuxVersion) {
		return
	}
	once, _ := indicZeroWidthCleanupOnce.LoadOrStore(socketName, &sync.Once{})
	once.(*sync.Once).Do(func() {
		out, err := runBoundedOutput(socketName, "show-options", "-s", "codepoint-widths")
		if err != nil {
			return // a partial or failed read never authorizes a mutation
		}
		if slots := ownedIndicZeroWidthSlots(out); len(slots) > 0 {
			if _, err := runBoundedOutput(socketName, indicZeroWidthUnsetArgs(slots)...); err != nil {
				statusLog.Warn("indic_zero_width_marks_unset_failed", slog.Any("error", err))
			}
		}
	})
}

// IndicZeroWidthMarksInfo is the `agent-deck doctor` note for users who
// turned [tmux] indic_zero_width_marks on.
type IndicZeroWidthMarksInfo struct {
	TmuxVersion string `json:"tmux_version,omitempty"`
	Applied     bool   `json:"applied"`
	Detail      string `json:"detail"`
}

// CheckIndicZeroWidthMarks reports whether the opt-in can take effect on
// this host's tmux.
func CheckIndicZeroWidthMarks() IndicZeroWidthMarksInfo {
	return indicZeroWidthMarksInfo(hostTmuxVersionString())
}

func indicZeroWidthMarksInfo(ver string) IndicZeroWidthMarksInfo {
	info := IndicZeroWidthMarksInfo{TmuxVersion: ver, Applied: tmuxSupportsCodepointWidths(ver)}
	const tradeoff = "aligns Claude Code, misaligns Codex/shell/vim for Indic text; server-wide"
	if info.Applied {
		info.Detail = "on (tmux " + ver + "): " + tradeoff
	} else {
		info.Detail = "on, but not applied: needs tmux >= 3.6 (have " + orUnknown(ver) + ")"
	}
	return info
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
