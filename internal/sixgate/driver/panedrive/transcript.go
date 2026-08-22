package panedrive

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// writeTranscript persists transcript.md: the artifact a human reads.
//
// It is the whole point of G1. "Done" stops being a builder's claim exactly
// when there is a document showing the software being used, step by step, with
// the real output pasted in — so the frames are reproduced in full rather than
// linked to. For this driver the transcript carries one section the in-process
// driver's cannot: the safety ledger. A run that drove a real tmux server has
// to be readable as proof it gave the server back, by somebody who does not
// trust it.
func writeTranscript(opts Options, run *Run) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# G1 DRIVE (Driver B, real pane) — %s — row `%s`\n\n", run.Slug, run.Fixture)
	fmt.Fprintf(&b, "> %s\n\n", opts.Script.Sentence)
	b.WriteString("This is a recording of the shipped binary being used, not a claim that it works.\n")
	b.WriteString("Every frame below came out of `capture-pane`: it is the literal text a real\n")
	b.WriteString("terminal was painting when the keystroke named above it had been pressed.\n\n")

	fmt.Fprintf(&b, "- **Result:** %s\n", passWord(run.Pass))
	fmt.Fprintf(&b, "- **Driver:** `%s` — %s\n", run.Driver, run.DriverKind)
	fmt.Fprintf(&b, "- **Artifact driven:** %s\n", run.Artifact)
	if run.BinaryInfo != "" {
		fmt.Fprintf(&b, "- **Build info:** %s\n", run.BinaryInfo)
	}
	fmt.Fprintf(&b, "- **Commit:** `%s`%s\n", run.GitSHA, dirtySuffix(run.GitDirty))
	fmt.Fprintf(&b, "- **Terminal:** %dx%d (%s)\n", run.TermWidth, run.TermHeight, orUnknown(run.TmuxVersion))
	fmt.Fprintf(&b, "- **World:** %s — %s\n", run.Fixture, run.FixtureNote)
	for _, n := range run.Notes {
		fmt.Fprintf(&b, "  - %s\n", n)
	}
	for _, q := range run.FirstRunQuestions {
		fmt.Fprintf(&b, "- **Asked before the deck appeared:** `%s` — answered with `%s`, offered on screen as %q (frame `%s`)\n",
			q.Label, q.Key, q.Hint, q.Frame)
	}
	fmt.Fprintf(&b, "- **Duration:** %d ms\n\n", run.DurationMS)

	writeSafetyLedger(&b, run)

	if run.StoppedAfter != "" {
		b.WriteString("## Why this row stops early\n\n")
		fmt.Fprintf(&b, "The journey was run through `%s` and no further.\n\n", run.StoppedAfter)
		fmt.Fprintf(&b, "> %s\n\n", run.StopReason)
		b.WriteString("The steps it did run were asserted in full. The steps it did not are listed\n")
		b.WriteString("below as skipped, and are **not** counted as passing anywhere: a drive that\n")
		b.WriteString("stops early leaves missing frames, and a missing frame is a G2 failure.\n\n")
	}

	if len(run.Failures) > 0 {
		b.WriteString("## Why this drive failed\n\n")
		for _, f := range run.Failures {
			fmt.Fprintf(&b, "- %s\n", f)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Steps\n\n")
	b.WriteString("| step | did | frame | settled |\n|------|-----|-------|---------|\n")
	for _, s := range run.Steps {
		frame := "—"
		if s.ScreenFile != "" {
			frame = "`" + s.ScreenFile + "`"
		}
		settled := "yes"
		switch {
		case s.Skipped != "":
			settled = "_skipped_"
		case !s.Settled:
			settled = "**no**"
		}
		fmt.Fprintf(&b, "| `%s` | %s %s | %s | %s (%d ms) |\n",
			s.ID, s.Verb, mdCode(s.Detail), frame, settled, s.SettleMS)
	}
	b.WriteString("\n")

	writePreambleFrames(&b, opts.OutDir, run)

	for _, s := range run.Steps {
		fmt.Fprintf(&b, "### %s — `%s %s`\n\n", s.ID, s.Verb, s.Detail)
		if s.Note != "" {
			fmt.Fprintf(&b, "*%s*\n\n", s.Note)
		}
		if s.Skipped != "" {
			b.WriteString("_not run by this row._\n\n")
			continue
		}
		if s.Error != "" {
			fmt.Fprintf(&b, "**step error:** %s\n\n", s.Error)
		}
		if s.ScreenFile == "" {
			b.WriteString("_no capture: a navigation beat_\n\n")
			continue
		}
		raw, err := os.ReadFile(filepath.Join(opts.OutDir, s.ScreenFile)) //nolint:gosec // artifact just written
		if err != nil {
			fmt.Fprintf(&b, "_frame unreadable: %v_\n\n", err)
			continue
		}
		fmt.Fprintf(&b, "What appeared (`%s`, %d lines):\n\n```\n%s```\n\n",
			s.ScreenFile, s.FrameLines, string(raw))
	}

	return os.WriteFile(filepath.Join(opts.OutDir, "transcript.md"), []byte(b.String()), 0o644) //nolint:gosec // committed artifact
}

// writeSafetyLedger renders the part of the artifact a sceptic reads first.
//
// This machine lost its whole session fleet twice to harnesses that spawned
// tmux, so a Driver B transcript that only showed screens would be missing the
// fact most worth checking. Every number here is observed after the fact, not
// promised before it.
func writeSafetyLedger(b *strings.Builder, run *Run) {
	t := run.Teardown
	b.WriteString("## Safety ledger — tmux and pseudo-terminals\n\n")
	b.WriteString("| fact | value |\n|------|-------|\n")
	fmt.Fprintf(b, "| dedicated socket | `%s` |\n", run.Isolation.SocketName)
	fmt.Fprintf(b, "| resolved socket path | `%s` |\n", run.Isolation.SocketPath)
	fmt.Fprintf(b, "| isolated `TMUX_TMPDIR` | `%s` |\n", run.Isolation.TmuxTmpdir)
	fmt.Fprintf(b, "| servers this run created | %d |\n", run.TmuxServersSpawned)
	fmt.Fprintf(b, "| teardown kill addressed | `%s` |\n", orDash(t.KillAddress))
	fmt.Fprintf(b, "| teardown kill error | %s |\n", orNone(t.KillError))
	fmt.Fprintf(b, "| sockets swept | %s |\n", orNone(strings.Join(t.SweepKilled, ", ")))
	fmt.Fprintf(b, "| postcondition | `%s` |\n", t.PostconditionCmd)
	fmt.Fprintf(b, "| postcondition held (the command failed) | %s |\n", yesNo(t.PostconditionFailed))
	fmt.Fprintf(b, "| tmux said | %s |\n", orNone(t.PostconditionOutput))
	fmt.Fprintf(b, "| servers STILL ANSWERING after the sweep | %s |\n", orNone(strings.Join(t.LiveSockets, ", ")))
	fmt.Fprintf(b, "| stale socket files (no server behind them) | %s |\n", orNone(strings.Join(t.StaleSocketFiles, ", ")))
	fmt.Fprintf(b, "| **teardown verified** | **%s** |\n", yesNo(t.Verified))
	fmt.Fprintf(b, "| ptys before / after / delta | %d / %d / %+d |\n", run.PTY.Before, run.PTY.After, run.PTY.Delta)
	b.WriteString("\nThe postcondition passes by FAILING: after teardown, asking this run's own\n")
	b.WriteString("socket to list its sessions must not succeed. A tmux server does not die when\n")
	b.WriteString("its socket is unlinked — it keeps its panes and a pty each, and becomes\n")
	b.WriteString("unreachable by every address in existence — so the check runs BEFORE anything\n")
	b.WriteString("is removed. Every socket left under the isolated directory is then asked,\n")
	b.WriteString("absolutely addressed, whether a server answers on it: a live one is a leak, a\n")
	b.WriteString("socket file with nothing behind it is a dying server's litter and holds no pty.\n\n")
	b.WriteString("Only pty GROWTH is this driver's fault; a shared host releases ptys on its own\n")
	b.WriteString("while a run is in progress, so a negative delta is ordinary.\n\n")

	if len(run.Isolation.Checks) > 0 {
		b.WriteString("### Refuse-to-start guards\n\n")
		b.WriteString("Checked before a single tmux command was built. Any failure aborts the run.\n\n")
		b.WriteString("| guard | held | note |\n|-------|------|------|\n")
		for _, c := range run.Isolation.Checks {
			fmt.Fprintf(b, "| %s | %s | %s |\n", c.Name, yesNo(c.OK), orDash(c.Note))
		}
		b.WriteString("\n")
	}
}

// writePreambleFrames reproduces the frames captured outside the script's
// steps — the first-run questions. They are the literal first thing a new user
// sees, which makes them the most valuable frames in a cold run and the ones a
// harness is most tempted to throw away.
func writePreambleFrames(b *strings.Builder, dir string, run *Run) {
	if len(run.FirstRunQuestions) == 0 {
		return
	}
	b.WriteString("## Before the journey — what a never-configured machine asked first\n\n")
	b.WriteString("Each question was captured, then answered with the key THE PRODUCT printed as\n")
	b.WriteString("the way to decline. The driver does not choose that key: it looks for the hint\n")
	b.WriteString("on screen and refuses to press anything if the hint is absent. Declining rather\n")
	b.WriteString("than accepting is the only honest choice for a gate — accepting would write\n")
	b.WriteString("into the config directory and make the next run a different experiment.\n\n")
	for _, q := range run.FirstRunQuestions {
		raw, err := os.ReadFile(filepath.Join(dir, q.Frame)) //nolint:gosec // artifact just written
		if err != nil {
			continue
		}
		fmt.Fprintf(b, "### `%s` — answered `%s`, because the screen says %q\n\n```\n%s```\n\n",
			q.Label, q.Key, q.Hint, string(raw))
	}
}

func mdCode(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return "`" + strings.ReplaceAll(s, "|", "\\|") + "`"
}

func passWord(ok bool) string {
	if ok {
		return "PASS — every step ran, every capture produced a frame, and teardown was verified"
	}
	return "FAIL"
}

func dirtySuffix(dirty bool) string {
	if dirty {
		return " (working tree dirty)"
	}
	return ""
}

func yesNo(ok bool) string {
	if ok {
		return "yes"
	}
	return "**NO**"
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "none"
	}
	return s
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "tmux version unknown"
	}
	return s
}
