package send

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrSuggestionShown is returned by ClearSuggestionBeforeBareKey when the
// composer still shows a Claude prompt suggestion after Escape: the bare key
// was withheld and nothing else was sent.
var ErrSuggestionShown = errors.New("composer shows a Claude prompt suggestion; bare key withheld (type text first)")

// SuggestionEscapeSettle is how long ClearSuggestionBeforeBareKey waits after
// Escape before it re-reads the pane. It is longer than a double press, so two
// refused calls in a row cannot combine into Esc Esc, which opens Claude's
// rewind picker.
const SuggestionEscapeSettle = time.Second

// BareKeyActsOnSuggestion reports whether key, a tmux key name ("Enter" for a
// plain Enter), is one Claude Code acts on at a shown suggestion with nothing
// typed: Tab and Right accept it into the input, and Enter submits from it.
//
// It accepts every spelling tmux resolves to those keys: names are matched
// case-insensitively ("enter", "TAB", "kpenter"), the control forms "C-m",
// "c-M", "^M" (Enter) and "C-i", "^i" (Tab), hex codes ("0xd", "0x9"), and a
// raw CR or TAB byte, which tmux types literally. A key with any other
// modifier (S-Enter, M-Right, C-Tab) is not the bare key and is let through.
func BareKeyActsOnSuggestion(key string) bool {
	switch bareKeyName(key) {
	case "enter", "kpenter", "tab", "right":
		return true
	}
	return false
}

// LiteralTextKey names the key a literal send-keys text (tmux send-keys -l)
// amounts to when the whole text is one keypress Claude Code acts on at a
// shown suggestion: a CR is Enter, a TAB is Tab, and the cursor-right
// sequence is Right. Any other text returns "": once real text is typed the
// composer no longer shows a suggestion.
func LiteralTextKey(text string) string {
	switch text {
	case "\r":
		return "Enter"
	case "\t":
		return "Tab"
	case "\x1b[C", "\x1bOC":
		return "Right"
	}
	return ""
}

// bareKeyName resolves key the way tmux's key_string_lookup_string does, as
// far as this guard needs: the lower-case name of an unmodified key, with the
// control forms of m and i folded into "enter" and "tab", and the CR and TAB
// codes too. Any other modified key resolves to "".
func bareKeyName(key string) string {
	switch key {
	case "\r":
		return "enter"
	case "\t":
		return "tab"
	}
	if hex, ok := strings.CutPrefix(key, "0x"); ok {
		switch n, err := strconv.ParseUint(hex, 16, 32); {
		case err != nil:
			return ""
		case n == '\r':
			return "enter"
		case n == '\t':
			return "tab"
		}
		return ""
	}
	ctrl := false
	if len(key) == 2 && key[0] == '^' {
		ctrl, key = true, key[1:]
	}
	for len(key) > 2 && key[1] == '-' {
		if key[0] != 'C' && key[0] != 'c' {
			return "" // M- or S- (or an unknown prefix): not a bare key
		}
		ctrl, key = true, key[2:]
	}
	key = strings.ToLower(key)
	if ctrl {
		switch key {
		case "m":
			return "enter"
		case "i":
			return "tab"
		}
		return ""
	}
	return key
}

// ClearSuggestionBeforeBareKey runs before an automated bare Enter, Tab or
// Right (one with no text typed first in the same operation). When the
// composer shows a prompt suggestion it presses Escape once, waits settle and
// re-reads; a suggestion still shown returns ErrSuggestionShown and the
// caller must not send the key. While a turn is running (ClaudeTurnRunning)
// it refuses without Escape, which would interrupt the turn. Claude Code
// 2.1.295 does not clear a suggestion on Escape, so in practice this refuses;
// the Escape is kept for builds and plugins that do. A failed capture detects
// nothing and returns nil: the key goes out as it did before this guard
// existed.
func ClearSuggestionBeforeBareKey(capture func() (string, error), escape func() error, settle time.Duration) error {
	raw, err := capture()
	if err != nil || !ComposerBodyIsSuggestion(raw) {
		return nil
	}
	if ClaudeTurnRunning(raw) {
		// Escape would interrupt the running turn; refuse without it.
		return ErrSuggestionShown
	}
	if err := escape(); err != nil {
		return err
	}
	time.Sleep(settle)
	if raw, err = capture(); err == nil && !ComposerBodyIsSuggestion(raw) {
		return nil
	}
	return ErrSuggestionShown
}
