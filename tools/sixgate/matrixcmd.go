package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sixgate/artifact"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/assert"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/driver/panedrive"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/driver/teadrive"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/fixture"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/lint"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/matrix"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/script"
)

// ---------------------------------------------------------------------------
// matrix — G3
// ---------------------------------------------------------------------------

// rowsDir is where a matrix row's own evidence lands, inside the G3 gate
// directory. Rows are kept in their own subdirectory so that G3's roll-up files
// stay at the top of the gate and a reader opening the gate sees the summary
// before eighteen directories of frames.
const rowsDir = "rows"

// cmdMatrix runs G3: every declared world, each in its own sandbox, each capped.
func cmdMatrix(args []string) (int, error) {
	fs := flag.NewFlagSet("matrix", flag.ContinueOnError)
	repo, gates := commonFlags(fs)
	only := fs.String("rows", "", "comma-separated row ids to run (default: every declared row)")
	keepRoot := fs.String("world-root", "", "materialize sandboxed worlds here instead of a temporary directory (for debugging)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return exitUsage, err
	}
	t, err := resolveTree(*repo, *gates, first(pos))
	if err != nil {
		return exitUsage, err
	}
	s, ps, err := loadScript(t)
	if err != nil {
		return exitUsage, err
	}
	if ps != nil {
		printProblems("G0 does not validate, so there is no journey to run in any world", ps)
		return exitGate, nil
	}

	g3, ok := artifact.GateByID(artifact.G3)
	if !ok {
		return exitUsage, fmt.Errorf("gate G3 is missing from the catalogue")
	}
	g3Dir := t.GateDir(g3)
	spec, mps, err := loadMatrix(g3Dir, s)
	if err != nil {
		return exitUsage, err
	}
	if mps != nil {
		fmt.Printf("%s is not a usable matrix (%d problem(s)):\n", t.Rel(filepath.Join(g3Dir, matrix.SpecFile)), len(mps))
		for _, p := range mps {
			fmt.Printf("  %s\n", p)
		}
		return exitGate, nil
	}
	if spec.Slug != t.Slug {
		return exitUsage, fmt.Errorf("%s declares slug %q but lives under docs/gates/%s", matrix.SpecFile, spec.Slug, t.Slug)
	}

	selected, err := selectMatrixRows(spec, *only)
	if err != nil {
		return exitUsage, err
	}

	sha, dirty := repoGitState(t.Repo)
	res := &matrix.Result{
		Schema:      matrix.ResultSchema,
		Slug:        spec.Slug,
		Sentence:    spec.Sentence,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Harness:     version,
		GitSHA:      sha,
		GitDirty:    dirty,
		Coverage:    spec.CoverageSummary(),
		Exclusions:  spec.Exclude,
		PTYBefore:   devPTYCount(),
	}

	// One build for every pane row. Each row's own run.json still records the
	// binary path, so sharing the build costs no honesty and saves a compile
	// per row.
	binary := ""
	if anyPaneRow(selected) {
		fmt.Printf("building the shipped binary from %s for the driver-B rows ...\n", t.Repo)
		binary, err = panedrive.BuildForRows(t.Repo)
		if err != nil {
			return exitUsage, err
		}
		fmt.Printf("built %s\n\n", binary)
	}

	fmt.Printf("%-34s %-4s %-8s %-9s %s\n", "row", "drv", "outcome", "time", "divergence")
	stopped := ""
	for i, row := range selected {
		if stopped != "" {
			res.Rows = append(res.Rows, notRunRow(row, t, stopped))
			fmt.Printf("%-34s %-4s %-8s %-9s %s\n", row.ID, row.Driver, matrix.OutcomeNotRun, "—", stopped)
			continue
		}
		rr := runMatrixRow(t, s, spec, row, binary, *keepRoot)
		res.Rows = append(res.Rows, rr)
		fmt.Printf("%-34s %-4s %-8s %-9s %s\n", row.ID, row.Driver, rr.Outcome,
			fmt.Sprintf("%dms", rr.DurationMS), divergenceLine(rr))
		for _, f := range rr.Failures {
			fmt.Printf("    %s\n", f)
		}
		if rr.TimedOut && !rr.Unwound {
			// The one condition that stops the run. A row that exceeded its cap
			// and has NOT finished unwinding still owns whatever it owns — for a
			// pane row that can include a live tmux server holding a
			// pseudo-terminal. Starting the next row would put a second world on
			// top of a first that is still moving, and on this host the mistake
			// costs the session fleet, not a flaky result.
			stopped = fmt.Sprintf("row %q exceeded its %d ms cap and had not unwound after the grace period; "+
				"the remaining %d row(s) were not started", row.ID, rr.BudgetMS, len(selected)-i-1)
			res.Aborted = stopped
			res.LeakRisk = row.Driver == matrix.DriverB
		}
	}

	res.Seams = runSeamPairs(t, g3Dir, spec, selected)
	res.PTYAfter = devPTYCount()
	res.Finish()

	if err := matrix.Write(g3Dir, res); err != nil {
		return exitUsage, err
	}
	fmt.Printf("\nwrote %s and %s\n",
		t.Rel(filepath.Join(g3Dir, matrix.ResultMarkdownFile)),
		t.Rel(filepath.Join(g3Dir, matrix.ResultJSONFile)))
	fmt.Printf("%d/%d rows ok · %d blank finding(s) · PTYs %d → %d (%+d)\n",
		res.Totals.OK, res.Totals.Rows, res.Totals.BlankFindings,
		res.PTYBefore, res.PTYAfter, res.PTYAfter-res.PTYBefore)
	if res.LeakRisk {
		fmt.Printf("\nLEAK RISK: a row did not unwind. Verify by SOCKET PATH only (see the row's run.json); never by process name.\n")
	}
	if !res.Pass {
		return exitGate, nil
	}
	return exitOK, nil
}

// loadMatrix reads and validates the declaration next to the gate.
func loadMatrix(g3Dir string, s *script.Script) (*matrix.Spec, matrix.Problems, error) {
	path := filepath.Join(g3Dir, matrix.SpecFile)
	spec, err := matrix.Load(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("no %s: G3 has nothing to run. The matrix is declared, not discovered", path)
		}
		return nil, matrix.Problems{{Path: matrix.SpecFile, Msg: err.Error()}}, nil
	}
	known := matrix.KnownSteps{}
	for _, st := range s.CaptureSteps() {
		known[st.ID] = true
	}
	return spec, spec.Validate(known), nil
}

func selectMatrixRows(spec *matrix.Spec, only string) ([]matrix.Row, error) {
	if strings.TrimSpace(only) == "" {
		return spec.Rows(), nil
	}
	var out []matrix.Row
	for _, raw := range strings.Split(only, ",") {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		r, ok := spec.RowByID(id)
		if !ok {
			return nil, fmt.Errorf("no row %q in the matrix", id)
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-rows named nothing")
	}
	return out, nil
}

func anyPaneRow(rows []matrix.Row) bool {
	for _, r := range rows {
		if r.Driver == matrix.DriverB {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// one row
// ---------------------------------------------------------------------------

// driveFacts is what the two drivers have in common, normalised so the row
// evaluator does not have to know which one ran.
type driveFacts struct {
	// stepFailed maps a step id to why that step went wrong, for steps the
	// driver could not execute cleanly.
	stepFailed map[string]string
	// nonStepFailures are failures that belong to the run rather than to a
	// step: the binary not booting, a teardown that could not be verified, a
	// pseudo-terminal census that grew. These fail a row no matter what the row
	// declared, because no world is allowed to leak.
	nonStepFailures []string

	tmuxServers      int
	ptyDelta         int
	teardownVerified bool
	durationMS       int
}

// runMatrixRow materializes one world, drives it under a wall-time cap, and
// judges it against what the row declared.
func runMatrixRow(t artifact.Tree, s *script.Script, spec *matrix.Spec, row matrix.Row, binary, keepRoot string) matrix.RowResult {
	budget := spec.BudgetFor(row)
	outDir := filepath.Join(t.GateDir(mustGate(artifact.G3)), rowsDir, row.ID)

	rr := matrix.RowResult{
		ID: row.ID, Note: row.Note,
		Adapter: row.Adapter, SessionState: row.SessionState, DataSize: row.DataSize,
		Config: row.Config, Terminal: row.Terminal, Driver: row.Driver,
		Fixture:            fixtureProvenance(t.Repo, row),
		ExpectedDivergence: row.DivergesAt,
		Why:                row.Why,
		Artifact:           t.Rel(outDir),
		BudgetMS:           int(budget / time.Millisecond),
		StepsExpected:      len(s.CaptureSteps()),
	}

	// Every row gets its own sandbox. Sharing one would let a world that wrote
	// a config file decide what the next row sees, which is how a matrix comes
	// to report the state of its own runner.
	root := keepRoot
	if root == "" {
		dir, err := os.MkdirTemp("", "sixgate-matrix-")
		if err != nil {
			rr.Outcome = matrix.OutcomeError
			rr.Failures = []string{"could not create a sandbox: " + err.Error()}
			return rr
		}
		defer func() { _ = os.RemoveAll(dir) }()
		root = dir
	} else {
		root = filepath.Join(root, row.ID)
		if err := os.MkdirAll(root, 0o755); err != nil {
			rr.Outcome = matrix.OutcomeError
			rr.Failures = []string{"could not create a sandbox: " + err.Error()}
			return rr
		}
	}

	rowScript, err := scriptForRow(s, row)
	if err != nil {
		rr.Outcome = matrix.OutcomeError
		rr.Failures = []string{err.Error()}
		return rr
	}

	type result struct {
		facts driveFacts
		err   error
	}
	done := make(chan result, 1)
	started := time.Now()
	go func() {
		facts, derr := driveRow(t, rowScript, row, root, outDir, binary)
		done <- result{facts: facts, err: derr}
	}()

	var facts driveFacts
	select {
	case r := <-done:
		if r.err != nil {
			rr.DurationMS = int(time.Since(started) / time.Millisecond)
			rr.Outcome = matrix.OutcomeError
			rr.Failures = []string{r.err.Error()}
			return rr
		}
		facts = r.facts
	case <-time.After(budget):
		// The row is NEVER killed. Both drivers hold internally bounded waits
		// and a deferred teardown that has to prove the tmux server is gone;
		// terminating one mid-flight is exactly how this machine leaked ~50
		// tmux servers and 507 of its 511 pseudo-terminals on 2026-07-18. So
		// the cap stops the row COUNTING as running and the runner then waits
		// for it to unwind.
		rr.TimedOut = true
		select {
		case r := <-done:
			rr.Unwound = true
			if r.err == nil {
				facts = r.facts
			}
			rr.Failures = append(rr.Failures, fmt.Sprintf(
				"exceeded its %s wall-time cap; it unwound %s later, so its own teardown ran and the following rows were safe to start",
				budget, time.Since(started).Round(time.Second)-budget))
		case <-time.After(spec.UnwindGrace()):
			rr.Unwound = false
			rr.Failures = append(rr.Failures, fmt.Sprintf(
				"exceeded its %s wall-time cap and had NOT unwound %s later. It was not killed: killing a driver mid-flight "+
					"is what leaks tmux servers. The run stops here, and anything this row owns must be verified by SOCKET PATH.",
				budget, spec.UnwindGrace()))
		}
		rr.DurationMS = int(time.Since(started) / time.Millisecond)
		rr.Outcome = matrix.OutcomeTimeout
		rr.StepsWithFrames, rr.ActualDivergence, rr.BlankFindings = inspectFrames(s, outDir)
		return rr
	}

	rr.DurationMS = facts.durationMS
	if rr.DurationMS == 0 {
		rr.DurationMS = int(time.Since(started) / time.Millisecond)
	}
	rr.TmuxServersSpawned = facts.tmuxServers
	rr.PTYDelta = facts.ptyDelta
	rr.TeardownVerified = facts.teardownVerified

	rr.StepsWithFrames, rr.ActualDivergence, rr.BlankFindings = inspectFrames(s, outDir)
	judgeRow(&rr, row, facts)
	return rr
}

// driveRow builds the world the row declares and drives it.
func driveRow(t artifact.Tree, rowScript *script.Script, row matrix.Row, root, outDir, binary string) (driveFacts, error) {
	v := fixture.Variant{SessionState: row.SessionState, DataSize: row.DataSize}

	if row.Driver == matrix.DriverA {
		setup, err := fixture.FromCorpusVariant(row.Fixture, root, v)
		if err != nil {
			return driveFacts{}, err
		}
		run, err := teadrive.Drive(teadrive.Options{
			Script: rowScript, Fixture: setup, OutDir: outDir, Repo: t.Repo, Tool: version,
		})
		if err != nil {
			return driveFacts{}, err
		}
		return factsFromTeadrive(run), nil
	}

	var world *panedrive.World
	var err error
	if row.Config == matrix.ConfigCold {
		world, err = panedrive.ColdFirstRun(root)
	} else {
		world, err = panedrive.LoadedWith(row.Fixture, root, v)
	}
	if err != nil {
		return driveFacts{}, err
	}
	if row.StopAfter != "" {
		world.StopAfter, world.StopReason = row.StopAfter, row.StopReason
	}
	run, err := panedrive.Drive(panedrive.Options{
		Script: rowScript, World: world, OutDir: outDir, Repo: t.Repo, Tool: version, Binary: binary,
	})
	if err != nil {
		return driveFacts{}, err
	}
	return factsFromPanedrive(run), nil
}

func factsFromTeadrive(run *teadrive.Run) driveFacts {
	f := driveFacts{
		stepFailed:       map[string]string{},
		tmuxServers:      run.TmuxServersSpawned,
		ptyDelta:         run.PTY.Delta,
		teardownVerified: true, // this driver has nothing to tear down: it spawns no server and no pty
		durationMS:       run.DurationMS,
	}
	for _, st := range run.Steps {
		if why := stepTrouble(st.Error, st.Capture, st.FrameBytes, st.Settled, ""); why != "" {
			f.stepFailed[st.ID] = why
		}
	}
	if run.RuntimeBytes == 0 {
		f.nonStepFailures = append(f.nonStepFailures,
			"the real renderer never wrote a byte: nothing proves the runtime painted")
	}
	if run.PTY.Delta > 0 {
		f.nonStepFailures = append(f.nonStepFailures,
			fmt.Sprintf("the pseudo-terminal census grew by %d; this driver must not create one", run.PTY.Delta))
	}
	return f
}

func factsFromPanedrive(run *panedrive.Run) driveFacts {
	f := driveFacts{
		stepFailed:       map[string]string{},
		tmuxServers:      run.TmuxServersSpawned,
		ptyDelta:         run.PTY.Delta,
		teardownVerified: run.Teardown.Verified,
		durationMS:       run.DurationMS,
	}
	for _, st := range run.Steps {
		if why := stepTrouble(st.Error, st.Capture, st.FrameBytes, st.Settled, st.Skipped); why != "" {
			f.stepFailed[st.ID] = why
		}
	}
	// Everything the driver itself flagged that is not about a single step:
	// the binary not booting, a first-run question with no offered answer, a
	// teardown that could not be verified, a census that grew.
	for _, fail := range run.Failures {
		if strings.HasPrefix(fail, "step ") {
			continue
		}
		f.nonStepFailures = append(f.nonStepFailures, fail)
	}
	return f
}

// stepTrouble reports why a step is not clean, or "" when it is.
func stepTrouble(stepErr, capture string, frameBytes int, settled bool, skipped string) string {
	if skipped != "" {
		// A skipped step is scoped out by the row, not failed by the product.
		// Its missing frame still counts as a divergence, which is where the
		// row's declaration has to account for it.
		return ""
	}
	switch {
	case stepErr != "":
		return stepErr
	case capture != "" && frameBytes == 0:
		return "captured an empty frame"
	case !settled:
		return "never stopped repainting; its frame is a moving target"
	}
	return ""
}

// inspectFrames reads the row's recorded frames the way G2 does — from the
// rendered text only — and reports how many capture steps produced a frame,
// where the journey first diverged, and every Blank Detector finding.
//
// It deliberately reuses the G2 evaluator rather than re-implementing it: a
// matrix that judged frames by its own slightly different rules would give a row
// a pass that G2 would not, which is the sort of discrepancy nobody notices
// until it matters.
func inspectFrames(s *script.Script, dir string) (framesWithContent int, firstDivergent string, findings []lint.Finding) {
	fr, err := assert.EvaluateDir(s, dir)
	if err != nil {
		return 0, "", nil
	}
	findings = append(findings, fr.BlankFindings...)
	findings = append(findings, scanExtraFrames(s, dir)...)
	return len(fr.Frames), fr.FirstDivergentStep, findings
}

// scanExtraFrames runs the Blank Detector over frames the journey never named —
// the first-run modals a cold machine shows before the deck appears.
//
// Those frames are the literal first thing a new user sees, so leaving them
// unscanned would exempt the most important screen in the matrix from the one
// rule that has no opt-in.
func scanExtraFrames(s *script.Script, dir string) []lint.Finding {
	stepFrames := map[string]bool{}
	for _, st := range s.CaptureSteps() {
		stepFrames[st.ID+assert.ScreenSuffix] = true
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*"+assert.ScreenSuffix))
	if err != nil {
		return nil
	}
	sort.Strings(matches)
	var out []lint.Finding
	for _, m := range matches {
		name := filepath.Base(m)
		if stepFrames[name] {
			continue
		}
		raw, err := os.ReadFile(m) //nolint:gosec // path from the gate tree
		if err != nil {
			continue
		}
		// Only journey-wide suppressions apply: a suppression scoped to a step
		// says nothing about a frame that belongs to no step.
		out = append(out, lint.Scan(name, string(raw), s.AllowedRulesFor(""))...)
	}
	return out
}

// judgeRow decides the row's outcome.
//
// The order is the argument. A Blank Detector finding outranks everything,
// because a row may declare that its world looks different and may never
// declare that a figure is allowed to be missing. A safety failure outranks the
// journey, because a leaked server is not a rendering opinion. Only then is the
// declared divergence compared — and it is compared for EQUALITY, not for "at
// least as far", so a row that starts failing one step earlier fails the gate.
func judgeRow(rr *matrix.RowResult, row matrix.Row, facts driveFacts) {
	rr.Failures = append(rr.Failures, facts.nonStepFailures...)
	if row.Driver == matrix.DriverB && !facts.teardownVerified {
		rr.Failures = append(rr.Failures,
			"teardown could not be verified for this row; a tmux server may still be alive on its socket")
	}

	// Step trouble at or after the declared divergence is the consequence of
	// the divergence, not a second finding: a wait_for that times out on step
	// 06 because step 04 already went another way is one fact, reported once.
	for id, why := range facts.stepFailed {
		if row.Expect == matrix.ExpectDiverges && row.DivergesAt != "" && id >= row.DivergesAt {
			continue
		}
		rr.Failures = append(rr.Failures, fmt.Sprintf("step %s: %s", id, why))
	}
	sort.Strings(rr.Failures)

	switch {
	case len(rr.BlankFindings) > 0:
		rr.Outcome = matrix.OutcomeBlank
	case len(rr.Failures) > 0:
		rr.Outcome = matrix.OutcomeError
	case rr.ActualDivergence != rr.ExpectedDivergence:
		rr.Outcome = matrix.OutcomeDivergence
		rr.Failures = append(rr.Failures, divergenceMismatch(rr))
	default:
		rr.Outcome = matrix.OutcomeOK
	}
}

func divergenceMismatch(rr *matrix.RowResult) string {
	switch {
	case rr.ExpectedDivergence == "":
		return fmt.Sprintf("this row declared that the whole journey holds, and it stopped holding at %s", rr.ActualDivergence)
	case rr.ActualDivergence == "":
		return fmt.Sprintf("this row declared a divergence at %s and the journey held all the way through; "+
			"the declaration is now describing a world that no longer exists", rr.ExpectedDivergence)
	default:
		return fmt.Sprintf("declared divergence at %s, actual %s", rr.ExpectedDivergence, rr.ActualDivergence)
	}
}

func notRunRow(row matrix.Row, t artifact.Tree, why string) matrix.RowResult {
	return matrix.RowResult{
		ID: row.ID, Note: row.Note,
		Adapter: row.Adapter, SessionState: row.SessionState, DataSize: row.DataSize,
		Config: row.Config, Terminal: row.Terminal, Driver: row.Driver,
		Fixture:            fixtureProvenance(t.Repo, row),
		ExpectedDivergence: row.DivergesAt,
		Why:                row.Why,
		Outcome:            matrix.OutcomeNotRun,
		Failures:           []string{why},
	}
}

// scriptForRow clones the journey at the row's terminal geometry.
//
// The G0 script owns the journey; a matrix row owns the screen it is played on.
// Cloning rather than mutating keeps a row from changing what the next one runs.
func scriptForRow(s *script.Script, row matrix.Row) (*script.Script, error) {
	w, h, err := row.Geometry()
	if err != nil {
		return nil, err
	}
	clone := *s
	clone.Term = script.Term{Width: w, Height: h}
	return &clone, nil
}

// ---------------------------------------------------------------------------
// provenance, seams and small facts
// ---------------------------------------------------------------------------

// fixtureProvenance records where a row's world came from and how old it is.
func fixtureProvenance(repo string, row matrix.Row) matrix.FixtureProvenance {
	if row.Config == matrix.ConfigCold {
		return matrix.FixtureProvenance{
			Name:        "cold-first-run",
			Synthesized: true,
		}
	}
	rel := filepath.ToSlash(filepath.Join("internal", "ctxinspect", "ctxfixture", "cases", row.Fixture))
	return matrix.FixtureProvenance{
		Name:       row.Fixture,
		Source:     rel,
		RecordedAt: lastCommitDate(repo, rel),
		PinnedNow:  fixture.FixedNow.UTC().Format(time.RFC3339),
	}
}

// lastCommitDate is when the fixture last changed in version control, which is
// the honest answer to "how old is this evidence". A corpus case recorded
// against a harness format that has since moved on is reassuring and wrong, and
// only a date makes that visible in the report.
func lastCommitDate(repo, rel string) string {
	out, err := exec.Command("git", "-C", repo, "log", "-1", "--format=%cI", "--", rel).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// runSeamPairs compares the declared A-versus-B pairs and writes each report.
func runSeamPairs(t artifact.Tree, g3Dir string, spec *matrix.Spec, ran []matrix.Row) []matrix.SeamRow {
	if len(spec.SeamPairs) == 0 {
		return nil
	}
	present := map[string]bool{}
	for _, r := range ran {
		present[r.ID] = true
	}
	var out []matrix.SeamRow
	for _, p := range spec.SeamPairs {
		row := matrix.SeamRow{A: p.A, B: p.B, Why: p.Why}
		if !present[p.A] || !present[p.B] {
			row.Note = "not compared: one of the two rows was not part of this run"
			out = append(out, row)
			continue
		}
		dirA := filepath.Join(g3Dir, rowsDir, p.A)
		dirB := filepath.Join(g3Dir, rowsDir, p.B)
		cmp, err := compareSeamDirs(dirA, dirB)
		if err != nil {
			row.Note = "not compared: " + err.Error()
			out = append(out, row)
			continue
		}
		if len(cmp.stats) == 0 {
			row.Note = "not compared: the two rows share no frame, so there is nothing the drivers could disagree about"
			out = append(out, row)
			continue
		}
		row.FramesCompared = len(cmp.stats)
		row.NumericDisagreements = len(cmp.numeric)
		for _, f := range cmp.numeric {
			row.Findings = append(row.Findings, matrix.SeamFinding{Frame: f.Frame, A: f.A, B: f.B})
		}
		reportPath := filepath.Join(g3Dir, fmt.Sprintf("seam-%s-vs-%s.md", p.A, p.B))
		if err := writeSeamPairReport(reportPath, p, cmp); err != nil {
			row.Note = "comparison made but the report could not be written: " + err.Error()
		} else {
			row.Report = t.Rel(reportPath)
		}
		out = append(out, row)
	}
	return out
}

// writeSeamPairReport writes one matrix seam comparison.
func writeSeamPairReport(path string, p matrix.SeamPair, cmp seamComparison) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Seam divergence — `%s` (driver A) versus `%s` (driver B)\n\n", p.A, p.B)
	fmt.Fprintf(&b, "> %s\n\n", strings.TrimSpace(p.Why))
	b.WriteString("Same G0 journey, same recorded case, two drivers. Only NUMERIC disagreements are\n")
	b.WriteString("treated as findings — two lines identical except for their digits — because the\n")
	b.WriteString("rest is chrome the two drivers legitimately paint differently, and reporting it\n")
	b.WriteString("would bury the one that matters.\n\n")

	if len(cmp.numeric) == 0 {
		b.WriteString("## Numeric disagreements\n\nNone. Every line both rows rendered carried identical figures.\n\n")
	} else {
		b.WriteString("## Numeric disagreements\n\n")
		for _, f := range cmp.numeric {
			fmt.Fprintf(&b, "**`%s`**\n\n```\nA: %s\nB: %s\n```\n\n", f.Frame, f.A, f.B)
		}
	}

	b.WriteString("## Per-frame summary\n\n")
	b.WriteString("| frame | lines A/B | shapes compared | numeric disagreements | only A | only B |\n")
	b.WriteString("|-------|-----------|-----------------|-----------------------|--------|--------|\n")
	for _, st := range cmp.stats {
		fmt.Fprintf(&b, "| `%s` | %d/%d | %d | %d | %d | %d |\n",
			st.name, st.linesA, st.linesB, st.comparedShapes, st.numericFindings, st.onlyA, st.onlyB)
	}
	b.WriteString("\n")
	return os.WriteFile(path, []byte(b.String()), 0o644) //nolint:gosec // committed artifact
}

func divergenceLine(rr matrix.RowResult) string {
	switch {
	case rr.ActualDivergence == "" && rr.ExpectedDivergence == "":
		return "held throughout"
	case rr.ActualDivergence == rr.ExpectedDivergence:
		return rr.ActualDivergence + " (as declared)"
	case rr.ActualDivergence == "":
		return "held throughout, but " + rr.ExpectedDivergence + " was declared"
	default:
		return rr.ActualDivergence + " (declared " + orDash(rr.ExpectedDivergence) + ")"
	}
}

func mustGate(id artifact.GateID) artifact.GateSpec {
	g, ok := artifact.GateByID(id)
	if !ok {
		panic("sixgate: gate " + string(id) + " is missing from the catalogue")
	}
	return g
}

// devPTYCount counts the host's pseudo-terminal device nodes.
//
// It is a read-only directory listing. It identifies nothing by process name and
// terminates nothing: on macOS a process keeps its original argv, so argv is not
// identity, and a reaper that believed otherwise destroyed this machine's whole
// session fleet on 2026-07-26. The census is evidence, never an actuator.
func devPTYCount() int {
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

func repoGitState(repo string) (sha string, dirty bool) {
	sha = "unknown"
	if out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output(); err == nil {
		sha = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "-C", repo, "status", "--porcelain").Output(); err == nil {
		dirty = strings.TrimSpace(string(out)) != ""
	}
	return sha, dirty
}
