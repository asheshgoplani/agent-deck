package send

import (
	"strings"
	"unicode/utf8"
)

// Claude Code renders a prompt suggestion (a predicted next user message,
// shown in an EMPTY composer) or its older shell-style autosuggestion as
// placeholder text in the SGR dim/faint attribute:
//
//	❯ <ESC>[2mrun the tests again<ESC>[0m
//
// A real operator draft carries no dim attribute:
//
//	❯ run the tests again
//
// That attribute is the ONLY thing distinguishing the two — the glyphs are
// identical once ANSI is stripped. The composer-draft guard (issue #1409)
// used to introspect stripped content only, so it classified an
// autosuggestion as an operator draft, saved it, cleared the composer, and
// then TYPED THE SUGGESTION BACK as real committable text. The next Enter
// from any source (a later send, a control-plane keystroke, the operator)
// then submitted a prompt nobody authored.
//
// Claude Code 2.1.295 paints the placeholder with the cursor ON its first
// character (its placeholder renderer: inverse(text[0]) + dim(text[1:])), so
// the first visible cell is inverse, not dim:
//
//	❯ <ESC>[7mR<ESC>[0;2mun the tests again<ESC>[0m     focused terminal
//	❯ R<ESC>[2mun the tests again<ESC>[0m               native cursor mode
//	❯ <ESC>[2mRun the tests again<ESC>[0m               unfocused terminal
//
// (bytes as tmux capture-pane -e re-encodes them). A first-cell-only check
// read the first two as typed text: the guard refused every automated send
// to an idle session showing a suggestion, and the attribution gate lost the
// one signal telling it the composer is really empty.
//
// Suggestions have accept-key semantics: Tab or Right arrow on the empty
// composer copies the suggestion into the input and the next Enter submits
// it; typing replaces it. They must never be treated as content to preserve,
// and automation must never press a bare Enter (or an accept key) while one
// is shown (see EnterAttribution.NudgeEnter). The helpers below read the
// attributes off the RAW pane capture (tmux capture-pane -e, which
// CapturePaneFresh already requests) before it is stripped.

// composerMarkers are the prompt glyphs Claude renders at the composer.
var composerMarkers = []string{"❯", "›"}

// composerMarkerBody returns the remainder of line following the composer
// marker, and whether a marker was present.
func composerMarkerBody(line string) (string, bool) {
	for _, marker := range composerMarkers {
		if idx := strings.Index(line, marker); idx >= 0 {
			return line[idx+len(marker):], true
		}
	}
	return "", false
}

// sgrState is the running rendering state a suggestion can hide behind.
// Claude (or a terminal profile) renders the ghost either with the SGR
// dim/faint attribute or in bright-black/256-colour grey — issue #1777
// documented fleets where the same suggestion arrived as `\e[90m` /
// `\e[38;5;8m` instead of `\e[2m`, defeating a dim-only check.
type sgrState struct {
	dim     bool // SGR 2 (faint)
	grey    bool // bright-black foreground: SGR 90 or 38;5;8
	inverse bool // SGR 7: the cell Claude paints its cursor on
}

// suggestionStyled reports whether a cell in this state is placeholder
// (suggestion) styled: dim or bright-black grey.
func (st sgrState) suggestionStyled() bool { return st.dim || st.grey }

// applySGR folds one SGR parameter list into the running suggestion-rendering
// state.
//
// Extended-colour introducers (38/48/58) consume their own parameters —
// `38;5;2` is colour index 2, NOT the dim attribute — so they are skipped
// explicitly. Without that, the bright-white `38;5;231` Claude uses for
// submitted messages would be misread. A 38-introduced foreground REPLACES
// the previous foreground, so it sets or clears grey; any direct foreground
// parameter (30-37, 39, 91-97) likewise clears it.
func applySGR(params string, st *sgrState) {
	if params == "" {
		// A bare CSI m is an implicit reset.
		*st = sgrState{}
		return
	}
	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); i++ {
		p := strings.TrimSpace(parts[i])
		switch p {
		case "38", "48", "58":
			if i+1 < len(parts) {
				switch strings.TrimSpace(parts[i+1]) {
				case "5":
					if p == "38" {
						st.grey = i+2 < len(parts) && strings.TrimSpace(parts[i+2]) == "8"
					}
					i += 2 // 5;n
				case "2":
					if p == "38" {
						st.grey = false
					}
					i += 4 // 2;r;g;b
				default:
					i++
				}
			}
		case "2":
			st.dim = true
		case "0", "00":
			*st = sgrState{}
		case "22":
			st.dim = false
		case "7":
			st.inverse = true
		case "27":
			st.inverse = false
		case "90":
			st.grey = true
		default:
			if isForegroundColourParam(p) {
				st.grey = false
			}
		}
	}
}

// isForegroundColourParam reports whether p sets the foreground colour
// directly (classic 30-37, default 39, bright non-black 91-97).
func isForegroundColourParam(p string) bool {
	switch p {
	case "30", "31", "32", "33", "34", "35", "36", "37", "39",
		"91", "92", "93", "94", "95", "96", "97":
		return true
	}
	return false
}

// composerCell is one visible (non-padding) character of a composer body and
// the rendering state it was painted in.
type composerCell struct {
	r  rune
	st sgrState
}

// composerCells decodes an ANSI-bearing composer body into its visible
// cells, skipping the padding Claude puts around the text. ok is false when
// the body ends inside an unterminated escape sequence.
func composerCells(s string) (cells []composerCell, ok bool) {
	var st sgrState
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && !isCSIFinal(s[j]) {
				j++
			}
			if j >= len(s) {
				return cells, false
			}
			if s[j] == 'm' {
				applySGR(s[i+2:j], &st)
			}
			i = j + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if !isComposerSpace(r) {
			cells = append(cells, composerCell{r: r, st: st})
		}
		i += size
	}
	return cells, true
}

// minNativeCursorTail is how many placeholder-styled cells must follow a
// plain first cell before the body reads as a suggestion drawn under a
// native terminal cursor. A single typed character followed by an inline
// completion is excluded separately (inlineCompletionSigil); the length
// floor keeps a stray dim glyph from qualifying.
const minNativeCursorTail = 2

// inlineCompletionSigil reports whether r is a first character after which
// Claude's composer shows an INLINE completion (dim text after real typed
// input): a slash command, @-mention, shell mode, memory, emoji shortcode.
// A one-character buffer starting with one of these, followed by dim text,
// is operator input plus a completion, not a suggestion.
func inlineCompletionSigil(r rune) bool {
	return strings.ContainsRune("/@!#&:?", r)
}

// bodyIsPlaceholder reports whether an ANSI-bearing composer body is a
// placeholder or prompt suggestion rather than operator input. It accepts
// the three renderings Claude uses for an empty composer's placeholder:
//
//   - every cell from the first on is dim or grey (unfocused terminal, and
//     the older autosuggestion; issue #1777 added the grey variants);
//   - the first cell is inverse (Claude's own cursor) and every following
//     cell is dim or grey (focused terminal, 2.1.295);
//   - the first cell is plain and at least minNativeCursorTail following
//     cells are all dim or grey (native terminal cursor mode), unless the
//     first cell is an inline-completion sigil.
//
// Real input is never dim, so a single plain cell after the first one
// rejects the inverse and native-cursor forms. Returns false for a body
// with no visible cell (an empty composer).
func bodyIsPlaceholder(s string) bool {
	cells, ok := composerCells(s)
	if !ok || len(cells) == 0 {
		return false
	}
	if cells[0].st.suggestionStyled() {
		return true
	}
	tail := cells[1:]
	for _, c := range tail {
		if !c.st.suggestionStyled() {
			return false
		}
	}
	if cells[0].st.inverse {
		return len(tail) > 0
	}
	return len(tail) >= minNativeCursorTail && !inlineCompletionSigil(cells[0].r)
}

func isCSIFinal(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// isComposerSpace covers the padding Claude puts between the marker and the
// body, including the NBSP it uses for cursor placement.
func isComposerSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\r' || r == ' '
}

// ComposerBodyIsSuggestion reports whether the composer in raw (ANSI-bearing)
// pane content currently shows a Claude placeholder or prompt suggestion
// (see bodyIsPlaceholder for the renderings) rather than operator input. The
// composer is the LAST marker line in the pane; earlier marker lines are
// submitted history.
func ComposerBodyIsSuggestion(raw string) bool {
	lines := strings.Split(raw, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		body, ok := composerMarkerBody(lines[i])
		if !ok {
			continue
		}
		return bodyIsPlaceholder(body)
	}
	return false
}

// ComposerHoldsUnsentMessage is HasUnsentComposerPrompt for a raw
// (ANSI-bearing) capture: it reports whether the composer still holds
// message, unsent. A composer showing a suggestion holds nothing, even when
// the suggestion's text happens to contain (or loosely match) message: a
// predicted next prompt can easily echo the last one, and reading it as our
// stuck message would aim a bare Enter at the suggestion. strip removes ANSI
// (pass tmux.StripANSI; nil means identity).
func ComposerHoldsUnsentMessage(raw, message string, strip func(string) string) bool {
	if ComposerBodyIsSuggestion(raw) {
		return false
	}
	return HasUnsentComposerPrompt(orIdentity(strip)(raw), message)
}
