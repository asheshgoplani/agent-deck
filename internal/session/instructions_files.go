// Per-session instruction files (issue #3206, requested by dpalic).
//
// A session can carry a list of operator-written instruction files (a role,
// a team rule set). They are not a new delivery path: at every spawn (start,
// restart, resume, fork) each file is read and appended as its own section to
// the identity block agent-deck already renders and hands to the harness
// (identity_injection.go, documentation/HARNESS_IDENTITY.md), so every
// harness gets them exactly the way it gets that block, and editing a file
// takes effect on the next start or restart.
//
// Safety rules:
//   - paths are absolute (~ and $VAR expanded when set); a comma separates
//     list entries, so a path cannot contain one;
//   - only regular, UTF-8 text files are read; anything else is reported as
//     unreadable and named in the block instead of its content;
//   - control characters other than newline and tab are dropped, so a file
//     cannot add terminal sequences or break the launch command;
//   - a per-file cap (instructionsFileMaxBytes); where the block rides inline
//     in the launch command with a budget (codex's -c override, SSH, sandbox)
//     a file that does not fit is replaced, in order, by a one-line pointer;
//   - context level none and --no-identity suppress the section with the
//     rest of the block.
//
// A session without instruction files renders, writes and launches exactly
// what it did before this feature (identity_golden_test.go pins that).
package session

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"
)

const (
	// instructionsFileMaxBytes caps one instruction file. A larger file is
	// not read into the block; a pointer line names it instead.
	instructionsFileMaxBytes = 32 << 10
	// maxInstructionsFiles caps the list length per session.
	maxInstructionsFiles = 16
	// toolDataInstructionsFilesKey is the tool_data extras key the list is
	// persisted under (see instructions_files_persist.go).
	toolDataInstructionsFilesKey = "instructions_files"
)

// Instruction file states reported by `session show --json` and
// `session primer`.
const (
	InstructionsStateOK           = "ok"            // content is in the block
	InstructionsStateUnreadable   = "unreadable"    // missing, not a regular file, not UTF-8 text, ...
	InstructionsStateNotDelivered = "not_delivered" // readable, but only a pointer line (or nothing) reaches the harness
)

// InstructionsFileStatus is what became of one instruction file on a spawn.
type InstructionsFileStatus struct {
	Path   string `json:"path"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	Bytes  int    `json:"bytes,omitempty"`
}

// NormalizeInstructionsFiles turns user input into the stored list: each
// value may itself be a comma-separated list; entries are trimmed, ~ and $VAR
// are expanded, and the result must be an absolute path without control
// characters. Empty entries are skipped and duplicates dropped (first wins),
// so `session set <id> instructions-file ""` clears the list.
func NormalizeInstructionsFiles(values []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, v := range values {
		for _, raw := range strings.Split(v, ",") {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			// An unset variable would silently expand to "" and turn
			// "$TEAM/rules.md" into "/rules.md"; refuse it instead.
			var unset string
			os.Expand(raw, func(name string) string {
				if _, ok := os.LookupEnv(name); !ok && unset == "" {
					unset = name
				}
				return ""
			})
			if unset != "" {
				return nil, fmt.Errorf("instructions file %q uses $%s, which is not set", raw, unset)
			}
			p := ExpandPath(raw)
			if strings.ContainsFunc(p, isInstructionsControlRune) {
				return nil, fmt.Errorf("instructions file %q contains control characters", raw)
			}
			if !filepath.IsAbs(p) {
				return nil, fmt.Errorf("instructions file %q is not an absolute path (after ~ and $VAR expansion: %q)", raw, p)
			}
			p = filepath.Clean(p)
			if seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) > maxInstructionsFiles {
		return nil, fmt.Errorf("too many instructions files: %d (at most %d)", len(out), maxInstructionsFiles)
	}
	return out, nil
}

// isInstructionsControlRune reports the runes dropped from file content
// (newline and tab are handled by the caller) and refused in paths: C0
// controls, DEL and the C1 range.
func isInstructionsControlRune(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// readInstructionsFile reads one instruction file under the safety rules and
// returns its sanitized text, or a state and reason when it cannot be used.
//
// The path is stat'ed before it is opened, and a non-regular file (a FIFO,
// a device, a directory, a symlink to one of them) is refused without an
// open: an O_RDONLY open of a FIFO blocks until a writer appears, which hung
// every spawn and `session show`/`session primer` (#3215 review R-1). The
// open itself is O_NONBLOCK and the opened file is checked again, so a path
// swapped for a FIFO between the stat and the open cannot block either.
func readInstructionsFile(path string) (text string, size int, state, reason string) {
	st, err := os.Stat(path)
	if err != nil {
		return "", 0, InstructionsStateUnreadable, instructionsOpenReason(err)
	}
	if !st.Mode().IsRegular() {
		return "", 0, InstructionsStateUnreadable, "not a regular file"
	}
	if st.Size() > instructionsFileMaxBytes {
		return "", int(st.Size()), InstructionsStateNotDelivered, fmt.Sprintf("larger than the %d KB per-file cap", instructionsFileMaxBytes>>10)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) // #nosec G304 -- operator-configured absolute path, read only
	if err != nil {
		return "", 0, InstructionsStateUnreadable, instructionsOpenReason(err)
	}
	defer f.Close()
	if st, err = f.Stat(); err != nil {
		return "", 0, InstructionsStateUnreadable, "cannot stat the file"
	}
	if !st.Mode().IsRegular() {
		return "", 0, InstructionsStateUnreadable, "not a regular file"
	}
	data, err := io.ReadAll(io.LimitReader(f, instructionsFileMaxBytes+1))
	if err != nil {
		return "", 0, InstructionsStateUnreadable, "read failed"
	}
	if len(data) > instructionsFileMaxBytes {
		return "", len(data), InstructionsStateNotDelivered, fmt.Sprintf("larger than the %d KB per-file cap", instructionsFileMaxBytes>>10)
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return "", len(data), InstructionsStateUnreadable, "not UTF-8 text"
	}
	return sanitizeInstructionsText(string(data)), len(data), InstructionsStateOK, ""
}

func instructionsOpenReason(err error) string {
	switch {
	case os.IsNotExist(err):
		return "file not found"
	case os.IsPermission(err):
		return "permission denied"
	default:
		return "cannot open the file"
	}
}

// sanitizeInstructionsText drops a leading BOM and every control character
// other than newline and tab (a CR of a CRLF file included), and ends the
// text with exactly one newline.
func sanitizeInstructionsText(s string) string {
	s = strings.TrimPrefix(s, "\uFEFF")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if isInstructionsControlRune(r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimRight(s, "\n") + "\n"
}

// identityBudget names how a spawn carries the identity block, which decides
// whether instruction files are subject to an inline size budget.
type identityBudget int

const (
	// budgetFile: the harness reads the block from a file on this host
	// (claude, pi, omp, gemini, opencode, copilot, env-only tools).
	budgetFile identityBudget = iota
	// budgetCodexLocal: local codex gets the block inline as -c
	// developer_instructions in the pane command.
	budgetCodexLocal
	// budgetRemote: SSH/sandboxed spawn, block decoded from the launch
	// command (identityRemoteFileExport).
	budgetRemote
	// budgetRemoteCodex: budgetRemote plus codex's inline -c override, which
	// carries the block a second time.
	budgetRemoteCodex
)

// fits reports whether a block of this text stays inside the inline budget.
// existing is the developer_instructions codex already has configured: the
// codex override carries existing + "\n\n" + text (codexIdentityOverride),
// so that is what is measured (#3215 review R-2).
func (b identityBudget) fits(text, existing string) bool {
	override := func() int {
		merged := text
		if existing != "" {
			merged = existing + "\n\n" + text
		}
		return len("developer_instructions=") + len(identityTOMLString(merged))
	}
	switch b {
	case budgetCodexLocal:
		return override() <= identityInlineMaxBytes
	case budgetRemote:
		return len(identityPrintfArg(text)) <= identityInlineMaxBytes
	case budgetRemoteCodex:
		return len(identityPrintfArg(text))+override() <= identityInlineMaxBytes
	}
	return true
}

func (b identityBudget) overBudgetReason() string {
	if b == budgetCodexLocal {
		return fmt.Sprintf("over the %d KB inline budget of codex's developer_instructions", identityInlineMaxBytes>>10)
	}
	return fmt.Sprintf("over the %d KB inline budget of an SSH/sandboxed launch", identityInlineMaxBytes>>10)
}

// codexBudgetExisting is the developer_instructions this session's codex
// override is merged with (see codexIdentityOverride), for the budget: the
// configured value of a local or sandboxed codex, "" for any other tool and
// for SSH, whose remote config is not readable here and is not merged.
func (i *Instance) codexBudgetExisting() string {
	if i == nil || !IsCodexCompatible(i.Tool) || i.IsSSH() {
		return ""
	}
	if existing, found := codexEffectiveDeveloperInstructions(i.getCodexHomeDir(), i.EffectiveWorkingDir()); found {
		return existing
	}
	return ""
}

// spawnBudget is the budget the session's own harness spawn uses.
func (i *Instance) spawnBudget() identityBudget {
	codex := i != nil && IsCodexCompatible(i.Tool)
	switch {
	case i.identityRunsElsewhere() && codex:
		return budgetRemoteCodex
	case i.identityRunsElsewhere():
		return budgetRemote
	case codex:
		return budgetCodexLocal
	}
	return budgetFile
}

// instructionsSectionHeader opens the section appended to the identity block.
func instructionsSectionHeader(id string) string {
	return "\n## Session instructions\n" +
		"Standing instructions for this session from your operator, read from these files at every start/restart (`agent-deck session set " + id + " instructions-file` changes the list):\n"
}

// instructionsPointer is the one line that stands in for a file whose
// content is not in the block.
func instructionsPointer(reason string, remote bool) string {
	where := "read the file itself"
	if remote {
		where = "the file is on the agent-deck host, not where this session runs"
	}
	return fmt.Sprintf("(not included: %s; %s)\n", reason, where)
}

// instructionsSummary stands in for the whole section when not even one
// pointer line per file fits: one line naming every file that is not
// included, so none is dropped silently (#3215 review R-3).
func instructionsSummary(id string, paths []string, reason string, remote bool) string {
	where := "read the files themselves"
	if remote {
		where = "they are on the agent-deck host, not where this session runs"
	}
	return fmt.Sprintf("\n## Session instructions\n(not included: %d instruction file(s), %s; %s; `agent-deck session primer %s` lists them: %s)\n",
		len(paths), reason, where, id, strings.Join(paths, ", "))
}

// contextBlock renders the block a spawn carries at the given resolved level:
// the identity/primer block followed, when the session has instruction files,
// by their section. Without instruction files it is exactly
// BuildContextPromptForLevel(level). existing is the configured codex
// developer_instructions the codex override is merged with ("" otherwise).
// Writes nothing.
//
// Where the budget is tight it degrades in steps, never silently: files in
// full where they fit and a pointer line for each other file; else one
// summary line naming them all; else (the base block, plus a configured
// developer_instructions, already fill the budget) the base block alone, as
// without the flag, and every file reported not_delivered with the reason.
func (i *Instance) contextBlock(level string, budget identityBudget, existing string) (string, []InstructionsFileStatus) {
	base := i.BuildContextPromptForLevel(level)
	if i == nil || len(i.InstructionsFiles) == 0 {
		return base, nil
	}
	remote := budget == budgetRemote || budget == budgetRemoteCodex
	type entry struct {
		heading, body string
		status        InstructionsFileStatus
	}
	entries := make([]entry, 0, len(i.InstructionsFiles))
	for _, p := range i.InstructionsFiles {
		text, size, state, reason := readInstructionsFile(p)
		e := entry{heading: "\n### " + identityField(p) + "\n"}
		e.status = InstructionsFileStatus{Path: p, State: state, Reason: reason, Bytes: size}
		if state == InstructionsStateOK {
			e.body = text
		} else {
			e.body = instructionsPointer(reason, remote)
		}
		entries = append(entries, e)
	}
	assemble := func(n int) string {
		// Entries [0,n) as decided, entry n in full, each later file at the
		// smaller of its text and its pointer (it needs room at least for that).
		var b strings.Builder
		b.WriteString(base)
		b.WriteString(instructionsSectionHeader(identityField(i.ID)))
		for k, e := range entries {
			b.WriteString(e.heading)
			if k <= n {
				b.WriteString(e.body)
			} else if e.status.State == InstructionsStateOK {
				// Reserve the smaller of the file and its pointer: a short
				// file must not push an earlier one out (CodeRabbit, #3215).
				if p := instructionsPointer(budget.overBudgetReason(), remote); len(p) < len(e.body) {
					b.WriteString(p)
				} else {
					b.WriteString(e.body)
				}
			} else {
				b.WriteString(e.body)
			}
		}
		return b.String()
	}
	// Decide in order: each readable file goes in full when it fits together
	// with everything kept so far and a pointer for every later file.
	for k := range entries {
		if entries[k].status.State != InstructionsStateOK {
			continue
		}
		if !budget.fits(assemble(k), existing) {
			entries[k].body = instructionsPointer(budget.overBudgetReason(), remote)
			entries[k].status.State = InstructionsStateNotDelivered
			entries[k].status.Reason = budget.overBudgetReason()
		}
	}
	text := assemble(len(entries))
	statuses := make([]InstructionsFileStatus, len(entries))
	for k, e := range entries {
		statuses[k] = e.status
	}
	if budget.fits(text, existing) {
		return text, statuses
	}
	// Not even a pointer per file fits: every readable file is out of the
	// budget; unreadable and over-cap files keep their own state.
	reason := budget.overBudgetReason()
	if existing != "" && !budget.fits(base, existing) {
		reason = fmt.Sprintf("no room in codex's %d KB inline budget next to the configured developer_instructions", identityInlineMaxBytes>>10)
	}
	var paths []string
	for k := range statuses {
		if statuses[k].State == InstructionsStateOK || statuses[k].State == InstructionsStateNotDelivered && statuses[k].Reason == budget.overBudgetReason() {
			statuses[k].State = InstructionsStateNotDelivered
			statuses[k].Reason = reason
		}
		paths = append(paths, identityField(statuses[k].Path))
	}
	if summary := base + instructionsSummary(identityField(i.ID), paths, reason, remote); budget.fits(summary, existing) {
		return summary, statuses
	}
	// The base block (with a configured developer_instructions) already
	// fills the budget: leave it exactly as without instruction files, so a
	// session that started before the flag still starts.
	return base, statuses
}

// InstructionsFilesReport resolves what the session's next spawn delivers:
// the exact text its harness gets (empty when the context level is none) and
// the state of each instruction file. For `session show --json` and
// `session primer`. Reads the files, writes nothing.
func (i *Instance) InstructionsFilesReport() (text string, files []InstructionsFileStatus) {
	if i == nil {
		return "", nil
	}
	level, _ := i.EffectiveContextLevel()
	if level == ContextLevelNone {
		files = make([]InstructionsFileStatus, 0, len(i.InstructionsFiles))
		for _, p := range i.InstructionsFiles {
			files = append(files, InstructionsFileStatus{Path: p, State: InstructionsStateNotDelivered, Reason: "context level none (--no-identity or context-level none)"})
		}
		return "", files
	}
	return i.contextBlock(level, i.spawnBudget(), i.codexBudgetExisting())
}
