// Package teadrive is SIXGATE's Driver A: it executes a G0 script against the
// real Bubble Tea model, in process, and records the frames a human would see.
//
// WHY IN-PROCESS IS THE DEFAULT, NOT A COMPROMISE. For a full-screen Bubble Tea
// program, View() is the string the terminal paints. The blank percentage that
// created this framework lived in View(); an assertion on View() would have
// caught it before anybody opened the feature. Driving the model directly is
// also millisecond-fast, deterministic, and — decisively on this machine —
// spawns no tmux server and no PTY. This host has twice lost its entire live
// session fleet to harnesses that spawned tmux; a gate framework that leaks a
// PTY is a fleet-killer wearing a QA badge. So the default driver touches
// neither, and records a PTY census proving it.
//
// WHAT THIS DRIVER CANNOT SEE, stated up front rather than discovered later:
// that the shipped binary boots at all; real-terminal wrapping at odd widths;
// ANSI bleed past a cell boundary; alt-screen entry and exit; mouse input. Those
// need a real binary in a real pane, which is Driver B's job. Where both drivers
// run the same script on the same fixture, a differing frame is free evidence.
//
// WHAT IS REAL HERE. The real *ui.Home, the real bubbletea runtime (tea.Program
// with piped input and a buffered output, exactly the shape teatest uses), the
// real key routing, the real hotkey table, the real openContextInspector
// command, the real ctxinspect engine reading a real transcript off disk, and
// the real pager renderer. The only seam is Init(): it is a no-op so the
// constructor's background workers, storage watcher and tmux pipe manager never
// start. See internal/ui/gate_seam.go.
package teadrive

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/sixgate/fixture"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/script"
	"github.com/asheshgoplani/agent-deck/internal/ui"
)

// Name identifies this driver in artifacts.
const Name = "teadrive"

// RunSchema is the run.json schema version.
const RunSchema = 1

// Tunables. They are constants rather than flags because a gate whose timing
// can be argued with is a gate that gets argued with.
const (
	// pollInterval is how often the frame is re-read while waiting.
	pollInterval = 10 * time.Millisecond
	// settleFrames is how many consecutive identical frames count as quiescent.
	// Bubble Tea processes messages on one goroutine in order, but a step may
	// dispatch an async command (the context inspection reads files), so the
	// driver waits for the screen to stop changing rather than guessing.
	settleFrames = 4
	// settleTimeout bounds the wait for quiescence after an action.
	settleTimeout = 5 * time.Second
	// waitForTimeout bounds an explicit wait_for.
	waitForTimeout = 20 * time.Second
	// quitTimeout bounds the runtime shutting down.
	quitTimeout = 5 * time.Second
)

// Options configure one drive.
type Options struct {
	// Script is the validated G0 journey.
	Script *script.Script
	// Fixture is the materialized world to drive against.
	Fixture *fixture.Setup
	// OutDir is the per-fixture artifact directory
	// (docs/gates/<slug>/G1-drive/<fixture>).
	OutDir string
	// Repo is the repository root, recorded so the artifact says which
	// checkout produced it.
	Repo string
	// Tool is the sixgate version string, recorded in run.json.
	Tool string
}

// StepRun is one beat of the journey as it actually happened.
type StepRun struct {
	ID      string `json:"id"`
	Note    string `json:"note,omitempty"`
	Verb    string `json:"verb"`
	Detail  string `json:"detail,omitempty"`
	Capture string `json:"capture,omitempty"`
	// ScreenFile and ANSIFile are the artifacts, relative to OutDir.
	ScreenFile string `json:"screen_file,omitempty"`
	ANSIFile   string `json:"ansi_file,omitempty"`
	// SettleMS is how long the screen took to stop changing after the action.
	SettleMS int `json:"settle_ms"`
	// Polls is how many frames were read while waiting.
	Polls int `json:"polls"`
	// Settled is false when the frame was still changing at the timeout, which
	// makes the capture a snapshot of a moving target and is recorded as such.
	Settled bool `json:"settled"`
	// FrameLines and FrameBytes describe the captured frame.
	FrameLines int `json:"frame_lines,omitempty"`
	FrameBytes int `json:"frame_bytes,omitempty"`
	// Error is non-empty when the step could not be executed at all.
	Error string `json:"error,omitempty"`
}

// PTYCensus records how many pseudo-terminals existed around the run. A driver
// that leaks one is a fleet risk on this host, so the number is an artifact
// rather than an assurance.
type PTYCensus struct {
	Before int `json:"before"`
	After  int `json:"after"`
	Delta  int `json:"delta"`
}

// Run is the machine-readable record of one drive: run.json.
type Run struct {
	Schema int `json:"schema"`
	// Pass is the gate signal SIXGATE's verdict reads. It is false if any step
	// errored, any capture came back empty, or the PTY census moved.
	Pass    bool   `json:"pass"`
	Slug    string `json:"slug"`
	Fixture string `json:"fixture"`
	// FixtureNote explains what world this was, for a reader of the artifact.
	FixtureNote  string            `json:"fixture_note,omitempty"`
	FixtureNotes []string          `json:"fixture_notes,omitempty"`
	FixtureEnv   map[string]string `json:"fixture_env,omitempty"`
	FixtureRoot  string            `json:"fixture_root,omitempty"`

	Driver string `json:"driver"`
	// DriverKind states, in one line, what this driver does and does not prove.
	DriverKind string `json:"driver_kind"`
	// Artifact identifies what was driven. Driver A drives a package build
	// rather than a shipped binary, and says so instead of implying otherwise.
	Artifact  string `json:"artifact"`
	Tool      string `json:"tool"`
	GoVersion string `json:"go_version"`
	GitSHA    string `json:"git_sha"`
	GitDirty  bool   `json:"git_dirty"`

	TermWidth  int `json:"term_width"`
	TermHeight int `json:"term_height"`

	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	DurationMS int    `json:"duration_ms"`

	Steps []StepRun `json:"steps"`

	// TmuxServersSpawned is always zero for this driver and is recorded rather
	// than assumed: an artifact that states the number can be checked, a driver
	// that merely promises cannot.
	TmuxServersSpawned int       `json:"tmux_servers_spawned"`
	PTY                PTYCensus `json:"pty"`

	// Failures are the reasons Pass is false.
	Failures []string `json:"failures,omitempty"`
	// RuntimeBytes is how many bytes the real renderer wrote to its output. A
	// zero here would mean the runtime never painted, which no assertion on
	// View() would notice.
	RuntimeBytes int `json:"runtime_output_bytes"`
}

// Drive executes the script and writes G1's artifacts.
//
// It returns the run record even when the drive failed: a failed gate must
// still leave evidence, because the artifact IS the report.
func Drive(opts Options) (*Run, error) {
	if opts.Script == nil {
		return nil, fmt.Errorf("teadrive: no script")
	}
	if opts.Fixture == nil {
		return nil, fmt.Errorf("teadrive: no fixture")
	}
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return nil, err
	}

	restore, err := opts.Fixture.Apply()
	if err != nil {
		return nil, err
	}
	defer restore()

	sha, dirty := gitState(opts.Repo)
	start := time.Now()
	run := &Run{
		Schema:       RunSchema,
		Slug:         opts.Script.Slug,
		Fixture:      opts.Fixture.Name,
		FixtureNote:  opts.Fixture.Description,
		FixtureNotes: opts.Fixture.Notes,
		FixtureEnv:   opts.Fixture.Env,
		FixtureRoot:  opts.Fixture.Root,
		Driver:       Name,
		DriverKind: "in-process bubbletea: real *ui.Home, real runtime, real renderer; " +
			"no tmux server, no PTY, no shipped binary (that is Driver B)",
		Artifact:           "package build of internal/ui (*ui.Home via ui.NewGateHome)",
		Tool:               opts.Tool,
		GoVersion:          goVersion(),
		GitSHA:             sha,
		GitDirty:           dirty,
		TermWidth:          opts.Script.Term.Width,
		TermHeight:         opts.Script.Term.Height,
		StartedAt:          start.UTC().Format(time.RFC3339Nano),
		TmuxServersSpawned: 0,
	}
	run.PTY.Before = ptyCount()

	model := ui.NewGateHome(ui.GateHomeOptions{
		Width:     opts.Script.Term.Width,
		Height:    opts.Script.Term.Height,
		Instances: opts.Fixture.Instances,
		Cursor:    opts.Fixture.Cursor,
		Profile:   "_sixgate",
	})

	// The real runtime, wired the way teatest wires it: a pipe for input so the
	// program has something to read, and a buffer for output so the renderer
	// actually paints somewhere we can measure.
	inR, inW := io.Pipe()
	out := &syncBuffer{}
	prog := tea.NewProgram(model,
		tea.WithInput(inR),
		tea.WithOutput(out),
		tea.WithoutSignalHandler(),
		tea.WithoutCatchPanics(),
	)

	done := make(chan error, 1)
	go func() {
		_, runErr := prog.Run()
		done <- runErr
	}()

	// A synthetic WindowSizeMsg is how a Bubble Tea program learns its
	// geometry; without it every layout renders against zero and the frames
	// would be a property of the harness rather than of the product.
	prog.Send(tea.WindowSizeMsg{Width: opts.Script.Term.Width, Height: opts.Script.Term.Height})
	waitSettle(model, settleTimeout)

	for _, st := range opts.Script.Steps {
		rec := execStep(prog, model, st, opts.OutDir)
		run.Steps = append(run.Steps, rec)
		if rec.Error != "" {
			run.Failures = append(run.Failures, fmt.Sprintf("step %s: %s", st.ID, rec.Error))
		}
		if st.Capture != "" && rec.FrameBytes == 0 && rec.Error == "" {
			run.Failures = append(run.Failures,
				fmt.Sprintf("step %s captured an empty frame: there is nothing for G2 to assert on", st.ID))
		}
		if !rec.Settled && rec.Error == "" {
			run.Failures = append(run.Failures,
				fmt.Sprintf("step %s never stopped repainting within %s; its frame is a moving target", st.ID, settleTimeout))
		}
	}

	prog.Quit()
	select {
	case runErr := <-done:
		if runErr != nil && runErr.Error() != "" {
			run.Failures = append(run.Failures, "runtime: "+runErr.Error())
		}
	case <-time.After(quitTimeout):
		run.Failures = append(run.Failures, "runtime did not shut down within "+quitTimeout.String())
		prog.Kill()
	}
	_ = inW.Close()
	_ = inR.Close()

	run.RuntimeBytes = out.Len()
	if run.RuntimeBytes == 0 {
		run.Failures = append(run.Failures,
			"the real renderer never wrote a byte: the frames were produced by View() alone and nothing proves the runtime painted")
	}

	run.PTY.After = ptyCount()
	run.PTY.Delta = run.PTY.After - run.PTY.Before
	// Only a POSITIVE delta is this driver's fault. A negative one means some
	// other process on the machine released a pseudo-terminal while the drive
	// ran, which is ordinary on a shared host and was observed on the very
	// first three-fixture run. Failing on it would make the one safety signal
	// that matters here flaky — and a flaky safety gate is a gate that gets
	// switched off, which is how this machine lost its session fleet. The
	// number is recorded either way; only growth is an accusation.
	if run.PTY.Delta > 0 {
		run.Failures = append(run.Failures,
			fmt.Sprintf("PTY census grew by %d: this driver must not create a pseudo-terminal", run.PTY.Delta))
	}

	finish := time.Now()
	run.FinishedAt = finish.UTC().Format(time.RFC3339Nano)
	run.DurationMS = int(finish.Sub(start) / time.Millisecond)
	run.Pass = len(run.Failures) == 0

	if err := writeRun(opts.OutDir, run); err != nil {
		return run, err
	}
	if err := writeTranscript(opts, run); err != nil {
		return run, err
	}
	return run, nil
}

// execStep performs one script step and captures its frame.
func execStep(prog *tea.Program, model *ui.GateHome, st script.Step, outDir string) StepRun {
	rec := StepRun{ID: st.ID, Note: st.Note, Capture: st.Capture}
	kinds := st.Do.Kinds()
	if len(kinds) != 1 {
		rec.Error = "step does not carry exactly one verb; the script should not have validated"
		return rec
	}
	rec.Verb = string(kinds[0])

	begin := time.Now()
	switch kinds[0] {
	case script.ActKey:
		rec.Detail = *st.Do.Key
		if err := sendKey(prog, *st.Do.Key); err != nil {
			rec.Error = err.Error()
			return rec
		}
	case script.ActKeys:
		rec.Detail = strings.Join(st.Do.Keys, " ")
		for _, k := range st.Do.Keys {
			if err := sendKey(prog, k); err != nil {
				rec.Error = err.Error()
				return rec
			}
			waitSettle(model, settleTimeout)
		}
	case script.ActType:
		rec.Detail = *st.Do.Type
		for _, r := range *st.Do.Type {
			prog.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
	case script.ActWait:
		rec.Detail = *st.Do.Wait
		d, err := time.ParseDuration(*st.Do.Wait)
		if err != nil {
			rec.Error = err.Error()
			return rec
		}
		time.Sleep(d)
	case script.ActWaitFor:
		rec.Detail = *st.Do.WaitFor
		if !waitForText(model, *st.Do.WaitFor, waitForTimeout) {
			rec.Error = fmt.Sprintf("waited %s for %q to appear on screen and it never did", waitForTimeout, *st.Do.WaitFor)
			// Fall through: capture the frame anyway. A gate that discards the
			// evidence when it fails is a gate nobody can debug.
		}
	case script.ActRun:
		rec.Detail = *st.Do.Run
		rec.Error = "the run verb is for CLI journeys; this driver drives a TUI model and has no shell"
		return rec
	}

	settled, polls := waitSettle(model, settleTimeout)
	rec.Settled = settled
	rec.Polls = polls
	rec.SettleMS = int(time.Since(begin) / time.Millisecond)

	if st.Capture == "" {
		return rec
	}

	raw := model.Snapshot()
	plain := ansi.Strip(raw)
	rec.ScreenFile = st.ID + ".screen.txt"
	rec.ANSIFile = st.ID + ".screen.ansi"
	rec.FrameBytes = len(strings.TrimSpace(plain))
	rec.FrameLines = len(strings.Split(strings.TrimRight(plain, "\n"), "\n"))

	if err := os.WriteFile(filepath.Join(outDir, rec.ScreenFile), []byte(normalizeFrame(plain)), 0o644); err != nil { //nolint:gosec // committed artifact
		rec.Error = err.Error()
		return rec
	}
	if err := os.WriteFile(filepath.Join(outDir, rec.ANSIFile), []byte(raw), 0o644); err != nil { //nolint:gosec // committed artifact
		rec.Error = err.Error()
	}
	return rec
}

// normalizeFrame trims trailing whitespace per line and guarantees a final
// newline, so a committed frame diffs cleanly without changing what it says.
func normalizeFrame(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n") + "\n"
}

// waitSettle polls the frame until it stops changing, and reports whether it
// did. It returns the number of polls so the artifact records how much waiting
// each step actually needed.
func waitSettle(model *ui.GateHome, timeout time.Duration) (settled bool, polls int) {
	deadline := time.Now().Add(timeout)
	last := ""
	stable := 0
	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		polls++
		cur := model.Snapshot()
		if cur == last {
			stable++
			if stable >= settleFrames {
				return true, polls
			}
			continue
		}
		last = cur
		stable = 0
	}
	return false, polls
}

// waitForText polls until the rendered, ANSI-stripped frame contains substr.
func waitForText(model *ui.GateHome, substr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if strings.Contains(ansi.Strip(model.Snapshot()), substr) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(pollInterval)
	}
}

// keyTypes maps a script's key names onto Bubble Tea key types. Anything not
// here is sent as runes, which is how "C", "2" and "q" arrive.
var keyTypes = map[string]tea.KeyType{
	"enter":     tea.KeyEnter,
	"esc":       tea.KeyEsc,
	"escape":    tea.KeyEsc,
	"tab":       tea.KeyTab,
	"shift+tab": tea.KeyShiftTab,
	"space":     tea.KeySpace,
	"backspace": tea.KeyBackspace,
	"delete":    tea.KeyDelete,
	"up":        tea.KeyUp,
	"down":      tea.KeyDown,
	"left":      tea.KeyLeft,
	"right":     tea.KeyRight,
	"home":      tea.KeyHome,
	"end":       tea.KeyEnd,
	"pgup":      tea.KeyPgUp,
	"pgdown":    tea.KeyPgDown,
}

// sendKey translates one script key name into a tea.KeyMsg.
//
// It is deliberately strict: an unrecognised multi-character name is an error
// rather than a best-effort literal, because silently typing the letters of
// "dwon" into a search box is precisely the kind of harness bug that produces a
// confident, wrong transcript.
func sendKey(prog *tea.Program, name string) error {
	key := strings.TrimSpace(name)
	if key == "" {
		return fmt.Errorf("empty key")
	}
	if kt, ok := keyTypes[strings.ToLower(key)]; ok {
		prog.Send(tea.KeyMsg{Type: kt})
		return nil
	}
	if strings.HasPrefix(strings.ToLower(key), "ctrl+") {
		return fmt.Errorf("key %q is not in this driver's key table; add it rather than approximating it", key)
	}
	runes := []rune(key)
	if len(runes) != 1 {
		return fmt.Errorf("key %q is neither a named key nor a single character", key)
	}
	prog.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: runes})
	return nil
}

// writeRun persists run.json.
func writeRun(dir string, run *Run) error {
	raw, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(filepath.Join(dir, "run.json"), raw, 0o644) //nolint:gosec // committed artifact
}

// writeTranscript persists transcript.md: the artifact a human reads.
//
// It is the whole point of G1. "Done" stops being a builder's claim exactly
// when there is a document showing the software being used, step by step, with
// the real output pasted in — so the frames are reproduced in full rather than
// linked to. A transcript nobody can read without opening six other files is a
// transcript nobody reads.
func writeTranscript(opts Options, run *Run) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# G1 DRIVE — %s — fixture `%s`\n\n", run.Slug, run.Fixture)
	fmt.Fprintf(&b, "> %s\n\n", opts.Script.Sentence)
	b.WriteString("This is a recording of the software being used, not a claim that it works.\n")
	b.WriteString("Every frame below is the literal text the terminal would paint, captured after\n")
	b.WriteString("the keystroke named above it.\n\n")

	fmt.Fprintf(&b, "- **Result:** %s\n", passWord(run.Pass))
	fmt.Fprintf(&b, "- **Driver:** `%s` — %s\n", run.Driver, run.DriverKind)
	fmt.Fprintf(&b, "- **Artifact driven:** %s\n", run.Artifact)
	fmt.Fprintf(&b, "- **Commit:** `%s`%s\n", run.GitSHA, dirtySuffix(run.GitDirty))
	fmt.Fprintf(&b, "- **Terminal:** %dx%d\n", run.TermWidth, run.TermHeight)
	fmt.Fprintf(&b, "- **Fixture:** %s — %s\n", run.Fixture, run.FixtureNote)
	for _, n := range run.FixtureNotes {
		fmt.Fprintf(&b, "  - %s\n", n)
	}
	fmt.Fprintf(&b, "- **tmux servers spawned:** %d\n", run.TmuxServersSpawned)
	fmt.Fprintf(&b, "- **PTYs before / after / delta:** %d / %d / %+d (only growth is this driver's fault; a machine-wide census drifts down on its own)\n",
		run.PTY.Before, run.PTY.After, run.PTY.Delta)
	fmt.Fprintf(&b, "- **Renderer output:** %d bytes (proof the real runtime painted, not just View())\n", run.RuntimeBytes)
	fmt.Fprintf(&b, "- **Duration:** %d ms\n\n", run.DurationMS)

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
		if !s.Settled {
			settled = "**no**"
		}
		fmt.Fprintf(&b, "| `%s` | %s %s | %s | %s (%d ms) |\n",
			s.ID, s.Verb, mdCode(s.Detail), frame, settled, s.SettleMS)
	}
	b.WriteString("\n")

	for _, s := range run.Steps {
		fmt.Fprintf(&b, "### %s — `%s %s`\n\n", s.ID, s.Verb, s.Detail)
		if s.Note != "" {
			fmt.Fprintf(&b, "*%s*\n\n", s.Note)
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

func mdCode(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return "`" + strings.ReplaceAll(s, "|", "\\|") + "`"
}

func passWord(ok bool) string {
	if ok {
		return "PASS — every step ran and every capture produced a frame"
	}
	return "FAIL"
}

func dirtySuffix(dirty bool) string {
	if dirty {
		return " (working tree dirty)"
	}
	return ""
}

// ptyCount counts the pseudo-terminal device nodes present on the host.
//
// It is a read-only directory listing. It identifies nothing by process name
// and kills nothing: on macOS a process keeps its original argv, so argv is not
// identity, and a reaper that believed otherwise destroyed this machine's whole
// session fleet on 2026-07-26. The census is evidence, never an actuator.
func ptyCount() int {
	entries, err := os.ReadDir("/dev")
	if err != nil {
		return -1
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "ttys") || strings.HasPrefix(name, "pts") {
			n++
		}
	}
	return n
}

func goVersion() string {
	out, err := exec.Command("go", "version").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func gitState(repo string) (sha string, dirty bool) {
	sha = "unknown"
	if out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output(); err == nil {
		sha = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "-C", repo, "status", "--porcelain").Output(); err == nil {
		dirty = strings.TrimSpace(string(out)) != ""
	}
	return sha, dirty
}

// FixtureDirs lists the per-fixture drive directories under a G1 root, sorted.
// G2 consumes this so the assert runner never has to guess where the evidence
// is.
func FixtureDirs(g1Root string) ([]string, error) {
	entries, err := os.ReadDir(g1Root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		out = append(out, filepath.Join(g1Root, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}
