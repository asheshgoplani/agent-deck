package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sixgate/artifact"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/coldeye"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/driver/panedrive"
)

// cmdColdeye is G5: hand a reviewer the built binary and one sentence, and
// grade what comes back.
func cmdColdeye(args []string) (int, error) {
	if len(args) == 0 {
		return exitUsage, errors.New("usage: sixgate coldeye brief|outcome <slug> [flags]")
	}
	switch args[0] {
	case "brief":
		return cmdColdeyeBrief(args[1:])
	case "outcome":
		return cmdColdeyeOutcome(args[1:])
	default:
		return exitUsage, fmt.Errorf("unknown coldeye sub-verb %q (want brief|outcome)", args[0])
	}
}

func coldeyeGateDir(t artifact.Tree) string {
	g, _ := artifact.GateByID(artifact.G5)
	return t.GateDir(g)
}

// defaultColdeyeSentence is what the reviewer is told this software is.
//
// It names the product and nothing else. It does not say "context inspector",
// it does not mention tokens, and it does not hint that a particular screen is
// under review — because the first question a cold eye asks is the one a briefed
// reviewer never gets to ask.
const defaultColdeyeSentence = "A terminal dashboard for managing AI coding sessions."

func cmdColdeyeBrief(args []string) (int, error) {
	fs := flag.NewFlagSet("coldeye brief", flag.ContinueOnError)
	repo, gates := commonFlags(fs)
	sentence := fs.String("sentence", defaultColdeyeSentence, "the ONE sentence the reviewer is allowed to know")
	binary := fs.String("binary", "", "an already-built binary to review (default: build the shipped command from this checkout)")
	parent := fs.String("parent", "/tmp", "where to create the reviewer's directory")
	seed := fs.String("seed", "", "a recorded corpus case to seed the reviewer's computer with (default: a computer that has never run the program)")
	runID := fs.String("run-id", "", "run identifier (default: random)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return exitUsage, err
	}
	t, err := resolveTree(*repo, *gates, first(pos))
	if err != nil {
		return exitUsage, err
	}

	id := *runID
	if id == "" {
		id, err = randomRunID()
		if err != nil {
			return exitUsage, err
		}
	}

	src := *binary
	if src == "" {
		fmt.Printf("building the shipped binary from %s ...\n", t.Repo)
		src, err = panedrive.BuildForRows(t.Repo)
		if err != nil {
			return exitUsage, err
		}
		fmt.Printf("built %s\n", src)
	}

	// The machine the reviewer will run the program on. Seeding it is not a
	// hint: they are told nothing about what is on it, only how to run under a
	// throwaway home. An empty program tells a reviewer about its empty state
	// and nothing about the screen anybody actually uses.
	var machine *coldeye.Machine
	if *seed != "" {
		root := filepath.Join(*parent, "coldeye-"+id+"-machine")
		if err := os.MkdirAll(root, 0o755); err != nil {
			return exitUsage, err
		}
		world, err := panedrive.Loaded(*seed, root)
		if err != nil {
			return exitUsage, err
		}
		machine = &coldeye.Machine{
			Home: world.Home,
			Env:  world.Env,
			Note: fmt.Sprintf("a computer already in use: %s (recorded case %q, materialized at %s)", world.Description, *seed, root),
		}
	}

	w, err := coldeye.BuildWorld(*parent, id, *sentence, src, machine)
	if err != nil {
		return exitUsage, err
	}
	gateDir := coldeyeGateDir(t)
	if err := coldeye.WriteGateArtifacts(gateDir, w, *sentence); err != nil {
		return exitUsage, err
	}

	fmt.Printf("\nreviewer's world: %s\n", w.Dir)
	for _, e := range w.Entries {
		fmt.Printf("  %s\n", e)
	}
	fmt.Printf("\nthat is everything they get. Denied:\n")
	for _, d := range coldeye.Denied() {
		fmt.Printf("  - %s\n", d)
	}
	fmt.Printf("\nwrote %s and %s\n",
		t.Rel(filepath.Join(gateDir, coldeye.BriefFile)),
		t.Rel(filepath.Join(gateDir, coldeye.ReportTemplateFile)))
	fmt.Printf("\nSend a reviewer to %s with no other context. When their report comes back,\n", w.Dir)
	fmt.Printf("save it as %s, answer every finding in %s, then run:\n",
		t.Rel(filepath.Join(gateDir, coldeye.ReportFile)),
		t.Rel(filepath.Join(gateDir, coldeye.ResolutionsFile)))
	fmt.Printf("  sixgate coldeye outcome %s\n", t.Slug)
	return exitOK, nil
}

func cmdColdeyeOutcome(args []string) (int, error) {
	fs := flag.NewFlagSet("coldeye outcome", flag.ContinueOnError)
	repo, gates := commonFlags(fs)
	pos, err := parseFlags(fs, args)
	if err != nil {
		return exitUsage, err
	}
	t, err := resolveTree(*repo, *gates, first(pos))
	if err != nil {
		return exitUsage, err
	}
	gateDir := coldeyeGateDir(t)

	var report *coldeye.Report
	reportPath := filepath.Join(gateDir, coldeye.ReportFile)
	if _, statErr := os.Stat(reportPath); statErr == nil {
		report, err = coldeye.ParseReport(reportPath)
		if err != nil {
			return exitUsage, err
		}
	}
	res, err := coldeye.LoadResolutions(filepath.Join(gateDir, coldeye.ResolutionsFile))
	if err != nil {
		return exitUsage, err
	}

	out := coldeye.Grade(t.Slug, version, report, res, time.Now())
	if err := coldeye.Write(gateDir, out); err != nil {
		return exitUsage, err
	}
	fmt.Printf("wrote %s and %s\n",
		t.Rel(filepath.Join(gateDir, coldeye.OutcomeMDFile)),
		t.Rel(filepath.Join(gateDir, coldeye.OutcomeJSONFile)))
	fmt.Printf("  report present:  %v\n", out.ReportPresent)
	fmt.Printf("  contamination:   %s\n", orNone(out.Contamination))
	fmt.Printf("  findings:        %d\n", len(out.Items))
	for _, it := range out.Items {
		state := it.Status
		if !it.Closed {
			state = "UNCLOSED"
		}
		fmt.Printf("      %-10s %s\n", state, it.Quote)
	}
	if !out.Pass {
		for _, p := range out.Problems {
			fmt.Printf("  %s\n", p)
		}
		return exitGate, nil
	}
	fmt.Printf("G5 PASS\n")
	return exitOK, nil
}

func orNone(s string) string {
	if s == "" {
		return "(none stated)"
	}
	return s
}

// randomRunID produces the 8-hex identifier a run is keyed by.
func randomRunID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
