package session

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/procowner"
	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Hands-free account switch (SWFB1018). After a switch restarts a Claude
// session on another account, the new process meets the target account's
// first-start state: its own folder-trust list, onboarding and the bypass
// acknowledgement. Field evidence: sessions sat at "Quick safety check" or
// exited, plugins came up CONNECTION_CLOSED while the old poller still held
// the bot token, and background work was lost until someone nudged them.
//
// SettleSwitch drives one restarted session to a known end state without a
// human: wait for ready, answer only the known first-start prompts (matched
// exactly, for the session's own project path), confirm plugins and MCPs
// connected (one retry), send a continue kick, verify the account in use and
// return a one-line receipt. Anything it does not recognise stops it with
// nothing pressed.

// SettlePaneState is what a captured pane shows during settle.
type SettlePaneState string

const (
	SettlePaneStarting     SettlePaneState = "starting"
	SettlePaneReady        SettlePaneState = "ready"
	SettlePaneTrustOwn     SettlePaneState = "trust-own-path"
	SettlePaneTrustOther   SettlePaneState = "trust-other-path"
	SettlePaneTheme        SettlePaneState = "theme-picker"
	SettlePaneOnboarding   SettlePaneState = "onboarding-continue"
	SettlePaneBypass       SettlePaneState = "bypass-ack"
	SettlePaneUnknownMenu  SettlePaneState = "unknown-prompt"
	SettlePanePermission   SettlePaneState = "permission-prompt"
	SettlePaneLoginNeeded  SettlePaneState = "login-required"
	SettlePaneShellOrEnded SettlePaneState = "exited"
)

var (
	// Each first-start screen is recognised only by its own layout: its
	// title at the start of a line, then an option block (the contiguous
	// rows around the cursor) made only of that screen's own labels. Words
	// of a screen anywhere else (a command in a permission prompt, a
	// reply) never match one.
	settleTrustTitles  = []string{"Quick safety check:", "Do you trust the files in this folder?"}
	settleBypassTitles = []string{"WARNING: Claude Code running in Bypass Permissions mode"}
	settleThemeTitles  = []string{"Choose the text style that looks best with your terminal"}
	// Option rows, matched exactly after the cursor, a "N." number and the
	// frame are stripped. A live capture of the trust dialog draws the
	// cursor on "No, exit" first, so Enter alone would exit the session.
	settleTrustYes  = []string{"Yes, I trust this folder", "Yes, proceed"}
	settleBypassYes = []string{"Yes, I accept"}
	settleNoExit    = []string{"No, exit"}
	settleThemeRow  = regexp.MustCompile(`^(Dark|Light) mode( \((colorblind-friendly|ANSI colors only)\))?$`)
	// Onboarding notes: the title alone on its line, a continue line after
	// it, and no option rows at all.
	settleSecurityNotes = "Security notes:"
	settleContinueLine  = "Press Enter to continue"
	// Tool-permission and approval prompts. Any of these on screen stops
	// settle with nothing pressed, whatever else the screen shows.
	settlePermissionMarkers = []string{
		"Do you want to", "Would you like to", "Yes, and don't ask again", "No, and tell Claude what to do differently",
		"Allow once", "Allow always", "Yes, allow", "Run this command?", "Allow this MCP server", "Allow execution of",
		"Action Required", "Waiting for user confirmation",
	}
	settleLoginMarkers = []string{"Select login method", "Please run /login", "Invalid API key", "OAuth token has expired"}
	settleMenuCursor   = regexp.MustCompile(`^[\s│]*❯\s*\d+\.\s+\S`)
	settleOptionNumber = regexp.MustCompile(`^\d+\.\s+`)
	settleMenuHints    = []string{"Enter to confirm", "Enter to select", "Esc to cancel", "Use arrow keys to navigate", "Press Enter to continue"}
	settleBoxChars     = "│╭╮╰╯─ \t"
)

// SettlePane is one classification with the evidence behind it.
type SettlePane struct {
	State  SettlePaneState
	Detail string
}

// ClassifySettlePane reads a pane capture (ANSI allowed). projectPaths are
// the spellings of the session's own project path; a trust dialog for any
// other path is reported, never answered.
func ClassifySettlePane(raw string, projectPaths []string) SettlePane {
	content := tmux.StripANSI(raw)
	// A dialog replaces Claude's composer. With a composer on screen, any
	// dialog text above it is conversation history (a reply quoting the trust
	// question), never a live prompt.
	lines := liveDialogRegion(strings.Split(content, "\n"))
	// A permission or approval prompt is never a first-start screen, even
	// when what it shows (a command, a diff) carries a screen's words.
	if marker := permissionPromptLine(lines); marker != "" {
		return SettlePane{State: SettlePanePermission, Detail: marker}
	}
	flat := flattenPane(lines)
	for _, marker := range settleLoginMarkers {
		if strings.Contains(flat, marker) {
			return SettlePane{State: SettlePaneLoginNeeded, Detail: marker}
		}
	}
	if title := titleLine(lines, settleTrustTitles); title >= 0 {
		_, _, live := yesNoBlock(lines, title, settleTrustYes, settleNoExit)
		shown, own := trustDialogPath(lines, projectPaths)
		if live && own {
			return SettlePane{State: SettlePaneTrustOwn, Detail: shown}
		}
		if shown == "" {
			shown = "path not shown"
		}
		if own {
			// The own path, but not as the only menu on screen.
			return SettlePane{State: SettlePaneTrustOther, Detail: "folder trust for " + shown + settleTrustNotAlone}
		}
		return SettlePane{State: SettlePaneTrustOther, Detail: "folder trust for " + shown}
	}
	if title := titleLine(lines, settleBypassTitles); title >= 0 {
		if _, _, live := yesNoBlock(lines, title, settleBypassYes, settleNoExit); live {
			return SettlePane{State: SettlePaneBypass, Detail: settleBypassTitles[0]}
		}
	}
	if themeScreen(lines) {
		return SettlePane{State: SettlePaneTheme, Detail: settleThemeTitles[0]}
	}
	if securityNotesScreen(lines) {
		return SettlePane{State: SettlePaneOnboarding, Detail: settleSecurityNotes}
	}
	if menu := openMenuLine(lines); menu != "" {
		return SettlePane{State: SettlePaneUnknownMenu, Detail: menu}
	}
	if send.HasCurrentComposerPrompt(content) {
		return SettlePane{State: SettlePaneReady}
	}
	return SettlePane{State: SettlePaneStarting}
}

// permissionPromptLine returns the first line that carries a permission or
// approval marker, or "".
func permissionPromptLine(lines []string) string {
	for _, line := range lines {
		text := strings.Join(strings.Fields(strings.Trim(line, settleBoxChars)), " ")
		for _, marker := range settlePermissionMarkers {
			if strings.Contains(text, marker) {
				return text
			}
		}
	}
	// A narrow pane wraps a marker across lines.
	flat := flattenPane(lines)
	for _, marker := range settlePermissionMarkers {
		if strings.Contains(flat, marker) {
			return marker + " (wrapped)"
		}
	}
	return ""
}

// titleLine is the index of the last line that starts with one of titles
// (frame and indentation stripped), or -1.
func titleLine(lines []string, titles []string) int {
	for i := len(lines) - 1; i >= 0; i-- {
		text := strings.Join(strings.Fields(strings.Trim(lines[i], settleBoxChars)), " ")
		for _, title := range titles {
			if strings.HasPrefix(text, title) {
				return i
			}
		}
	}
	return -1
}

// settleOptionRow is one row of an option block.
type settleOptionRow struct {
	idx    int
	text   string
	cursor bool
}

// optionBlockAfter returns the first option block below line after: the
// first row carrying the cursor and the contiguous non-blank rows around it
// (never reaching back to line after), up to a hint line. ok is false when no
// cursor row follows within maxGap lines.
func optionBlockAfter(lines []string, after, maxGap int) ([]settleOptionRow, bool) {
	cursor := -1
	for i := after + 1; i < len(lines) && i <= after+maxGap; i++ {
		if _, isCursor := optionText(lines[i]); isCursor {
			cursor = i
			break
		}
	}
	if cursor < 0 {
		return nil, false
	}
	blank := func(i int) bool { return strings.Trim(lines[i], settleBoxChars) == "" }
	hint := func(i int) bool {
		for _, h := range settleMenuHints {
			if strings.Contains(lines[i], h) {
				return true
			}
		}
		return false
	}
	start, end := cursor, cursor
	for start-1 > after && !blank(start-1) && !hint(start-1) {
		start--
	}
	for end+1 < len(lines) && !blank(end+1) && !hint(end+1) {
		end++
	}
	rows := make([]settleOptionRow, 0, end-start+1)
	for i := start; i <= end; i++ {
		text, isCursor := optionText(lines[i])
		rows = append(rows, settleOptionRow{idx: i, text: text, cursor: isCursor})
	}
	return rows, true
}

// settleOptionLike matches a row that belongs to some menu: a cursor row
// (❯ or ›) or a numbered option row, after the frame is stripped.
var settleOptionLike = regexp.MustCompile(`^(❯|›)\s*\S|^\d+\.\s+\S`)

// soleMenuBlock is the structural guard on every answer: the block must be
// the only menu in the region (no cursor row or numbered option row
// anywhere outside it) and the last thing on screen (only blank and hint
// lines after it). A screen's block copied into earlier tool output above a
// live menu, or a live menu under or around a decoy, therefore never gets a
// key: any second menu means stop.
func soleMenuBlock(lines []string, rows []settleOptionRow) bool {
	if len(rows) == 0 {
		return false
	}
	start, end := rows[0].idx, rows[len(rows)-1].idx
	for i, line := range lines {
		if i >= start && i <= end {
			continue
		}
		text := strings.TrimSpace(strings.Trim(line, settleBoxChars))
		if settleOptionLike.MatchString(text) {
			return false
		}
		if i > end && text != "" && !settleHintLine(text) {
			return false
		}
	}
	return true
}

// settleBodyMenuLike matches a row of any menu layout, Claude's or not: a
// cursor glyph (❯ › > * •), a checkbox or radio, "N." or "N)" numbering, or
// buttons. A screen's body (its prose between title and block) never has one.
var settleBodyMenuLike = regexp.MustCompile(`^(❯|›|>|\*|•|\[.?\]|\(.?\))\s*\S|^\d+[.)]\s+\S|\[\s*[^\]]+\s*\]\s+\[`)

// screenIsAlone is the allow-list guard: the pane holds the known screen and
// nothing else. Before its first line (start) only blank or frame-only lines,
// or a header the screen itself draws (header); between start and the
// option block (or the end line) only the screen's own prose, never a
// menu-like row of any layout (body may allow more); after it only blank or
// hint lines. Any other line, in any layout, means it is not that screen.
func screenIsAlone(lines []string, start, blockStart, blockEnd int, header, body func(string) bool) bool {
	for i, line := range lines {
		text := strings.Join(strings.Fields(strings.Trim(line, settleBoxChars)), " ")
		switch {
		case text == "":
		case i < start:
			if header == nil || !header(text) {
				return false
			}
		case i > start && i < blockStart:
			if body != nil {
				if !body(text) {
					return false
				}
			} else if settleBodyMenuLike.MatchString(text) {
				return false
			}
		case i > blockEnd:
			if !settleHintLine(text) {
				return false
			}
		}
	}
	return true
}

// settleHintLine is a key-hint footer line ("Enter to confirm · Esc to
// cancel").
func settleHintLine(text string) bool {
	for _, h := range settleMenuHints {
		if strings.Contains(text, h) {
			return true
		}
	}
	return false
}

// yesNoBlock checks that the option block under the title is exactly the
// two rows of a Yes/No dialog, one each, with the cursor on one of them; it
// returns the Yes row and the cursor row. Any other row, or a third row,
// makes it not that dialog.
func yesNoBlock(lines []string, title int, yes, no []string) (yesRow, cursorRow int, live bool) {
	rows, ok := optionBlockAfter(lines, title, 16)
	if !ok || len(rows) != 2 || !soleMenuBlock(lines, rows) ||
		!screenIsAlone(lines, yesNoScreenStart(lines, title), rows[0].idx, rows[len(rows)-1].idx, nil, nil) {
		return -1, -1, false
	}
	yesRow, cursorRow = -1, -1
	noSeen := false
	for _, row := range rows {
		switch {
		case containsString(yes, row.text):
			yesRow = row.idx
		case containsString(no, row.text):
			noSeen = true
		default:
			return -1, -1, false
		}
		if row.cursor {
			cursorRow = row.idx
		}
	}
	return yesRow, cursorRow, yesRow >= 0 && noSeen && cursorRow >= 0
}

// yesNoScreenStart is the first line of a Yes/No screen: for the trust
// dialog its "Accessing workspace:" block just above the title, else the
// title itself.
func yesNoScreenStart(lines []string, title int) int {
	for i := title - 1; i >= 0 && i >= title-8; i-- {
		if strings.HasPrefix(strings.TrimSpace(strings.Trim(lines[i], settleBoxChars)), "Accessing workspace:") {
			return i
		}
	}
	return title
}

// themeScreen is the theme picker: its title at the start of a line, then
// an option block of theme rows only, with the cursor on one.
func themeScreen(lines []string) bool {
	title := titleLine(lines, settleThemeTitles)
	if title < 0 {
		return false
	}
	rows, ok := optionBlockAfter(lines, title, 4)
	if !ok || len(rows) < 2 || !soleMenuBlock(lines, rows) {
		return false
	}
	header := func(text string) bool { return text == "Let's get started." }
	body := func(text string) bool { return text == "To change this later, run /theme" }
	if !screenIsAlone(lines, title, rows[0].idx, rows[len(rows)-1].idx, header, body) {
		return false
	}
	for _, row := range rows {
		if !settleThemeRow.MatchString(row.text) {
			return false
		}
	}
	return true
}

// securityNotesScreen is the onboarding notes: "Security notes:" alone on
// its line, a "Press Enter to continue" line after it, and no option rows
// anywhere (a screen with a cursor row asks something else).
func securityNotesScreen(lines []string) bool {
	title, cont := -1, -1
	for i, line := range lines {
		text := strings.TrimSpace(strings.Trim(line, settleBoxChars))
		if _, isCursor := optionText(line); isCursor {
			return false
		}
		if text == settleSecurityNotes && title < 0 {
			title = i
		}
		if title >= 0 && i > title && strings.HasPrefix(text, settleContinueLine) {
			cont = i
		}
	}
	if title < 0 || cont <= title {
		return false
	}
	// The notes are numbered ("1. Claude can make mistakes"); any other
	// menu-like row, and anything at all before the title, is not this
	// screen.
	notesBody := func(text string) bool {
		return regexp.MustCompile(`^\d+\.\s+\S`).MatchString(text) || !settleBodyMenuLike.MatchString(text)
	}
	if !screenIsAlone(lines, title, cont, cont, nil, notesBody) {
		return false
	}
	// The continue line ends the screen: nothing but blank or hint lines
	// after it.
	for i := cont + 1; i < len(lines); i++ {
		if text := strings.TrimSpace(strings.Trim(lines[i], settleBoxChars)); text != "" && !settleHintLine(text) {
			return false
		}
	}
	return true
}

// liveDialogRegion returns the lines below the last composer on screen, or
// every line when there is none. The composer is a bare "❯" line, or the
// prompt line inside a frame of two rules that is not an option row.
func liveDialogRegion(lines []string) []string {
	last := -1
	for i, line := range lines {
		if t := strings.TrimSpace(line); t == "❯" || t == "›" {
			last = i
		}
	}
	if top, bottom := send.ComposerBounds(lines); top >= 0 && bottom > top+1 {
		for i := top + 1; i < bottom; i++ {
			t := strings.TrimSpace(lines[i])
			if (strings.HasPrefix(t, "❯") || strings.HasPrefix(t, "›")) && !isOptionRow(lines[i]) && bottom > last {
				// The whole frame is the composer: a draft inside it
				// (even one holding a screen's block) is never a dialog.
				last = bottom
			}
		}
	}
	if last < 0 {
		return lines
	}
	return lines[last+1:]
}

// optionText strips the frame, the cursor and a "N." number from a row and
// reports whether the cursor was on it.
func optionText(line string) (string, bool) {
	t := strings.Trim(line, settleBoxChars)
	cursor := strings.HasPrefix(t, "❯")
	t = strings.TrimSpace(strings.TrimPrefix(t, "❯"))
	t = settleOptionNumber.ReplaceAllString(t, "")
	t = strings.TrimSpace(strings.TrimSuffix(t, "✔"))
	return t, cursor
}

func isOptionRow(line string) bool {
	text, cursor := optionText(line)
	return cursor && (settleMenuCursor.MatchString(line) || containsString(settleNoExit, text) ||
		containsString(settleTrustYes, text) || containsString(settleBypassYes, text))
}

func containsString(list []string, v string) bool {
	for _, have := range list {
		if have == v {
			return true
		}
	}
	return false
}

// dialogChoice finds the live Yes/No dialog on screen: the title of one of
// the known Yes/No screens, then its exact two-row block.
func dialogChoice(lines []string, yes, no []string) (yesRow, cursorRow int, live bool) {
	titles := settleTrustTitles
	if containsString(yes, settleBypassYes[0]) {
		titles = settleBypassTitles
	}
	title := titleLine(lines, titles)
	if title < 0 {
		return -1, -1, false
	}
	return yesNoBlock(lines, title, yes, no)
}

// flattenPane joins the pane's lines with single spaces, frame characters
// and indentation removed.
func flattenPane(lines []string) string {
	words := make([]string, 0, len(lines))
	for _, line := range lines {
		if text := strings.Trim(line, settleBoxChars); text != "" {
			words = append(words, strings.Join(strings.Fields(text), " "))
		}
	}
	return strings.Join(words, " ")
}

// openMenuLine returns a line that marks an open picker or dialog, or "".
//
// Claude draws a picker or dialog in place of its composer. When the
// composer frame is on screen, the lines inside it are the user's draft
// ("❯ 1. fix the bug" is not a menu) and the lines above it are history, so
// only the footer below it is read.
func openMenuLine(lines []string) string {
	from := 0
	if top, bottom := send.ComposerBounds(lines); top >= 0 {
		from = bottom + 1
	}
	for i := len(lines) - 1; i >= from; i-- {
		line := strings.TrimSpace(lines[i])
		if settleMenuCursor.MatchString(lines[i]) {
			return strings.Trim(line, settleBoxChars)
		}
		for _, hint := range settleMenuHints {
			if strings.Contains(line, hint) {
				return firstDialogLine(lines, i)
			}
		}
	}
	return ""
}

// firstDialogLine names a dialog by the first non-empty line above its hint,
// for a report a person can act on.
func firstDialogLine(lines []string, hint int) string {
	start := hint - 12
	if start < 0 {
		start = 0
	}
	for i := start; i <= hint; i++ {
		if text := strings.Trim(lines[i], settleBoxChars); text != "" {
			return text
		}
	}
	return strings.Trim(lines[hint], settleBoxChars)
}

// trustDialogPath reads the workspace path the dialog itself shows: the
// block right under "Accessing workspace:" (or under the older "Do you trust
// the files in this folder?"), up to the next blank line or the question. A
// narrow pane wraps a long path, so the block's lines are joined; the whole
// block must equal one of projectPaths. Any other path on screen, and a
// longer path that merely starts with the session's own, never match.
func trustDialogPath(lines []string, projectPaths []string) (string, bool) {
	want := map[string]bool{}
	for _, p := range projectPaths {
		if p = strings.TrimRight(strings.TrimSpace(p), "/"); p != "" {
			want[p] = true
		}
	}
	// The anchor nearest the option rows is the live dialog; leftover text
	// of an earlier dialog above it is not.
	for i := len(lines) - 1; i >= 0; i-- {
		text := strings.Trim(lines[i], settleBoxChars)
		anchor := ""
		for _, a := range []string{"Accessing workspace:", "Do you trust the files in this folder?"} {
			if strings.HasPrefix(text, a) {
				anchor = a
			}
		}
		if anchor == "" {
			continue
		}
		var block []string
		if rest := strings.TrimSpace(strings.TrimPrefix(text, anchor)); rest != "" {
			block = append(block, rest)
		}
		for j := i + 1; j < len(lines) && len(block) < 6; j++ {
			t := strings.Trim(lines[j], settleBoxChars)
			if t == "" {
				if len(block) == 0 {
					continue
				}
				break
			}
			if strings.Contains(t, "Is this a project you created") || strings.Contains(t, "Quick safety check") {
				break
			}
			block = append(block, t)
		}
		shown := strings.Join(block, "")
		return shown, shown != "" && want[strings.TrimRight(shown, "/")]
	}
	return "", false
}

// SettleProjectPaths lists the spellings Claude may show or key trust by for
// dir: as typed, cleaned, with ~ expanded, and its realpath.
func SettleProjectPaths(dir string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		p = strings.TrimRight(p, "/")
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	add(dir)
	expanded := filepath.Clean(ExpandPath(dir))
	add(expanded)
	if real, err := filepath.EvalSymlinks(expanded); err == nil {
		add(real)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, p := range append([]string(nil), out...) {
			if strings.HasPrefix(p, home+"/") {
				add("~" + strings.TrimPrefix(p, home))
			}
		}
	}
	return out
}

// PreseedClaudeFirstStart prepares the target account's .claude.json so the
// restarted session meets no first-start prompt: folder trust for every
// spelling of the session's own project path, and — only when the source
// account already has them — the theme, the completed onboarding (for a
// target that is logged in) and the bypass acknowledgement (only when the
// session runs with it). Pre-seeding is preferred over pressing keys; the
// settle loop answers what a concurrent Claude rewrite still loses.
func PreseedClaudeFirstStart(sourceDir, targetDir, projectPath string, bypass bool) ([]string, error) {
	if targetDir == "" || projectPath == "" {
		return nil, fmt.Errorf("target config dir and project path are required")
	}
	var source map[string]any
	if sourceDir != "" {
		for _, path := range claudeJSONPathsForDir(sourceDir) {
			if data, err := os.ReadFile(path); err == nil && jsonUnmarshalLoose(data, &source) == nil && source != nil {
				break
			}
		}
	}
	var notes []string
	for _, targetJSON := range claudeJSONPathsForDir(targetDir) {
		seeded, err := preseedClaudeJSON(source, targetJSON, projectPath, bypass)
		notes = append(notes, seeded...)
		if err != nil {
			return notes, err
		}
	}
	return notes, nil
}

// claudeJSONPathsForDir lists the .claude.json files Claude may read for a
// config dir: <dir>/.claude.json, and for the default ~/.claude also
// ~/.claude.json, which Claude reads when CLAUDE_CONFIG_DIR is unset.
func claudeJSONPathsForDir(dir string) []string {
	inDir := filepath.Join(ExpandPath(dir), ".claude.json")
	if home, err := os.UserHomeDir(); err == nil && sameConfigDir(dir, filepath.Join(home, ".claude")) {
		// Never create a stub <dir>/.claude.json for the default account:
		// other readers take its presence as the login state.
		paths := []string{filepath.Join(home, ".claude.json")}
		if _, statErr := os.Stat(inDir); statErr == nil {
			paths = append(paths, inDir)
		}
		return paths
	}
	return []string{inDir}
}

func preseedClaudeJSON(source map[string]any, targetJSON, projectPath string, bypass bool) ([]string, error) {
	paths := SettleProjectPaths(projectPath)
	var notes []string
	recorded := false
	err := updateClaudeJSONLocked(targetJSON, func(cfg map[string]any) {
		// The locked writer re-applies mutate to verify; only the first
		// pass describes what changed.
		var changed []string
		defer func() {
			if !recorded {
				notes, recorded = changed, true
			}
		}()
		projects, _ := cfg["projects"].(map[string]any)
		if projects == nil {
			projects = map[string]any{}
		}
		for _, p := range paths {
			if strings.HasPrefix(p, "~") {
				continue // Claude keys trust by absolute path
			}
			entry, _ := projects[p].(map[string]any)
			if entry == nil {
				entry = map[string]any{}
			}
			if entry["hasTrustDialogAccepted"] != true {
				entry["hasTrustDialogAccepted"] = true
				changed = append(changed, "trust:"+p)
			}
			projects[p] = entry
		}
		cfg["projects"] = projects
		if source == nil {
			return
		}
		if theme, ok := source["theme"]; ok {
			if _, has := cfg["theme"]; !has {
				cfg["theme"] = theme
				changed = append(changed, "theme")
			}
		}
		_, loggedIn := cfg["oauthAccount"]
		if source["hasCompletedOnboarding"] == true && cfg["hasCompletedOnboarding"] != true && loggedIn {
			cfg["hasCompletedOnboarding"] = true
			if v, ok := source["lastOnboardingVersion"]; ok {
				cfg["lastOnboardingVersion"] = v
			}
			changed = append(changed, "onboarding")
		}
		if bypass && source["bypassPermissionsModeAccepted"] == true && cfg["bypassPermissionsModeAccepted"] != true {
			cfg["bypassPermissionsModeAccepted"] = true
			changed = append(changed, "bypass-ack")
		}
	})
	if err == nil {
		for _, note := range notes {
			if dir, ok := strings.CutPrefix(note, "trust:"); ok {
				logFolderTrustGranted("claude", dir, "config", targetJSON)
			}
		}
	}
	return notes, err
}

// SettleReport is the outcome of one settle run; Receipt renders it as the
// one line the owner reads.
type SettleReport struct {
	Title          string   `json:"title"`
	ID             string   `json:"id"`
	Account        string   `json:"account"`
	ConfigDir      string   `json:"config_dir"`
	Preseeded      []string `json:"preseeded,omitempty"`
	Ready          bool     `json:"ready"`
	Answered       []string `json:"answered_prompts,omitempty"`
	StoppedAt      string   `json:"stopped_at,omitempty"`
	StalePollers   []int    `json:"stale_pollers,omitempty"`
	PluginFailures []string `json:"plugin_failures,omitempty"`
	PluginRetried  bool     `json:"plugin_retried"`
	Kick           string   `json:"kick"`
	LiveConfigDir  string   `json:"live_config_dir,omitempty"`
	AccountOK      bool     `json:"account_verified"`
	AuthIdentity   string   `json:"auth_identity,omitempty"`
	Problems       []string `json:"problems,omitempty"`
}

// OK reports a fully healthy settle.
func (r *SettleReport) OK() bool {
	return r.Ready && r.StoppedAt == "" && len(r.PluginFailures) == 0 && r.AccountOK &&
		(r.Kick == "sent" || r.Kick == "skipped") && len(r.Problems) == 0
}

// Receipt is the single-line summary.
func (r *SettleReport) Receipt() string {
	verdict := "ok"
	if r.StoppedAt != "" {
		verdict = "STOPPED"
	} else if !r.OK() {
		verdict = "FAILED"
	}
	parts := []string{fmt.Sprintf("settle %s: %s", r.Title, verdict), "account=" + r.Account}
	if r.AccountOK {
		parts = append(parts, "verified")
	} else {
		parts = append(parts, "unverified(live="+orDash(r.LiveConfigDir)+")")
	}
	if r.AuthIdentity != "" {
		parts = append(parts, "auth="+r.AuthIdentity)
	}
	parts = append(parts, fmt.Sprintf("ready=%t", r.Ready))
	if len(r.Answered) > 0 {
		parts = append(parts, "answered="+strings.Join(r.Answered, ","))
	}
	if r.StoppedAt != "" {
		parts = append(parts, fmt.Sprintf("stopped-at=%q (no key sent to that screen)", r.StoppedAt))
	}
	if len(r.PluginFailures) > 0 {
		parts = append(parts, "plugins=FAILED:"+strings.Join(r.PluginFailures, ","))
	} else {
		parts = append(parts, "plugins=ok")
	}
	if r.PluginRetried {
		parts = append(parts, "plugin-retry=1")
	}
	parts = append(parts, "kick="+r.Kick)
	if len(r.Problems) > 0 {
		parts = append(parts, "problems="+strings.Join(r.Problems, "; "))
	}
	return strings.Join(parts, " ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// SettleDeps are the side effects settle needs; production wiring is in the
// CLI, tests inject fakes.
type SettleDeps struct {
	Capture func() (string, error)
	SendKey func(key string) error // tmux key name: "Enter", "Down"
	Restart func() error
	PanePID func() (int, error)
	// SessionPanePIDs lists every pane of the session (all windows); a
	// process under any of them is live, never an old poller. nil: only
	// PanePID's tree counts.
	SessionPanePIDs func() ([]int, error)
	Procs           SettleProcReader
	Kick            func(message string) error
	AuthInfo        func(configDir string) (string, error)
	Sleep           func(time.Duration)
	Now             func() time.Time
	Log             func(format string, args ...any)
}

// SettleOptions configure one run.
type SettleOptions struct {
	InstanceID   string
	Title        string
	Account      string
	ConfigDir    string // expected CLAUDE_CONFIG_DIR of the restarted process
	ProjectPath  string
	Bypass       bool // the session runs with --dangerously-skip-permissions
	Plugins      []string
	Kick         string // "" skips the kick
	ReadyTimeout time.Duration
	PollerWait   time.Duration
	PollEvery    time.Duration
	// OldPIDs are processes of the replaced instance (pane descendants
	// captured before the restart). Settle waits for them to be gone so a
	// new poller never races the old one for the same bot token.
	OldPIDs []int
}

// settleAnswerGrace is how long a just-answered dialog may still be on
// screen (a stale frame) before it counts as shown again.
const settleAnswerGrace = 3 * time.Second

// settleRedrawWait is how long an own-path trust dialog that is not yet the
// only menu on screen is sampled again before settle stops (#2893). Under
// load a capture can land while Claude is still drawing the dialog (title
// up, rows missing); that frame is refused, never answered, and the next
// ones usually show the whole dialog.
const settleRedrawWait = 3 * time.Second

// settleTrustNotAlone ends the detail of an own-path trust dialog refused
// because something else is on screen with it.
const settleTrustNotAlone = " is not the only menu on screen"

var errDialogGone = errors.New("dialog closed before it was answered")

// ErrSettleStopped marks a settle that met a prompt it may not answer.
var ErrSettleStopped = errors.New("settle stopped at an unknown prompt")

// DefaultSettleKick is the continue message after a switch.
func DefaultSettleKick(account string) string {
	return fmt.Sprintf("[agent-deck] This session was switched to account %s and restarted; the conversation was resumed. "+
		"Background shells, monitors and pollers from before the restart are gone: re-check your background work, "+
		"restart what is still needed, then continue where you left off.", account)
}

// SettleSwitch runs the hands-free settle.
func SettleSwitch(deps SettleDeps, opts SettleOptions) (*SettleReport, error) {
	if deps.Sleep == nil {
		deps.Sleep = time.Sleep
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Log == nil {
		deps.Log = func(string, ...any) {}
	}
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = 3 * time.Minute
	}
	if opts.PollerWait <= 0 {
		opts.PollerWait = 30 * time.Second
	}
	if opts.PollEvery <= 0 {
		opts.PollEvery = 500 * time.Millisecond
	}
	report := &SettleReport{Title: opts.Title, ID: opts.InstanceID, Account: opts.Account, ConfigDir: opts.ConfigDir, Kick: "not-sent"}

	report.StalePollers = waitOldProcessesGone(deps, opts)
	if err := settleToReady(deps, opts, report); err != nil {
		return report, err
	}
	if !report.Ready {
		report.Problems = append(report.Problems, "not ready within "+opts.ReadyTimeout.String())
		return report, nil
	}

	report.PluginFailures = checkPlugins(deps, opts)
	if len(report.PluginFailures) > 0 && deps.Restart != nil {
		// Retry once: the usual cause is the replaced process's poller still
		// holding the token. Wait for every process of the old pane, restart,
		// and settle again.
		report.PluginRetried = true
		deps.Log("plugins failed (%s); waiting for old pollers, retrying once", strings.Join(report.PluginFailures, ","))
		before := opts.OldPIDs
		if pid, err := deps.PanePID(); err == nil && deps.Procs != nil {
			before = append(before, settleDescendants(deps.Procs, pid)...)
		}
		if err := deps.Restart(); err != nil {
			report.Problems = append(report.Problems, "plugin retry restart: "+err.Error())
			return report, nil
		}
		retry := opts
		retry.OldPIDs = before
		report.StalePollers = waitOldProcessesGone(deps, retry)
		report.Ready = false
		if err := settleToReady(deps, retry, report); err != nil {
			return report, err
		}
		if !report.Ready {
			report.Problems = append(report.Problems, "not ready after plugin retry")
			return report, nil
		}
		report.PluginFailures = checkPlugins(deps, opts)
	}
	if len(report.StalePollers) > 0 {
		report.Problems = append(report.Problems, fmt.Sprintf("old processes still running: %v", report.StalePollers))
	}

	if opts.Kick == "" || deps.Kick == nil {
		report.Kick = "skipped"
	} else if err := deps.Kick(opts.Kick); err != nil {
		report.Kick = "failed"
		report.Problems = append(report.Problems, "kick: "+err.Error())
	} else {
		report.Kick = "sent"
	}

	verifyAccount(deps, opts, report)
	return report, nil
}

// settleToReady polls the pane until the composer is up, answering the known
// first-start prompts once each. It returns ErrSettleStopped for anything else.
func settleToReady(deps SettleDeps, opts SettleOptions, report *SettleReport) error {
	paths := SettleProjectPaths(opts.ProjectPath)
	deadline := deps.Now().Add(opts.ReadyTimeout)
	answered := map[SettlePaneState]int{}
	answeredAt := map[SettlePaneState]time.Time{}
	readySeen := 0
	var redrawSince time.Time
	for deps.Now().Before(deadline) {
		raw, err := deps.Capture()
		if err != nil {
			readySeen = 0
			deps.Sleep(opts.PollEvery)
			continue
		}
		pane := ClassifySettlePane(raw, paths)
		// A frame captured just after an answer may still show the dialog;
		// only one that is still up after the grace period is answered again.
		if at, ok := answeredAt[pane.State]; ok && deps.Now().Sub(at) < settleAnswerGrace {
			readySeen = 0
			deps.Sleep(opts.PollEvery)
			continue
		}
		switch pane.State {
		case SettlePaneReady:
			readySeen++
			if readySeen >= 3 {
				report.Ready = true
				return nil
			}
		case SettlePaneStarting:
			readySeen = 0
		case SettlePaneTrustOwn:
			readySeen = 0
			if answered[pane.State] >= 2 {
				report.StoppedAt = string(pane.State) + " keeps returning"
				return ErrSettleStopped
			}
			answered[pane.State]++
			deps.Log("answering %s (%s)", pane.State, pane.Detail)
			if err := selectDialogYes(deps, settleTrustYes, settleNoExit); errors.Is(err, errDialogGone) {
				continue
			} else if err != nil {
				report.StoppedAt = "folder trust: " + err.Error()
				return ErrSettleStopped
			}
			answeredAt[pane.State] = deps.Now()
			report.Answered = appendOnce(report.Answered, string(pane.State))
			logFolderTrustGranted("claude", opts.ProjectPath, "config_dir", opts.ConfigDir,
				slog.String("via", "dialog_keypress"))
			deps.Sleep(time.Second)
		case SettlePaneTheme, SettlePaneOnboarding:
			// Any theme is acceptable and the notes only continue, so
			// Enter on the highlighted row is the answer.
			readySeen = 0
			if answered[pane.State] >= 2 {
				report.StoppedAt = string(pane.State) + " keeps returning"
				return ErrSettleStopped
			}
			// Enter only on a screen positively matched twice: re-read it
			// right before the key.
			again, err := deps.Capture()
			if err != nil || ClassifySettlePane(again, paths).State != pane.State {
				deps.Sleep(opts.PollEvery)
				continue
			}
			answered[pane.State]++
			deps.Log("answering %s (%s)", pane.State, pane.Detail)
			if err := deps.SendKey("Enter"); err != nil {
				return fmt.Errorf("answer %s: %w", pane.State, err)
			}
			answeredAt[pane.State] = deps.Now()
			report.Answered = appendOnce(report.Answered, string(pane.State))
			deps.Sleep(time.Second)
		case SettlePaneBypass:
			readySeen = 0
			if !opts.Bypass || answered[pane.State] >= 1 {
				report.StoppedAt = pane.Detail + " (session did not run with it)"
				if opts.Bypass {
					report.StoppedAt = pane.Detail + " keeps returning"
				}
				return ErrSettleStopped
			}
			answered[pane.State]++
			if err := answerBypass(deps, opts); errors.Is(err, errDialogGone) {
				continue
			} else if err != nil {
				report.StoppedAt = err.Error()
				return ErrSettleStopped
			}
			answeredAt[pane.State] = deps.Now()
			report.Answered = appendOnce(report.Answered, string(pane.State))
		default:
			// A half-drawn own trust dialog: nothing is pressed, but look
			// again for a bounded time before stopping for good.
			if pane.State == SettlePaneTrustOther && strings.HasSuffix(pane.Detail, settleTrustNotAlone) {
				if redrawSince.IsZero() {
					redrawSince = deps.Now()
				}
				if deps.Now().Sub(redrawSince) < settleRedrawWait {
					readySeen = 0
					break
				}
			}
			report.StoppedAt = string(pane.State) + ": " + pane.Detail
			return ErrSettleStopped
		}
		deps.Sleep(opts.PollEvery)
	}
	return nil
}

// answerBypass accepts the bypass warning the same way as folder trust.
func answerBypass(deps SettleDeps, opts SettleOptions) error {
	return selectDialogYes(deps, settleBypassYes, settleNoExit)
}

// selectDialogYes moves the cursor onto the Yes row and presses Enter only
// when two consecutive captures agree the cursor is on it. A live capture of
// Claude's trust dialog draws the cursor on "No, exit" first, so a bare
// Enter exits the session. Each arrow is sent only after the previous one is
// seen to have moved the cursor, and never more arrows than the distance to
// Yes, so a slow repaint cannot queue extra moves that land after Enter.
func selectDialogYes(deps SettleDeps, yes, no []string) error {
	read := func() (yesRow, cursorRow int, err error) {
		raw, err := deps.Capture()
		if err != nil {
			return -1, -1, err
		}
		lines := liveDialogRegion(strings.Split(tmux.StripANSI(raw), "\n"))
		yesRow, cursorRow, live := dialogChoice(lines, yes, no)
		if !live {
			return -1, -1, fmt.Errorf("dialog rows not found (cursor or %q missing)", yes[0])
		}
		return yesRow, cursorRow, nil
	}
	yesRow, cursorRow, err := read()
	if err != nil {
		// Classified live a moment ago: the dialog closed in between.
		return errDialogGone
	}
	moves := yesRow - cursorRow
	key := "Down"
	if moves < 0 {
		key, moves = "Up", -moves
	}
	if moves > 3 {
		return fmt.Errorf("%q is %d rows from the cursor", yes[0], moves)
	}
	for ; moves > 0; moves-- {
		before := cursorRow
		if err := deps.SendKey(key); err != nil {
			return err
		}
		moved := false
		for wait := 0; wait < 10 && !moved; wait++ {
			deps.Sleep(300 * time.Millisecond)
			// A torn or partial frame is not evidence either way; read again.
			y, c, readErr := read()
			if readErr != nil {
				continue
			}
			yesRow, cursorRow = y, c
			moved = cursorRow != before
		}
		if !moved {
			return fmt.Errorf("cursor did not move toward %q", yes[0])
		}
	}
	// Two consecutive good frames must agree the cursor is on Yes.
	agreed := false
	for tries := 0; tries < 4 && !agreed; tries++ {
		deps.Sleep(300 * time.Millisecond)
		y, c, readErr := read()
		if readErr != nil {
			continue
		}
		agreed = c == y && cursorRow == yesRow && y == yesRow
		yesRow, cursorRow = y, c
	}
	if !agreed {
		return fmt.Errorf("cursor not settled on %q", yes[0])
	}
	if err := deps.SendKey("Enter"); err != nil {
		return err
	}
	// Wait for the dialog to close, so a late repaint of it is not taken
	// for a dialog still waiting and answered a second time.
	for wait := 0; wait < 30; wait++ {
		deps.Sleep(300 * time.Millisecond)
		if _, _, readErr := read(); readErr != nil {
			break
		}
	}
	return nil
}

func appendOnce(list []string, v string) []string {
	for _, have := range list {
		if have == v {
			return list
		}
	}
	return append(list, v)
}

var (
	settleMCPFailedCount = regexp.MustCompile(`(?i)\b(\d+)\s+MCP servers?\s+failed\b`)
	settleMCPFailedName  = regexp.MustCompile(`(?i)MCP server\s+["“']?([\w@:./-]+)["”']?\s+(?:failed|disconnected)`)
	settlePluginClosed   = regexp.MustCompile(`(plugin:[\w@.-]+(?::[\w@.-]+)?)[^\n]*?(CONNECTION_CLOSED|failed to connect|failed)`)
	settleNamedClosed    = regexp.MustCompile(`([\w@:.-]+)\s*\(?\s*CONNECTION_CLOSED`)
)

// checkPlugins reads the pane for MCP/plugin failures Claude reports and, for
// each expected channel plugin, confirms a poller runs under the new pane.
// Failures are named; an unnamed count is reported as "mcp(N)".
func checkPlugins(deps SettleDeps, opts SettleOptions) []string {
	var failures []string
	// Claude connects MCPs after the composer paints; give it a few polls.
	for i := 0; i < 6; i++ {
		raw, err := deps.Capture()
		if err == nil {
			failures = PaneMCPFailures(settleStatusRegion(tmux.StripANSI(raw)))
			if len(failures) > 0 {
				break
			}
		}
		deps.Sleep(opts.PollEvery)
	}
	if deps.Procs != nil && deps.PanePID != nil {
		for _, plugin := range opts.Plugins {
			if !pluginPollerRunning(deps, opts, plugin) {
				failures = appendOnce(failures, plugin+"(no process)")
			}
		}
	}
	sort.Strings(failures)
	return failures
}

// settleStatusRegion is where Claude reports MCP and plugin health: the
// composer and the footer under it, or the last lines when no composer is
// drawn. The conversation above is never read, so a reply that merely talks
// about a failed plugin is not a failure.
func settleStatusRegion(content string) string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if top, _ := send.ComposerBounds(lines); top >= 0 {
		return strings.Join(lines[top:], "\n")
	}
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	return strings.Join(lines, "\n")
}

// PaneMCPFailures extracts named MCP/plugin failures from status text.
func PaneMCPFailures(content string) []string {
	var out []string
	for _, m := range settlePluginClosed.FindAllStringSubmatch(content, -1) {
		out = appendOnce(out, m[1])
	}
	for _, m := range settleMCPFailedName.FindAllStringSubmatch(content, -1) {
		out = appendOnce(out, m[1])
	}
	if len(out) == 0 {
		for _, m := range settleNamedClosed.FindAllStringSubmatch(content, -1) {
			out = appendOnce(out, m[1])
		}
	}
	if len(out) == 0 {
		if m := settleMCPFailedCount.FindStringSubmatch(content); m != nil {
			out = append(out, "mcp("+m[1]+")")
		}
	}
	return out
}

// pluginPollerRunning waits briefly for a process whose command names plugin
// to appear under the new pane.
func pluginPollerRunning(deps SettleDeps, opts SettleOptions, plugin string) bool {
	for i := 0; i < 20; i++ {
		if pid, err := deps.PanePID(); err == nil {
			cmds, cerr := deps.Procs.Commands()
			if cerr == nil {
				for _, child := range settleDescendants(deps.Procs, pid) {
					if strings.Contains(strings.ToLower(strings.Join(cmds[child].Args, " ")), strings.ToLower(plugin)) {
						return true
					}
				}
			}
		}
		deps.Sleep(opts.PollEvery)
	}
	return false
}

// waitOldProcessesGone waits for the replaced pane's processes and for any
// orphan still carrying this instance's id (a plugin poller reparented to
// init when its Claude died) to exit. Liveness is matched by pid and the
// AGENTDECK_INSTANCE_ID in its environment, never by name. It returns what
// is still alive at the deadline; settle reports those, it does not kill.
func waitOldProcessesGone(deps SettleDeps, opts SettleOptions) []int {
	if deps.Procs == nil {
		return nil
	}
	deadline := deps.Now().Add(opts.PollerWait)
	for {
		alive := oldInstanceProcesses(deps, opts)
		if len(alive) == 0 || !deps.Now().Before(deadline) {
			return alive
		}
		deps.Sleep(opts.PollEvery)
	}
}

func oldInstanceProcesses(deps SettleDeps, opts SettleOptions) []int {
	cmds, err := deps.Procs.Commands()
	if err != nil {
		return nil
	}
	current := map[int]bool{}
	var roots []int
	if deps.PanePID != nil {
		if pid, perr := deps.PanePID(); perr == nil && pid > 0 {
			roots = append(roots, pid)
		}
	}
	if deps.SessionPanePIDs != nil {
		if pids, perr := deps.SessionPanePIDs(); perr == nil {
			roots = append(roots, pids...)
		}
	}
	for _, root := range roots {
		current[root] = true
		for _, child := range settleDescendants(deps.Procs, root) {
			current[child] = true
		}
	}
	// This settle run and its ancestors (a detached self-switch job) carry
	// the id too and are never "old".
	mine := map[int]bool{}
	for pid, hops := os.Getpid(), 0; pid > 1 && hops < 64; hops++ {
		mine[pid] = true
		pid = cmds[pid].PPID
	}
	var alive []int
	seen := map[int]bool{}
	for _, pid := range opts.OldPIDs {
		if _, ok := cmds[pid]; ok && !current[pid] && !seen[pid] {
			seen[pid] = true
			alive = append(alive, pid)
		}
	}
	if opts.InstanceID != "" {
		for pid, cmd := range cmds {
			// Not only ppid 1: under a subreaper (systemd --user, a
			// supervisor) an orphan is reparented there instead.
			if current[pid] || seen[pid] || mine[pid] || !settlePollerCommand(cmd.Args) {
				continue
			}
			if deps.Procs.Env(pid, "AGENTDECK_INSTANCE_ID") == opts.InstanceID {
				seen[pid] = true
				alive = append(alive, pid)
			}
		}
	}
	sort.Ints(alive)
	return alive
}

// settlePollerCommand reports whether argv looks like a channel plugin or
// MCP server: the processes that hold a token or a socket. Other orphans
// carrying the id (a detached helper such as this settle run) are not
// pollers and are left out.
func settlePollerCommand(args []string) bool {
	joined := strings.ToLower(strings.Join(args, " "))
	for _, marker := range []string{"telegram", "discord", "slack", "mcp-server", "mcp_server", "server-mcp", "/mcp", "bun "} {
		if strings.Contains(joined, marker) {
			return true
		}
	}
	return false
}

// verifyAccount checks the live process, not the record. The Claude
// processes under the pane that carry this instance's id (not the shell
// wrapper) must all run on the expected account: CLAUDE_CONFIG_DIR is the
// account dir itself, or a worker-scratch mirror whose source marker names
// it. Auth status names the identity when available.
func verifyAccount(deps SettleDeps, opts SettleOptions, report *SettleReport) {
	checked, agreed := 0, 0
	if deps.Procs != nil && deps.PanePID != nil {
		if pid, err := deps.PanePID(); err == nil {
			cmds, _ := deps.Procs.Commands()
			for _, child := range append([]int{pid}, settleDescendants(deps.Procs, pid)...) {
				if deps.Procs.Env(child, "AGENTDECK_INSTANCE_ID") != opts.InstanceID || !settleClaudeCommand(cmds[child].Args) {
					continue
				}
				// Only the session's own Claude: one started under it (a
				// worker on another config dir) is not the session.
				if settleUnderClaude(cmds, child, pid) {
					continue
				}
				checked++
				dir := deps.Procs.Env(child, "CLAUDE_CONFIG_DIR")
				report.LiveConfigDir = dir
				if dir == "" {
					report.LiveConfigDir = "(unset)"
				}
				if settleRunsOnAccount(dir, opts.ConfigDir) {
					agreed++
				}
			}
		}
	}
	report.AccountOK = checked > 0 && agreed == checked
	if !report.AccountOK {
		if checked == 0 {
			report.Problems = append(report.Problems, "no Claude process with this session's id under the pane")
		} else {
			report.Problems = append(report.Problems, fmt.Sprintf("live Claude process does not run on %s", opts.ConfigDir))
		}
	}
	if deps.AuthInfo != nil {
		if who, err := deps.AuthInfo(opts.ConfigDir); err == nil {
			report.AuthIdentity = who
		} else {
			report.AuthIdentity = "unknown"
			deps.Log("auth status: %v", err)
		}
	}
}

// settleClaudeCommand reports whether argv is a Claude process rather than
// the shell that launched it or a plugin under it.
func settleClaudeCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch filepath.Base(args[0]) {
	case "sh", "bash", "zsh", "dash", "fish", "ksh", "tcsh", "csh", "-bash", "-zsh", "-sh", "bun":
		return false
	}
	joined := strings.ToLower(strings.Join(args, " "))
	return strings.Contains(joined, "claude") && !settlePollerCommand(args)
}

// settleUnderClaude reports whether pid has a Claude process among its
// ancestors below the pane root.
func settleUnderClaude(cmds map[int]procowner.ProcCommand, pid, root int) bool {
	for p, hops := cmds[pid].PPID, 0; p > 1 && p != root && hops < 64; hops++ {
		if settleClaudeCommand(cmds[p].Args) {
			return true
		}
		p = cmds[p].PPID
	}
	return false
}

// settleRunsOnAccount reports whether a live CLAUDE_CONFIG_DIR is the
// account dir: the same dir, the default (unset) for the default account, or
// a worker-scratch generation whose source marker names the account dir.
func settleRunsOnAccount(live, account string) bool {
	if live == "" {
		home, err := os.UserHomeDir()
		return err == nil && (account == "" || sameConfigDir(account, filepath.Join(home, ".claude")))
	}
	if sameConfigDir(live, account) {
		return true
	}
	if data, err := os.ReadFile(filepath.Join(ExpandPath(live), scratchSourceMarker)); err == nil {
		return sameConfigDir(strings.TrimSpace(string(data)), account)
	}
	return false
}

func sameConfigDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = filepath.Clean(ExpandPath(a)), filepath.Clean(ExpandPath(b))
	if a == b {
		return true
	}
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	return aerr == nil && berr == nil && os.SameFile(ai, bi)
}

// SessionRunsBypass reports whether the session starts Claude with
// --dangerously-skip-permissions: its own options, else the config default.
// Only then may settle acknowledge the bypass warning.
func SessionRunsBypass(inst *Instance, cfg *UserConfig) bool {
	if inst == nil {
		return false
	}
	if strings.Contains(inst.Command, "--dangerously-skip-permissions") {
		return true
	}
	if opts := inst.GetClaudeOptions(); opts != nil {
		return opts.SkipPermissions
	}
	return cfg != nil && cfg.Claude.GetDangerousMode()
}
