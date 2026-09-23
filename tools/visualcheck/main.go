// Command visualcheck drives a real agent-deck binary through every TUI
// screen and dialog, in a private tmux server against a sandboxed HOME and a
// seeded store, captures text frames at 80x24/120x40/200x50 after each step,
// diffs them against committed goldens, and writes an HTML contact sheet.
//
// See docs/CORE-PLAN.md section 7 ("Visual check") and
// tools/visualcheck/README.md.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
)

type frameReport struct {
	step, width, status, reason, frame string
}

func main() { os.Exit(runMain(os.Args[1:])) }

func runMain(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: visualcheck <agent-deck-binary>")
		return 2
	}
	if runtime.GOOS == "darwin" && os.Getenv("VISUALCHECK_ALLOW_DARWIN") != "1" {
		fmt.Fprintln(os.Stderr, "visualcheck drives a Linux tmux/hook stack; run it on the g14 test box (see README.md). Set VISUALCHECK_ALLOW_DARWIN=1 to force a local dry run while developing the check itself.")
		return 2
	}
	bin, err := filepath.Abs(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	updateDir, err := os.MkdirTemp("", "visualcheck-update-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer os.RemoveAll(updateDir)
	updateBin := filepath.Join(updateDir, "agent-deck")
	_, sourceFile, _, _ := runtime.Caller(0)
	build := exec.CommandContext(ctx, "go", "build", "-ldflags", "-X main.Version=99.0.0", "-o", updateBin, "./cmd/agent-deck")
	build.Dir = filepath.Dir(filepath.Dir(filepath.Dir(sourceFile)))
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build sandbox update: %v: %s\n", err, out)
		return 2
	}

	var reports []frameReport
	failed := false
	// Each width gets its own sandbox: its own HOME, its own private tmux
	// server, its own freshly seeded store and live sessions -- not one
	// shared seed resized three times. Rehearsal found that sharing the
	// live tmux panes/hook-status files across widths (only the sqlite
	// store was ever per-width, via a checkpoint+restore) let one width's
	// dialog interactions and fork drift the *other* widths' sessions'
	// hook state between when they were seeded and when a later width's
	// TUI actually read them -- a real, reproducible source of DIFFs that
	// no snapshot of the database alone could prevent, because the drift
	// was never in the database.
	for _, spec := range widthSpecs {
		frames, runErr := runWidthIsolated(ctx, bin, updateBin, spec)
		seen := map[string]bool{}
		for _, f := range frames {
			seen[f.step] = true
			if f.advisory != "" {
				reports = append(reports, frameReport{step: f.step, width: f.width, status: "ADVISORY", reason: f.advisory})
				continue
			}
			st, cmpErr := compareGolden(f.step, f.width, f.scrub)
			if cmpErr != nil {
				fmt.Fprintln(os.Stderr, "compare golden:", cmpErr)
				reports = append(reports, frameReport{step: f.step, width: f.width, status: "FAIL", reason: cmpErr.Error(), frame: f.scrub})
				failed = true
				continue
			}
			reports = append(reports, frameReport{step: f.step, width: f.width, status: st.status, frame: f.scrub})
			if st.status != "PASS" {
				failed = true
			}
		}
		if runErr != nil {
			fmt.Fprintln(os.Stderr, "run", spec.name+":", runErr)
			failed = true
		}
		for _, step := range expectedFrames {
			if !seen[step] {
				reports = append(reports, frameReport{step: step, width: spec.name, status: "FAIL", reason: "screen was not captured"})
				failed = true
			}
		}
	}

	if err := writeContactSheet("contact-sheet.html", reports); err != nil {
		fmt.Fprintln(os.Stderr, "write contact sheet:", err)
		return 1
	}
	if err := writeJSONReport("visualcheck-report.json", reports); err != nil {
		fmt.Fprintln(os.Stderr, "write report:", err)
		return 1
	}

	fmt.Println("\n| Step | Width | Result |\n|---|---|---|")
	for _, r := range reports {
		fmt.Printf("| %s | %s | %s |\n", r.step, r.width, r.status)
	}
	fmt.Println("\ncontact-sheet.html and visualcheck-report.json written. A DIFF needs a reviewer's PASS before the golden is updated.")

	if failed {
		return 1
	}
	return 0
}

func writeJSONReport(path string, reports []frameReport) error {
	type row struct {
		Step, Width, Status, Reason string
	}
	out := make([]row, len(reports))
	for i, r := range reports {
		out[i] = row{Step: r.step, Width: r.width, Status: r.status, Reason: r.reason}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}
