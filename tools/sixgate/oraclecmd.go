package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/childenv"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/artifact"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/oracle"
)

// cmdOracle is G4: compare every number the software shows against a number
// somebody else produced, and emit the list of figures for which nobody did.
func cmdOracle(args []string) (int, error) {
	if len(args) == 0 {
		return exitUsage, errors.New("usage: sixgate oracle compare|collect <slug> [flags]")
	}
	switch args[0] {
	case "compare":
		return cmdOracleCompare(args[1:])
	case "collect":
		return cmdOracleCollect(args[1:])
	default:
		return exitUsage, fmt.Errorf("unknown oracle sub-verb %q (want compare|collect)", args[0])
	}
}

// oracleGate resolves the G4 gate directory and the declaration inside it.
func oracleGate(t artifact.Tree) (string, string) {
	g, _ := artifact.GateByID(artifact.G4)
	dir := t.GateDir(g)
	return dir, filepath.Join(dir, oracle.SpecFile)
}

// ---------------------------------------------------------------------------
// compare
// ---------------------------------------------------------------------------

func cmdOracleCompare(args []string) (int, error) {
	fs := flag.NewFlagSet("oracle compare", flag.ContinueOnError)
	repo, gates := commonFlags(fs)
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
		printProblems("G0 does not validate, so there are no frames worth grading", ps)
		return exitGate, nil
	}

	gateDir, specPath := oracleGate(t)
	spec, err := oracle.Load(specPath)
	if err != nil {
		return exitUsage, err
	}
	if problems := spec.Validate(); problems != nil {
		fmt.Printf("%s is not a usable oracle declaration (%d problem(s)):\n", t.Rel(specPath), len(problems))
		for _, p := range problems {
			fmt.Printf("  %s\n", p)
		}
		return exitGate, nil
	}
	if spec.Slug != t.Slug {
		return exitUsage, fmt.Errorf("the declaration is for slug %q but lives under docs/gates/%s", spec.Slug, t.Slug)
	}

	g1, _ := artifact.GateByID(artifact.G1)
	parity, err := oracle.Compare(oracle.Input{
		Spec:    spec,
		GateDir: gateDir,
		G1Root:  t.GateDir(g1),
		Tool:    version,
		Now:     time.Now(),
	})
	if err != nil {
		return exitUsage, err
	}
	if err := oracle.Write(gateDir, parity, version); err != nil {
		return exitUsage, err
	}

	fmt.Printf("wrote %s, %s and %s\n",
		t.Rel(filepath.Join(gateDir, oracle.ParityMDFile)),
		t.Rel(filepath.Join(gateDir, oracle.ParityJSONFile)),
		t.Rel(filepath.Join(gateDir, oracle.MustLabelJSONFile)))
	for _, c := range parity.Cases {
		fmt.Printf("  case %-28s %s  (oracle %s)\n", c.ID, passLabel(c.Pass), presentLabel(c.OraclePresent))
		for _, r := range c.Rows {
			fmt.Printf("      %-26s %-14s ours=%s theirs=%s\n", r.Figure, r.Verdict, figureValue(r.Ours), figureValue(r.Theirs))
		}
	}
	fmt.Printf("compared %d figure(s); %d must be labelled an estimate on screen\n", parity.Compared, len(parity.MustLabel))
	if len(parity.MustLabel) > 0 {
		fmt.Printf("G2 now has to prove those labels are rendered. Re-run: sixgate assert %s\n", t.Slug)
	}
	if !parity.Pass {
		for _, p := range parity.Problems {
			fmt.Printf("  %s\n", p)
		}
		return exitGate, nil
	}
	_ = s
	return exitOK, nil
}

func figureValue(r oracle.Reading) string {
	if !r.Found {
		if r.Error != "" {
			return "error"
		}
		return "—"
	}
	return fmt.Sprintf("%.0f", r.Value)
}

func passLabel(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func presentLabel(ok bool) string {
	if ok {
		return "present"
	}
	return "not collected"
}

// ---------------------------------------------------------------------------
// collect
// ---------------------------------------------------------------------------

// collectEnvGuard is set by the matrix runner's environment and, if seen here,
// aborts. It is a belt to the structural braces: `internal/sixgate/matrix` is
// forbidden by selfcheck from importing anything that could reach this code,
// and this file is a `main` package the matrix runner cannot import at all. The
// guard costs nothing and covers the case nobody thought of — a shell wrapper
// that calls the CLI from inside a matrix row.
const collectEnvGuard = "SIXGATE_MATRIX_ROW"

// cmdOracleCollect obtains an oracle file from a LIVE session.
//
// This is the only verb in SIXGATE that can cause a keystroke to be typed into
// somebody's terminal, and everything about it is arranged so that it cannot
// happen by accident:
//
//   - it refuses without --yes, which no automation in this repository passes;
//   - it refuses when the matrix runner's environment is present;
//   - it refuses unless the declaration for that case says collect: command,
//     so a case documented as manual-paste cannot be collected by machine;
//   - it prints the exact command and the session it will touch before running;
//   - it never invents the command — the declaration carries it, so what runs
//     is what was reviewed.
//
// The default path for this repository remains offline: a human runs the other
// tool themselves and pastes the panel into the declared file. On a machine
// holding live sessions that is the only sanctioned mode, and the declaration
// says so per case.
func cmdOracleCollect(args []string) (int, error) {
	fs := flag.NewFlagSet("oracle collect", flag.ContinueOnError)
	repo, gates := commonFlags(fs)
	caseID := fs.String("case", "", "the declared case to collect (required)")
	yes := fs.Bool("yes", false, "consent: this types into a live, billed session")
	dryRun := fs.Bool("dry-run", false, "print the command that would run and stop")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return exitUsage, err
	}
	t, err := resolveTree(*repo, *gates, first(pos))
	if err != nil {
		return exitUsage, err
	}
	gateDir, specPath := oracleGate(t)
	spec, err := oracle.Load(specPath)
	if err != nil {
		return exitUsage, err
	}
	if problems := spec.Validate(); problems != nil {
		fmt.Printf("%s is not a usable oracle declaration; fix it before collecting anything:\n", t.Rel(specPath))
		for _, p := range problems {
			fmt.Printf("  %s\n", p)
		}
		return exitGate, nil
	}

	var target *oracle.Case
	for i := range spec.Cases {
		if spec.Cases[i].ID == *caseID {
			target = &spec.Cases[i]
			break
		}
	}
	if target == nil {
		var ids []string
		for _, c := range spec.Cases {
			ids = append(ids, c.ID)
		}
		return exitUsage, fmt.Errorf("-case is required and must name a declared case (have: %s)", strings.Join(ids, ", "))
	}

	if target.Oracle.Collect != oracle.CollectCommand {
		return exitGate, fmt.Errorf(
			"case %q declares collect: %s, so there is nothing to run. Obtain the oracle the way the declaration says and paste it into %s",
			target.ID, target.Oracle.Collect, filepath.Join(t.Rel(gateDir), target.Oracle.Path))
	}
	if v := os.Getenv(collectEnvGuard); v != "" {
		return exitGate, fmt.Errorf(
			"refusing to collect: %s=%q says this is a matrix row. G3 visits every world it can; a verb that types into a live session must never be one of them",
			collectEnvGuard, v)
	}

	dest := filepath.Join(gateDir, filepath.FromSlash(target.Oracle.Path))
	fmt.Printf("case:     %s\n", target.ID)
	fmt.Printf("oracle:   %s (consent: %s)\n", target.Oracle.Name, target.Oracle.Consent)
	fmt.Printf("argv:     %s\n", strings.Join(target.Oracle.Command, " "))
	fmt.Printf("writes:   %s\n", t.Rel(dest))
	fmt.Printf("\nThis sends keystrokes to a live agent session and may be billed.\n")
	if *dryRun {
		fmt.Printf("dry run: nothing was sent.\n")
		return exitOK, nil
	}
	if !*yes {
		fmt.Printf("Refusing without --yes. Nothing was sent.\n")
		return exitGate, nil
	}

	out, runErr := runOracleCommand(target.Oracle.Command, t.Repo)
	if len(out) > 0 {
		if err := os.MkdirAll(gateDir, 0o755); err != nil {
			return exitUsage, err
		}
		if err := os.WriteFile(dest, out, 0o644); err != nil { //nolint:gosec // committed artifact
			return exitUsage, err
		}
		fmt.Printf("wrote %d bytes to %s\n", len(out), t.Rel(dest))
	}
	if runErr != nil {
		// The output is kept even on a non-zero exit: `session context --verify`
		// has an exit code meaning "indeterminate", and the panel it did manage
		// to read is evidence about why. Discarding it would leave the operator
		// with a failure and nothing to look at.
		fmt.Printf("the collection command failed: %v\n", runErr)
		return exitGate, nil
	}
	fmt.Printf("now run: sixgate oracle compare %s\n", t.Slug)
	return exitOK, nil
}

// runOracleCommand executes the declaration's argv directly.
//
// There is deliberately no shell. The argv is read from the reviewed
// declaration rather than assembled here, so what runs against a live session
// is exactly what somebody approved in a diff — and because it is a list rather
// than a command line, a declaration cannot smuggle a second command, a
// redirection or a pipeline past that review. It is run with the matrix guard
// set in its own environment too, so a nested sixgate invocation cannot loop
// back into another collection.
func runOracleCommand(argv []string, dir string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("the declaration carries no collection argv")
	}
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // argv comes from the reviewed declaration, and there is no shell
	cmd.Dir = dir
	cmd.Env = append(childenv.ForLaunch(""), collectEnvGuard+"=nested-collect")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil && stderr.Len() > 0 {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), err
}
