package send

import (
	"strings"
	"unicode/utf8"
)

// Claude Code renders a prompt autosuggestion (its shell-style ghost
// completion) in the composer using the SGR dim/faint attribute:
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
// Autosuggestions are accept-key semantics (Tab / Right arrow instantiate
// them), NOT placeholder semantics, so they must never be treated as content
// to preserve. The helpers below read the dim attribute off the RAW pane
// capture (tmux capture-pane -e, which CapturePaneFresh already requests)
// before it is stripped.

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

// applySGR folds one SGR parameter list into the running dim state.
//
// Extended-colour introducers (38/48/58) consume their own parameters —
// `38;5;2` is colour index 2, NOT the dim attribute — so they are skipped
// explicitly. Without that, the bright-white `38;5;231` Claude uses for
// submitted messages would be misread.
func applySGR(params string, dim *bool) {
	if params == "" {
		// A bare CSI m is an implicit reset.
		*dim = false
		return
	}
	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); i++ {
		switch strings.TrimSpace(parts[i]) {
		case "38", "48", "58":
			if i+1 < len(parts) {
				switch strings.TrimSpace(parts[i+1]) {
				case "5":
					i += 2 // 5;n
				case "2":
					i += 4 // 2;r;g;b
				default:
					i++
				}
			}
		case "2":
			*dim = true
		case "0", "00", "22":
			*dim = false
		}
	}
}

// bodyStartsDim reports whether the first visible (non-whitespace) character
// in an ANSI-bearing composer body is rendered dim. Returns false when the
// body holds no visible character at all (an empty composer).
func bodyStartsDim(s string) bool {
	dim := false
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && !isCSIFinal(s[j]) {
				j++
			}
			if j < len(s) {
				if s[j] == 'm' {
					applySGR(s[i+2:j], &dim)
				}
				i = j + 1
				continue
			}
			return false
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if !isComposerSpace(r) {
			return dim
		}
		i += size
	}
	return false
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
// pane content currently shows a dim-rendered Claude autosuggestion rather
// than operator input. The composer is the LAST marker line in the pane;
// earlier marker lines are submitted history.
func ComposerBodyIsSuggestion(raw string) bool {
	lines := strings.Split(raw, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		body, ok := composerMarkerBody(lines[i])
		if !ok {
			continue
		}
		return bodyStartsDim(body)
	}
	return false
}

// ComposerDraft and ComposerHasDraft (guard.go) consume ComposerBodyIsSuggestion
// directly, so every caller gets the dim check for free.
