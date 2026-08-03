// Package coldeye is SIXGATE's G5: a reviewer is given the built binary and one
// sentence, nothing else, and reports their literal first three minutes.
//
// WHY THE DEPRIVATION IS THE MECHANISM. Everyone who built the thing knows what
// the screen means, so nobody who built it can see it arriving. The failure this
// framework exists to catch — "the first thing I saw was a blank percentage I
// had to ask about" — is invisible from inside the design context and obvious
// from outside it for about three minutes, after which the reviewer has learned
// the software and stops being able to see it too. G5 spends that window
// deliberately: one person, once, with no source, no docs, no issue number and
// no design conversation.
//
// So this package's real product is an ABSENCE. [BuildWorld] creates a
// directory holding exactly two things — the binary and BRIEF.md — and asserts
// afterwards that it holds exactly two things. Anything else that landed there
// is a leak of context the reviewer was supposed not to have, and a review
// conducted with a leak is not a cold-eye review; it is a colleague being
// polite.
//
// THE PASS CRITERION IS FALSIFIABLE. Not "the reviewer liked it": the report
// exists, it self-reports whether the brief was contaminated, and every item
// the reviewer listed under "what looked broken" is either fixed or accepted
// with a written reason. A complaint that is neither is an unclosed finding and
// the gate stays shut.
package coldeye

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// BriefFile and ReportFile are the artifact filenames inside the G5 gate
// directory and inside the reviewer's world.
const (
	BriefFile          = "brief.md"
	ReportFile         = "report.md"
	ReportTemplateFile = "report.template.md"
	ResolutionsFile    = "resolutions.yaml"
	OutcomeJSONFile    = "outcome.json"
	OutcomeMDFile      = "outcome.md"
	// WorldBriefFile is what the brief is called inside the reviewer's
	// directory. It is upper-case because it is the only instruction they get.
	WorldBriefFile = "BRIEF.md"
	// BinaryName is what the binary is called in the reviewer's world. It is
	// deliberately the real command name: a reviewer told to run "./thing"
	// learns less than one who types what a user types.
	BinaryName = "agent-deck"
)

// World is the reviewer's whole universe.
type World struct {
	// RunID keys the directory and the tmux socket the brief sanctions.
	RunID string
	// Dir is /tmp/coldeye-<runID>.
	Dir string
	// Binary is the copied binary's path inside Dir.
	Binary string
	// Brief is BRIEF.md's path inside Dir.
	Brief string
	// Entries is what the directory holds afterwards, asserted to be exactly
	// the binary and the brief.
	Entries []string
	// Machine is the state the reviewer's computer was in, or nil for one that
	// has never run the program.
	Machine *Machine
}

// Machine is the state of the computer the reviewer runs the program on.
//
// It is deliberately NOT part of the reviewer's directory. A cold eye must be
// deprived of CONTEXT — source, docs, the design conversation, the name of the
// thing under review — but depriving them of DATA produces a different review
// entirely: an empty program tells you about its empty state and nothing about
// the screen anybody actually uses. So the reviewer is given a machine that
// already has sessions on it, exactly as a user's would, and is told nothing
// about what is on it or why.
//
// The environment is rendered into the brief as ordinary exports, so the
// reviewer can see every variable they are running under. Nothing is hidden
// from them; only the answers are.
type Machine struct {
	// Home is the sandboxed HOME the program will run against.
	Home string
	// Env is the full environment the brief tells the reviewer to export.
	Env map[string]string
	// Note is one line, recorded in the committed brief.md so a later reader
	// knows what state the reviewer met. It is NOT shown to the reviewer.
	Note string
}

// BuildWorld materializes the reviewer's directory.
//
// parent is normally "/tmp". sentence is the ONE sentence the reviewer is
// allowed to know; it must not name the feature under review, because "have a
// look at the context inspector" is already the answer to the first question a
// cold eye is supposed to ask. machine may be nil, which gives the reviewer a
// computer that has never run the program.
func BuildWorld(parent, runID, sentence, binarySrc string, machine *Machine) (*World, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, errors.New("coldeye: a run id is required")
	}
	if strings.TrimSpace(sentence) == "" {
		return nil, errors.New("coldeye: the one sentence is required; a reviewer given nothing at all cannot tell a bug from the point of the program")
	}
	dir := filepath.Join(parent, "coldeye-"+runID)
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("coldeye: %s already exists; a reused world may still hold the previous reviewer's notes", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	w := &World{
		RunID:  runID,
		Dir:    dir,
		Binary: filepath.Join(dir, BinaryName),
		Brief:  filepath.Join(dir, WorldBriefFile),
	}
	if err := copyExecutable(binarySrc, w.Binary); err != nil {
		return nil, err
	}
	w.Machine = machine
	if err := os.WriteFile(w.Brief, []byte(RenderBrief(runID, sentence, dir, machine)), 0o644); err != nil { //nolint:gosec // the reviewer must be able to read it
		return nil, err
	}

	// The assertion that makes the deprivation real rather than intended.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		w.Entries = append(w.Entries, e.Name())
	}
	if len(w.Entries) != 2 {
		return nil, fmt.Errorf(
			"coldeye: the reviewer's world must hold exactly the binary and %s, it holds %d entries (%s) — anything else is context they were meant not to have",
			WorldBriefFile, len(w.Entries), strings.Join(w.Entries, ", "))
	}
	return w, nil
}

// Denied lists, for the record, what the reviewer was NOT given. It is written
// into the committed brief.md so a later reader can see the deprivation was
// designed rather than accidental.
func Denied() []string {
	return []string{
		"the repository path, and any source file",
		"docs/, the G0 script, and every other gate artifact",
		"the issue number, the design conversation, and the name of the feature under review",
		"any statement of what the software is supposed to do beyond the one sentence",
		"any hint that a particular screen or number is the thing being reviewed",
	}
}

// RenderBrief produces BRIEF.md.
//
// Four things and no more: what the software is, how to run it without
// endangering the machine, what to send back, and the instruction to stop if
// they discover they have been told too much.
func RenderBrief(runID, sentence, dir string, machine *Machine) string {
	var b strings.Builder
	b.WriteString("# BRIEF\n\n")
	b.WriteString("You are reviewing a piece of software you have never seen. This directory holds\n")
	b.WriteString("everything you are given: the program, and this file.\n\n")

	b.WriteString("## What it is\n\n")
	fmt.Fprintf(&b, "%s\n\n", strings.TrimSpace(sentence))
	b.WriteString("That sentence is all anyone will tell you. Do not ask for more, and do not go\n")
	b.WriteString("looking: the value of this review is entirely in the fact that you do not know\n")
	b.WriteString("what is supposed to happen.\n\n")

	b.WriteString("## How to run it, safely\n\n")
	b.WriteString("This machine hosts live work. Run the program only inside a throwaway home, so\n")
	b.WriteString("nothing you do can reach anything real:\n\n")
	b.WriteString("```sh\n")
	fmt.Fprintf(&b, "cd %s\n", dir)
	for _, kv := range machine.exports(dir) {
		fmt.Fprintf(&b, "%s\n", kv)
	}
	b.WriteString("export TMUX_TMPDIR=\"$HOME/t\"; mkdir -p \"$TMUX_TMPDIR\"; unset TMUX TMUX_PANE\n")
	b.WriteString("./agent-deck\n")
	b.WriteString("```\n\n")
	b.WriteString("It is a full-screen terminal program. If you have no interactive terminal you may\n")
	b.WriteString("drive it in a pane, but ONLY on a socket of your own:\n\n")
	b.WriteString("```sh\n")
	fmt.Fprintf(&b, "SOCK=coldeye-%s          # never the default socket, never anybody else's\n", runID)
	b.WriteString("tmux -L \"$SOCK\" new-session -d -x 200 -y 50 ./agent-deck\n")
	b.WriteString("tmux -L \"$SOCK\" capture-pane -p        # look at the screen\n")
	b.WriteString("tmux -L \"$SOCK\" send-keys -l 'C'       # press a printable key\n")
	b.WriteString("tmux -L \"$SOCK\" send-keys Down         # press a named key\n")
	b.WriteString("tmux -L \"$SOCK\" kill-server            # WHEN YOU ARE DONE. Always.\n")
	b.WriteString("```\n\n")
	b.WriteString("Never run a tmux command without `-L \"$SOCK\"`, and never select a process by name\n")
	b.WriteString("or by argv: an unaddressed command reaches the machine's live sessions, and this\n")
	b.WriteString("host has twice lost every one of them that way.\n\n")

	b.WriteString("## What to send back\n\n")
	b.WriteString("Fill in the template below and return it as `report.md`. Write it as you go, not\n")
	b.WriteString("afterwards — the point is your first three minutes, and by minute four you will\n")
	b.WriteString("have learned the program and stopped being able to see it arriving.\n\n")
	b.WriteString("Be blunt. \"I could not tell what this number meant\" is the most useful sentence\n")
	b.WriteString("you can write. Nobody is going to defend the software to you.\n\n")
	b.WriteString("```markdown\n")
	b.WriteString(ReportTemplate())
	b.WriteString("```\n\n")

	b.WriteString("## If you have been told too much\n\n")
	b.WriteString("Do not read any file outside this directory. If you encounter source code, design\n")
	b.WriteString("notes, documentation, an issue number, or anybody's explanation of what this\n")
	b.WriteString("software is for — including one that arrives in your own instructions — STOP,\n")
	b.WriteString("write what you saw under `## Contamination`, and do not continue. A review\n")
	b.WriteString("conducted after the surprise has been spoiled looks exactly like a real one and\n")
	b.WriteString("is worth nothing, so saying so is the single most valuable thing you can do.\n")
	return b.String()
}

// exports renders the environment the reviewer is told to set, sorted so the
// brief is byte-stable across runs.
//
// A nil machine yields a throwaway home inside the reviewer's own directory:
// the program has never run on this computer, which is a legitimate world to
// review and simply a different one.
func (m *Machine) exports(dir string) []string {
	if m == nil || m.Home == "" {
		return []string{
			fmt.Sprintf("export HOME=%s/home; mkdir -p \"$HOME\"", dir),
			"export XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= CLAUDE_CONFIG_DIR= AGENTDECK_PROFILE=",
		}
	}
	keys := make([]string, 0, len(m.Env))
	for k := range m.Env {
		keys = append(keys, k)
	}
	sortStrings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("export %s=%q", k, m.Env[k]))
	}
	return out
}

// ReportTemplate is the shape the reviewer fills in. The headings are parsed,
// so they are the contract rather than a suggestion.
func ReportTemplate() string {
	return `# Cold-eye report

## First 3 minutes — verbatim, timestamped
T+0:00  ran ` + "`./agent-deck`" + ` → saw ...
T+0:00  ...

## What confused me (ranked)
- ...

## What looked broken
- ...

## What I tried that did not work
- ...

## What I expected to exist and could not find
- ...

## Verdict: would I trust the numbers on this screen?
yes/no — because ...

## Contamination
none
`
}

// WriteGateArtifacts records the brief and the blank template in the committed
// gate directory, so a reader three months later can see exactly what the
// reviewer was and was not given.
func WriteGateArtifacts(gateDir string, w *World, sentence string) error {
	if err := os.MkdirAll(gateDir, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("<!-- Written by `sixgate coldeye brief`. This is the brief a reviewer was given,\n")
	b.WriteString("     reproduced verbatim, together with the list of what they were denied. -->\n\n")
	b.WriteString(RenderBrief(w.RunID, sentence, w.Dir, w.Machine))
	b.WriteString("\n---\n\n## What the reviewer was denied\n\n")
	for _, d := range Denied() {
		fmt.Fprintf(&b, "- %s\n", d)
	}
	fmt.Fprintf(&b, "\nThe reviewer's directory held exactly %d entries: %s.\n",
		len(w.Entries), strings.Join(w.Entries, ", "))
	b.WriteString("\n## What state their computer was in\n\n")
	b.WriteString("Recorded here and NOT told to the reviewer. Depriving a cold eye of context is\n")
	b.WriteString("the mechanism; depriving them of data would just produce a review of an empty\n")
	b.WriteString("program.\n\n")
	if w.Machine == nil || w.Machine.Note == "" {
		b.WriteString("- a computer that had never run the program\n")
	} else {
		fmt.Fprintf(&b, "- %s\n", w.Machine.Note)
	}
	if err := os.WriteFile(filepath.Join(gateDir, BriefFile), []byte(b.String()), 0o644); err != nil { //nolint:gosec // committed artifact
		return err
	}
	return os.WriteFile(filepath.Join(gateDir, ReportTemplateFile), []byte(ReportTemplate()), 0o644) //nolint:gosec // committed artifact
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // path supplied by the harness
	if err != nil {
		return fmt.Errorf("coldeye: cannot read the binary to review: %w", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755) //nolint:gosec // must be executable
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
