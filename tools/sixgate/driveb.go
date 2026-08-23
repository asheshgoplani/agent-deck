package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/sixgate/artifact"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/driver/panedrive"
)

// rowSpec declares one Driver B row: which world it builds, where its evidence
// lands, and why it lands there.
//
// The "where" is load-bearing and is the reason this is a table rather than a
// flag. G1 is the gate that records THE journey, and G2 asserts every capture
// step of that journey against every directory it finds under G1-drive. A row
// that cannot execute the whole journey therefore does not belong under G1: its
// missing frames would be reported as failures of the product, when they are
// really the scope of the row.
//
// The cold first run is exactly such a row. A machine that has never run
// agent-deck owns no session, so there is nothing to press C on. What it proves
// — that the shipped binary boots from nothing, what it asks first, what an
// empty deck looks like — is what G3's mandatory cold-first-run row is for, so
// its evidence is written there.
var rowSpecs = []struct {
	name  string
	gate  artifact.GateID
	build func(root string) (*panedrive.World, error)
	why   string
}{
	{
		name: "cold-first-run",
		gate: artifact.G3,
		build: func(root string) (*panedrive.World, error) {
			return panedrive.ColdFirstRun(root)
		},
		why: "no config, no profile, no database, no sessions — the mandatory matrix row where first-run modals and empty states live",
	},
	{
		name: "claude-cold",
		gate: artifact.G1,
		build: func(root string) (*panedrive.World, error) {
			return panedrive.Loaded("claude-cold", root)
		},
		why: "the recorded claude-cold case seeded into a real profile database, so the shipped binary loads it the way a user's would",
	},
	{
		name: "claude-unknown-model",
		gate: artifact.G1,
		build: func(root string) (*panedrive.World, error) {
			return panedrive.LoadedWithModel("claude-cold", root, untaughtModel)
		},
		why: "the same session on a model identifier this build has never been taught — the window resolves to unknown, " +
			"the gauge loses its percentage, and this is the arrival screen the feature's own user hit first",
	},
}

// untaughtModel is the identifier the unknown-window row runs on.
//
// It is not invented: it is an identifier recorded by live sessions on this
// repository's maintainer's own deck, and internal/ctxinspect/claude/window.go
// resolves no window for it. Choosing a fictional identifier would make the row
// a thought experiment; choosing this one makes it a report of what a user of
// this build sees right now.
//
// IT WAS "claude-opus-5", AND THAT ROTTED. The window table was taught the
// 5-series, and from that commit the row kept its name, kept passing, and
// quietly recorded a TAUGHT screen: header "window 1.0M tokens (model-default)"
// and a live percentage, under a fixture called claude-unknown-model. Nothing
// failed, because G0's step-04 assertion accepts a percentage OR the honest
// unknown sentence — it cannot tell which world it is in. The row's own doc
// comment had predicted exactly this ("a case recorded on an untaught model
// would stop being untaught the moment the table learned it") and it happened
// anyway, so the prediction is now a maintenance rule:
//
//	Whenever modelWindows learns this identifier, this row stops testing
//	anything. Re-point it at one the table still refuses, and say so here.
//
// A bare alias is the sturdier choice: the table is keyed on the versioned
// identifiers a provider ships, so an alias is unlikely to be taught by the
// same commit that teaches a release. It is also not hypothetical — two live
// sessions on the maintainer's deck (stream-fub-visibility,
// stream-conversation-brain) record their model as exactly this and get no
// percentage today.
const untaughtModel = "opus"

// cmdDriveB is G1/G3's real-artifact driver: the shipped binary, a real
// terminal, and a socket nobody else can reach.
func cmdDriveB(args []string) (int, error) {
	fs := flag.NewFlagSet("drive-b", flag.ContinueOnError)
	repo, gates := commonFlags(fs)
	rows := fs.String("rows", "", "comma-separated rows to drive (default: all)")
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
		printProblems("G0 does not validate, so there is no journey to drive", ps)
		return exitGate, nil
	}

	selected, err := selectRows(*rows)
	if err != nil {
		return exitUsage, err
	}

	// One build, every row. The binary path is recorded in each run.json, so
	// sharing it costs no honesty and saves a compile per row.
	fmt.Printf("building the shipped binary from %s ...\n", t.Repo)
	binary, err := panedrive.BuildForRows(t.Repo)
	if err != nil {
		return exitUsage, err
	}
	fmt.Printf("built %s\n\n", binary)

	allPass := true
	for _, name := range selected {
		spec := rowByName(name)
		gate, ok := artifact.GateByID(spec.gate)
		if !ok {
			return exitUsage, fmt.Errorf("gate %s is missing from the catalogue", spec.gate)
		}
		outDir := filepath.Join(t.GateDir(gate), "pane-"+spec.name)

		root := *keepRoot
		if root == "" {
			root, err = os.MkdirTemp("", "sixgate-world-")
			if err != nil {
				return exitUsage, err
			}
			defer func(dir string) { _ = os.RemoveAll(dir) }(root)
		} else {
			root = filepath.Join(root, spec.name)
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			return exitUsage, err
		}

		world, err := spec.build(root)
		if err != nil {
			return exitUsage, err
		}
		run, err := panedrive.Drive(panedrive.Options{
			Script: s, World: world, OutDir: outDir, Repo: t.Repo, Tool: version, Binary: binary,
		})
		if err != nil {
			return exitUsage, err
		}

		fmt.Printf("%-22s %s  %s\n", spec.name, statusWord(run.Pass), t.Rel(outDir))
		fmt.Printf("    socket %s → %s\n", run.Isolation.SocketName, run.Isolation.SocketPath)
		fmt.Printf("    teardown verified: %v   postcondition `%s` failed as required: %v   servers still answering: %d\n",
			run.Teardown.Verified, run.Teardown.PostconditionCmd, run.Teardown.PostconditionFailed, len(run.Teardown.LiveSockets))
		fmt.Printf("    ptys %d → %d (%+d)   servers spawned: %d\n",
			run.PTY.Before, run.PTY.After, run.PTY.Delta, run.TmuxServersSpawned)
		for _, f := range run.Failures {
			fmt.Printf("    FAIL %s\n", f)
		}
		if !run.Pass {
			allPass = false
		}

		// Where Driver A has already driven the same recorded case, the two
		// seams can be compared for free. Doing it here, while both drives are
		// on disk, keeps the finding durable instead of leaving it to whoever
		// happens to diff two directories later.
		if spec.gate == artifact.G1 {
			g1, ok := artifact.GateByID(artifact.G1)
			if ok {
				seam, err := writeSeamReport(t.GateDir(g1), spec.name)
				if err != nil {
					return exitUsage, err
				}
				if seam != "" {
					fmt.Printf("    seam vs Driver A: %s\n", t.Rel(seam))
				}
			}
		}
	}
	if !allPass {
		return exitGate, nil
	}
	fmt.Printf("\nDriver B recorded. A drive does not judge — that is G2:\n  sixgate assert %s\n", t.Slug)
	return exitOK, nil
}

func rowByName(name string) struct {
	name  string
	gate  artifact.GateID
	build func(root string) (*panedrive.World, error)
	why   string
} {
	for _, r := range rowSpecs {
		if r.name == name {
			return r
		}
	}
	return rowSpecs[0]
}

func selectRows(flagValue string) ([]string, error) {
	all := make([]string, 0, len(rowSpecs))
	known := map[string]bool{}
	for _, r := range rowSpecs {
		all = append(all, r.name)
		known[r.name] = true
	}
	if strings.TrimSpace(flagValue) == "" {
		return all, nil
	}
	var out []string
	for _, raw := range strings.Split(flagValue, ",") {
		n := strings.TrimSpace(raw)
		if n == "" {
			continue
		}
		if !known[n] {
			return nil, fmt.Errorf("no row named %q; Driver B has: %s", n, strings.Join(all, ", "))
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-rows named nothing")
	}
	return out, nil
}
