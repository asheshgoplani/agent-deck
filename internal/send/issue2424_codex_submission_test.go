package send

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Issue #2424: `session send --no-wait` to a Codex lane reported the message
// as not submitted while the lane's transcript already showed it and Codex
// was working on it ("Working", "Working (3m 32s)", "Working (6s)"). Codex
// draws its own acknowledgement in the pane: the message leaves the composer
// and becomes a "›" cell in the transcript, and the live status row runs
// above the composer. Either is submission. A body still sitting in the
// composer is not (#1793), and neither is one parked in Codex's queued
// follow-up inputs.

// issue2424Message is a lane instruction in the 480 to 1300 character range
// the reporter saw.
const issue2424Message = "Stand down on the current task and switch to the retry lane. " +
	"Rebase fix/lane-a-retry on origin/main, rerun the failing integration suite with -count=1, " +
	"and report the three slowest tests with their timings and the exact command you ran. " +
	"Do not push anything and do not open a pull request until I confirm the numbers. " +
	"If the rebase conflicts in internal/session, stop and describe the conflict instead of resolving it. " +
	"Keep the existing worktree; do not create a new one. When you are done, reply with one short paragraph " +
	"that names the branch head, the suite result, and anything you skipped."

const codexFooter = "  gpt-6-sol · ~/work/lane-a · Context 70% left · Context 30% used · weekly 94% left · 258K window"

// renderCodexCell renders msg the way Codex 0.15x draws a user message, in the
// transcript and in its composer alike: "› " at column 0, wrapped rows
// indented two columns.
func renderCodexCell(msg string) []string {
	const width = 96
	var rows []string
	for i, line := range strings.Split(msg, "\n") {
		if line == "" {
			rows = append(rows, "")
			continue
		}
		for j := 0; j < len(line); j += width {
			end := min(j+width, len(line))
			prefix := "  "
			if i == 0 && j == 0 {
				prefix = "› "
			}
			rows = append(rows, prefix+line[j:end])
		}
	}
	return rows
}

func codexFrame(rows ...[]string) string {
	var all []string
	for _, r := range rows {
		all = append(all, r...)
	}
	return strings.Join(all, "\n") + "\n"
}

func lines(rows ...string) []string { return rows }

var (
	codexEarlierWork = lines(
		"• Ran go test ./internal/session/...",
		"  └ ok  	github.com/example/lane/internal/session	4.112s",
		"",
	)
	codexIdleComposer = lines(
		"",
		"› Ask Codex to do anything",
		"",
		codexFooter,
	)
)

func codexWorking(elapsed string) []string {
	return lines("• Working ("+elapsed+" • esc to interrupt)", "")
}

func TestIssue2424_CodexPaneEvidence(t *testing.T) {
	msg := issue2424Message
	cell := renderCodexCell(msg)
	idle := codexFrame(codexEarlierWork, lines("─ Worked for 2m 07s ─────────────────────────────"), codexIdleComposer)
	busy := codexFrame(codexEarlierWork, codexWorking("3m 30s"), codexIdleComposer)

	cases := []struct {
		name     string
		baseline string
		frame    string
		want     Outcome
	}{
		{
			// Occurrence 3: the lane was already working; the message went
			// into the running turn and shows in the transcript.
			name:     "message in transcript, Working (3m 32s)",
			baseline: busy,
			frame:    codexFrame(codexEarlierWork, cell, lines(""), codexWorking("3m 32s"), codexIdleComposer),
			want:     OutcomeConfirmed,
		},
		{
			// Occurrence 4: an idle lane that started its turn on this message.
			name:     "message in transcript, Working (6s)",
			baseline: idle,
			frame:    codexFrame(codexEarlierWork, cell, lines(""), codexWorking("6s"), codexIdleComposer),
			want:     OutcomeConfirmed,
		},
		{
			// Codex 0.155 with reduced motion hides the spinner bullet.
			name:     "message in transcript, reduced-motion Working",
			baseline: idle,
			frame:    codexFrame(codexEarlierWork, cell, lines("", "Working (2s • esc to interrupt)", ""), codexIdleComposer),
			want:     OutcomeConfirmed,
		},
		{
			// The live status row alone, with the body out of the composer:
			// Codex is running the turn and the composer is not holding it.
			name:     "Working with the body visible outside the composer",
			baseline: busy,
			frame:    codexFrame(codexEarlierWork, lines("• Context: "+msg, ""), codexWorking("3m 33s"), codexIdleComposer),
			want:     OutcomeConfirmed,
		},
		{
			// A fast turn that already finished: the message cell sits in
			// the transcript above Codex's reply and an empty composer.
			name:     "message in transcript, turn already over",
			baseline: idle,
			frame:    codexFrame(codexEarlierWork, cell, lines("", "• Done: fix/lane-a-retry is at 4f2c1e0, suite green.", "", "─ Worked for 3s ─────────────────────────────"), codexIdleComposer),
			want:     OutcomeConfirmed,
		},
		{
			// #1793 true negative: Enter swallowed, the body sits in the
			// composer of an idle lane.
			name:     "#1793 body still in the composer, idle",
			baseline: idle,
			frame:    codexFrame(codexEarlierWork, lines("─ Worked for 2m 07s ─────────────────────────────", ""), cell, lines("", codexFooter)),
			want:     OutcomeFailed,
		},
		{
			// The same on a lane that is already working: its status row is
			// the old turn, not this message.
			name:     "body still in the composer while Working",
			baseline: busy,
			frame:    codexFrame(codexEarlierWork, codexWorking("3m 32s"), lines(""), cell, lines("", codexFooter)),
			want:     OutcomeFailed,
		},
		{
			// An earlier identical message in the transcript is not this
			// send: the new copy is stuck in the composer.
			name:     "repeated message, earlier copy in transcript, new copy in composer",
			baseline: codexFrame(cell, lines("", "• Done.", ""), codexIdleComposer),
			frame:    codexFrame(cell, lines("", "• Done.", "", ""), cell, lines("", codexFooter)),
			want:     OutcomeFailed,
		},
		{
			// Parked in Codex's queued follow-up inputs: Codex has not taken
			// it into a turn yet, so it is not submitted.
			name:     "body only in queued follow-up inputs",
			baseline: busy,
			frame: codexFrame(codexEarlierWork, codexWorking("3m 32s"), lines(
				"• Queued follow-up inputs",
				"  ↳ "+msg[:120],
				"    "+msg[120:],
				"    shift + ← edit last queued message",
			), codexIdleComposer),
			want: OutcomeDeliveredUnconfirmed,
		},
		{
			// A picker row drawn under the composer ("›" selection) must not
			// turn the composer holding the body into a transcript cell.
			name:     "composer holding the body with a picker row below it",
			baseline: idle,
			frame:    codexFrame(codexEarlierWork, cell, lines("› internal/session/instance.go", "", codexFooter)),
			want:     OutcomeDeliveredUnconfirmed,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := NewObserver("codex", msg, Captured(c.baseline))
			o.CodexLike = true
			v := replay(t, o, Captured(c.frame))
			if v.Outcome != c.want {
				t.Fatalf("outcome = %q (%s), want %q", v.Outcome, v.Message, c.want)
			}
		})
	}
}

// The evidence is Codex's, not a general rule: a Claude observer reading the
// same frame keeps its own verdict (Claude lanes unchanged).
func TestIssue2424_ClaudeObserverIgnoresCodexEvidence(t *testing.T) {
	msg := issue2424Message
	busy := codexFrame(codexEarlierWork, codexWorking("3m 30s"), codexIdleComposer)
	frame := codexFrame(codexEarlierWork, renderCodexCell(msg), lines(""), codexWorking("3m 32s"), codexIdleComposer)
	o := NewObserver("claude", msg, Captured(busy))
	o.ClaudeLike = true
	o.BusyBeforeSend = true
	if v := replay(t, o, Captured(frame)); v.Outcome == OutcomeConfirmed {
		t.Fatalf("a Claude observer must not confirm on Codex's pane evidence: %q", v.Outcome)
	}
}

// The cell reading holds on every recorded Codex frame that ends at its idle
// composer: the composer is the last cell, and the newest user message above
// it is followed by transcript rows (the shape the transcript-cell evidence
// relies on). Older messages can sit back to back (sent while a turn ran).
func TestIssue2424_CodexCellsMatchCorpus(t *testing.T) {
	dir := filepath.Join("..", "tmux", "testdata", "status_corpus")
	labels, err := os.Open(filepath.Join(dir, "labels.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer labels.Close()
	scanner := bufio.NewScanner(labels)
	checked := 0
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), "\t", 4)
		if len(fields) < 3 || fields[1] != "codex" {
			continue
		}
		frame, err := os.ReadFile(filepath.Join(dir, fields[0]+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		cells := codexCells(tmux.StripANSI(string(frame)))
		if len(cells) == 0 || !strings.HasPrefix(cells[len(cells)-1].text, "AskCodex") {
			continue
		}
		checked++
		if len(cells) > 1 && !cells[len(cells)-2].followedByTranscript {
			t.Errorf("%s: newest user message (%.40q) not followed by transcript rows", fields[0], cells[len(cells)-2].text)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if checked < 15 {
		t.Fatalf("only %d Codex frames ending at the idle composer", checked)
	}
}
