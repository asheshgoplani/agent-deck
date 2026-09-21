package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func handleDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOutput := fs.Bool("json", false, "Output account and runtime health diagnostics as JSON")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck doctor [--json]\n\nReport local runtime health and check named Claude slots configured as [profiles.<name>.claude].config_dir.\nWarn when slots share a directory; missing or unreadable paths remain unknown.\nAccount checks read directory metadata; health reads local samples. Neither verifies live login identities.\nAlso lists untracked tmux sessions (agentdeck_ prefix, not in `list --json`) so you can decide whether to keep or stop them; never stops any itself.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return
		}
		os.Exit(2)
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "doctor does not accept positional arguments")
		os.Exit(2)
	}
	config, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: load config: %q\n", err.Error())
		os.Exit(1)
	}
	runtimeHealth, err := readRuntimeHealth("", time.Hour)
	if err != nil {
		// Report returns its schema and budgets even when history cannot be read.
		runtimeHealth.Flags = append(runtimeHealth.Flags, "runtime health unknown: "+strconv.QuoteToASCII(err.Error()))
	}
	runtimeHealth.UntrackedTmuxSessions = untrackedTmuxSessionsForHealth("")
	slots := session.DiagnoseClaudeAccountDirectories(config)
	// Codex notify hook for this host's default CODEX_HOME: without it every
	// codex session made here is content detection only.
	codexConfig := getCodexConfigPath()
	codexHooks := codexHooksStateForConfig(codexConfig)
	// Profile store roots: which data root is active and whether a second
	// one holds profiles too (stray XDG store incidents, 2026-09-19/20).
	storeRoots, storeRootsErr := session.StoreRootReport()
	// #2334: only users who opted into [tmux] indic_zero_width_marks get a
	// note on whether their tmux can apply it.
	var indicMarks *tmux.IndicZeroWidthMarksInfo
	if config.Tmux.IndicZeroWidthMarks {
		info := tmux.CheckIndicZeroWidthMarks()
		indicMarks = &info
	}
	if *jsonOutput {
		report := struct {
			AccountSlots []session.AccountDirectoryDiagnostic `json:"account_slots"`
			Health       health.Summary                       `json:"health"`
			CodexHooks   struct {
				State  string `json:"state"`
				Config string `json:"config"`
			} `json:"codex_hooks"`
			StoreRoots *session.StoreRootSelection   `json:"store_roots,omitempty"`
			StoreError string                        `json:"store_roots_error,omitempty"`
			IndicMarks *tmux.IndicZeroWidthMarksInfo `json:"tmux_indic_zero_width_marks,omitempty"`
		}{AccountSlots: slots, Health: runtimeHealth, IndicMarks: indicMarks}
		report.CodexHooks.State = codexHooks
		report.CodexHooks.Config = codexConfig
		if storeRootsErr != nil {
			report.StoreError = storeRootsErr.Error()
		} else {
			report.StoreRoots = &storeRoots
		}
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			fmt.Fprintf(os.Stderr, "Error: encode diagnostics: %v\n", err)
			os.Exit(1)
		}
		return
	}
	fmt.Print(health.Format(runtimeHealth))
	fmt.Print(formatStoreRoots(storeRoots, storeRootsErr))
	fmt.Printf("Codex notify %s\n", codexHooksLine(codexHooks, codexConfig))
	if indicMarks != nil {
		fmt.Printf("tmux indic_zero_width_marks INFO: %s\n", indicMarks.Detail)
	}
	fmt.Println("Named Claude account directories:")
	if len(slots) == 0 {
		fmt.Println("No named Claude account slots configured.")
		return
	}
	for _, slot := range slots {
		fmt.Printf("%s slot %q: %q (path: %s)", strings.ToUpper(slot.State), slot.Name, slot.ConfigDir, slot.PathState)
		if len(slot.SharedWith) > 0 {
			fmt.Print("; shared with")
			for _, name := range slot.SharedWith {
				fmt.Printf(" %q", name)
			}
		}
		if slot.Reason != "" {
			fmt.Printf("; %s", slot.Reason)
		}
		fmt.Println()
	}
}

// formatStoreRoots renders the profile store roots for `doctor`: the active
// root with its reason, both candidates with per-profile session counts, and
// a WARNING line when both roots hold profiles.
func formatStoreRoots(sel session.StoreRootSelection, err error) string {
	if err != nil {
		return fmt.Sprintf("Profile store: unknown (%v)\n", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Profile store: %s (%s, reason=%s)\n", sel.Active, sel.Kind, sel.Reason)
	for _, root := range []session.StoreRootInfo{sel.Legacy, sel.XDG} {
		marker := ""
		if root.Kind == sel.Kind {
			marker = " [active]"
		}
		fmt.Fprintf(&b, "  %-6s %s: %s%s\n", root.Kind, root.Path, formatStoreRootState(root), marker)
	}
	if warning := sel.Warning(); warning != "" {
		fmt.Fprintf(&b, "  WARNING: %s\n", warning)
	}
	if note := sel.Note(); note != "" {
		fmt.Fprintf(&b, "  Note: %s\n", note)
	}
	return b.String()
}

// formatStoreRootState renders "no profiles", "N sessions (a=1, b=unreadable)"
// or "unreadable" when no store under the root could be counted.
func formatStoreRootState(root session.StoreRootInfo) string {
	if !root.HasProfiles {
		return "no profiles"
	}
	state := fmt.Sprintf("%d sessions", root.Sessions)
	if root.Sessions == 0 && root.Unreadable > 0 {
		state = "unreadable"
	}
	if root.Marker {
		state += ", marker"
	}
	if len(root.Profiles) == 0 {
		return state
	}
	parts := make([]string, 0, len(root.Profiles))
	for _, name := range slices.Sorted(maps.Keys(root.Profiles)) {
		if n := root.Profiles[name]; n < 0 {
			parts = append(parts, name+"=unreadable")
		} else {
			parts = append(parts, fmt.Sprintf("%s=%d", name, n))
		}
	}
	return state + " (" + strings.Join(parts, ", ") + ")"
}
