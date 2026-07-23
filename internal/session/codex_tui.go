package session

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

// ApplyCodexTUISettings merges Agent Deck-managed TUI defaults into one
// CODEX_HOME/config.toml while preserving every unrelated setting.
func ApplyCodexTUISettings(codexHome string, settings *CodexTUISettings) error {
	if !hasManagedCodexTUISettings(settings) {
		return nil
	}
	store, err := newHomeSkillStore(codexHome, "Codex")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(store.home, 0o700); err != nil {
		return fmt.Errorf("create Codex home %s: %w", store.home, err)
	}
	configPath := filepath.Join(store.home, "config.toml")
	lock, err := acquireCodexConfigLock(configPath)
	if err != nil {
		return err
	}
	defer lock.Release()

	var existingData []byte
	cfg := map[string]any{}
	if data, readErr := os.ReadFile(configPath); readErr == nil {
		existingData = data
		if len(existingData) > 0 {
			if err := toml.Unmarshal(existingData, &cfg); err != nil {
				return fmt.Errorf("refusing to overwrite unparseable Codex config %s: %w", configPath, err)
			}
		}
	} else if !os.IsNotExist(readErr) {
		return fmt.Errorf("read Codex config %s: %w", configPath, readErr)
	}

	tui, _ := cfg["tui"].(map[string]any)
	if tui == nil {
		tui = map[string]any{}
	}
	changed := false
	if settings.StatusLine != nil && !equalCodexStatusLine(tui["status_line"], *settings.StatusLine) {
		changed = true
	}
	if settings.StatusLineUseColors != nil && tui["status_line_use_colors"] != *settings.StatusLineUseColors {
		changed = true
	}
	if !changed {
		return nil
	}
	data, err := composeCodexTUIConfig(existingData, settings)
	if err != nil {
		return fmt.Errorf("marshal Codex TUI config: %w", err)
	}
	var verified map[string]any
	if err := toml.Unmarshal(data, &verified); err != nil {
		return fmt.Errorf("refusing to write invalid Codex TUI config: %w", err)
	}
	if err := atomicfile.WriteFile(configPath, data, 0o600); err != nil {
		return fmt.Errorf("save Codex TUI config %s: %w", configPath, err)
	}
	return nil
}

func hasManagedCodexTUISettings(settings *CodexTUISettings) bool {
	return settings != nil && (settings.StatusLine != nil || settings.StatusLineUseColors != nil)
}

func composeCodexTUIConfig(existing []byte, settings *CodexTUISettings) ([]byte, error) {
	replacements, err := encodeCodexTUIAssignments(settings)
	if err != nil {
		return nil, err
	}
	lines := strings.SplitAfter(string(existing), "\n")
	if len(existing) == 0 {
		lines = nil
	}

	var out strings.Builder
	inTUI := false
	foundTUI := false
	rootDottedTUI := false
	rootDottedFlushed := false
	var currentTable []string
	var scanState codexTOMLScanState
	nestingDepth := 0
	emitted := make(map[string]bool, len(replacements))
	emitMissing := func(newline, prefix string) {
		for _, key := range []string{"status_line", "status_line_use_colors"} {
			line, managed := replacements[key]
			if managed && !emitted[key] {
				out.WriteString(prefix)
				out.WriteString(line)
				out.WriteString(newline)
				emitted[key] = true
			}
		}
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		structuralStart := scanState.multiline == 0 && nestingDepth == 0
		if structuralStart {
			if tablePath, ok := codexTOMLTablePath(line); ok {
				if inTUI {
					emitMissing(preferredLineEnding(existing), "")
				}
				if len(currentTable) == 0 && rootDottedTUI && !rootDottedFlushed {
					emitMissing(preferredLineEnding(existing), "tui.")
					rootDottedFlushed = true
				}
				currentTable = tablePath
				inTUI = reflect.DeepEqual(tablePath, []string{"tui"})
				foundTUI = foundTUI || inTUI
				out.WriteString(line)
				nestingDepth += scanCodexTOMLLine(line, &scanState)
				continue
			}
		}
		if structuralStart {
			if key, dotted, ok := codexTUIManagedAssignment(line, currentTable); ok {
				if dotted {
					rootDottedTUI = true
					foundTUI = true
				}
				replacement, managed := replacements[key]
				if managed {
					out.WriteString(leadingWhitespace(line))
					if dotted {
						out.WriteString("tui.")
					}
					out.WriteString(replacement)
					out.WriteString(lineEnding(line))
					emitted[key] = true
					foundTUI = true
					i = codexTUIAssignmentEnd(lines, i)
					continue
				}
			}
		}
		out.WriteString(line)
		nestingDepth += scanCodexTOMLLine(line, &scanState)
		if nestingDepth < 0 {
			nestingDepth = 0
		}
	}

	if inTUI {
		newline := preferredLineEnding(existing)
		if out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") {
			out.WriteString(newline)
		}
		emitMissing(newline, "")
		return []byte(out.String()), nil
	}
	if rootDottedTUI && !rootDottedFlushed {
		newline := preferredLineEnding(existing)
		if out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") {
			out.WriteString(newline)
		}
		emitMissing(newline, "tui.")
		return []byte(out.String()), nil
	}
	if foundTUI {
		return []byte(out.String()), nil
	}

	newline := preferredLineEnding(existing)
	if out.Len() > 0 {
		if !strings.HasSuffix(out.String(), "\n") {
			out.WriteString(newline)
		}
		out.WriteString(newline)
	}
	out.WriteString("[tui]")
	out.WriteString(newline)
	emitMissing(newline, "")
	return []byte(out.String()), nil
}

type codexTOMLScanState struct {
	multiline byte
}

// scanCodexTOMLLine tracks structural nesting while ignoring comments and all
// TOML string forms. This keeps table-looking and assignment-looking text
// inside multiline strings from being edited as configuration.
func scanCodexTOMLLine(line string, state *codexTOMLScanState) int {
	depthDelta := 0
	for i := 0; i < len(line); {
		if state.multiline != 0 {
			quote := state.multiline
			if i+2 < len(line) && line[i] == quote && line[i+1] == quote && line[i+2] == quote &&
				(quote == '\'' || !codexTOMLQuoteEscaped(line, i)) {
				state.multiline = 0
				i += 3
				continue
			}
			i++
			continue
		}
		switch line[i] {
		case '#':
			return depthDelta
		case '"', '\'':
			quote := line[i]
			if i+2 < len(line) && line[i+1] == quote && line[i+2] == quote {
				state.multiline = quote
				i += 3
				continue
			}
			i++
			for i < len(line) {
				if line[i] == quote && (quote == '\'' || !codexTOMLQuoteEscaped(line, i)) {
					i++
					break
				}
				i++
			}
		case '[', '{':
			depthDelta++
			i++
		case ']', '}':
			depthDelta--
			i++
		default:
			i++
		}
	}
	return depthDelta
}

func codexTOMLQuoteEscaped(line string, quoteIndex int) bool {
	backslashes := 0
	for i := quoteIndex - 1; i >= 0 && line[i] == '\\'; i-- {
		backslashes++
	}
	return backslashes%2 == 1
}

func codexTOMLTablePath(line string) ([]string, bool) {
	tableName, ok := tomlTableName(line)
	if !ok || strings.HasPrefix(strings.TrimSpace(line), "[[") {
		return nil, false
	}
	var decoded map[string]any
	snippet := "[" + tableName + "]\n__agent_deck_marker__ = true\n"
	if _, err := toml.Decode(snippet, &decoded); err != nil {
		return nil, false
	}
	return codexTOMLPathToMarker(decoded, "__agent_deck_marker__")
}

func codexTOMLPathToMarker(node map[string]any, marker string) ([]string, bool) {
	if value, ok := node[marker]; ok && value == true {
		return []string{}, true
	}
	if len(node) != 1 {
		return nil, false
	}
	for key, value := range node {
		child, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		path, found := codexTOMLPathToMarker(child, marker)
		if !found {
			return nil, false
		}
		return append([]string{key}, path...), true
	}
	return nil, false
}

func codexTUIManagedAssignment(line string, currentTable []string) (key string, dotted bool, ok bool) {
	lhs, ok := codexTOMLAssignmentLHS(line)
	if !ok {
		return "", false, false
	}
	var decoded map[string]any
	if _, err := toml.Decode(lhs+" = true\n", &decoded); err != nil {
		return "", false, false
	}
	path, found := codexTOMLPathToMarkerValue(decoded)
	if !found {
		return "", false, false
	}
	switch {
	case reflect.DeepEqual(currentTable, []string{"tui"}) && len(path) == 1:
		key = path[0]
	case len(currentTable) == 0 && len(path) == 2 && path[0] == "tui":
		key, dotted = path[1], true
	default:
		return "", false, false
	}
	switch key {
	case "status_line", "status_line_use_colors":
		return key, dotted, true
	default:
		return "", false, false
	}
}

func codexTOMLPathToMarkerValue(node map[string]any) ([]string, bool) {
	if len(node) != 1 {
		return nil, false
	}
	for key, value := range node {
		if value == true {
			return []string{key}, true
		}
		child, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		path, found := codexTOMLPathToMarkerValue(child)
		if !found {
			return nil, false
		}
		return append([]string{key}, path...), true
	}
	return nil, false
}

func codexTOMLAssignmentLHS(line string) (string, bool) {
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '#':
			return "", false
		case '"', '\'':
			quote := line[i]
			i++
			for i < len(line) && (line[i] != quote || (quote == '"' && codexTOMLQuoteEscaped(line, i))) {
				i++
			}
		case '=':
			lhs := strings.TrimSpace(line[:i])
			return lhs, lhs != ""
		}
	}
	return "", false
}

func encodeCodexTUIAssignments(settings *CodexTUISettings) (map[string]string, error) {
	type tuiAssignments struct {
		StatusLine          *[]string `toml:"status_line,omitempty"`
		StatusLineUseColors *bool     `toml:"status_line_use_colors,omitempty"`
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(tuiAssignments{
		StatusLine:          settings.StatusLine,
		StatusLineUseColors: settings.StatusLineUseColors,
	}); err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n") {
		if key, ok := codexTUIAssignmentKey(line); ok {
			result[key] = strings.TrimSpace(line)
		}
	}
	return result, nil
}

func codexTUIAssignmentKey(line string) (string, bool) {
	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", false
	}
	eq := strings.IndexByte(trimmed, '=')
	if eq < 0 {
		return "", false
	}
	key := strings.TrimSpace(trimmed[:eq])
	key = strings.Trim(key, `"'`)
	switch key {
	case "status_line", "status_line_use_colors":
		return key, true
	default:
		return "", false
	}
}

func codexTUIAssignmentEnd(lines []string, start int) int {
	var fragment strings.Builder
	for i := start; i < len(lines); i++ {
		if i > start {
			if _, ok := tomlTableName(lines[i]); ok {
				return i - 1
			}
		}
		fragment.WriteString(lines[i])
		var decoded map[string]any
		if _, err := toml.Decode("[tui]\n"+fragment.String(), &decoded); err == nil {
			return i
		}
	}
	return len(lines) - 1
}

func leadingWhitespace(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

func lineEnding(line string) string {
	if strings.HasSuffix(line, "\r\n") {
		return "\r\n"
	}
	if strings.HasSuffix(line, "\n") {
		return "\n"
	}
	return ""
}

func preferredLineEnding(data []byte) string {
	if bytes.Contains(data, []byte("\r\n")) {
		return "\r\n"
	}
	return "\n"
}

func equalCodexStatusLine(value any, want []string) bool {
	switch items := value.(type) {
	case []string:
		return reflect.DeepEqual(items, want)
	case []any:
		got := make([]string, len(items))
		for i, item := range items {
			text, ok := item.(string)
			if !ok {
				return false
			}
			got[i] = text
		}
		return reflect.DeepEqual(got, want)
	default:
		return false
	}
}
