package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/procowner"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// `agent-deck session ownership` — the recovery surface for #1873.
//
// When a restart is refused because the session still owns a process tree that
// escaped its pane, the operator needs three things and nothing else: see what
// is claimed, reap what is provably ours, and — only as a deliberate act —
// discard a claim that can no longer be verified. Everything here goes through
// the receipt; none of it matches on names, paths or command lines.

func handleSessionOwnership(profile string, args []string) {
	if len(args) == 0 {
		printSessionOwnershipHelp()
		os.Exit(1)
	}
	switch args[0] {
	case "inspect", "status", "show":
		handleSessionOwnershipInspect(profile, args[1:])
	case "reconcile":
		handleSessionOwnershipReconcile(profile, args[1:])
	case "abandon":
		handleSessionOwnershipAbandon(profile, args[1:])
	case "help", "--help", "-h":
		printSessionOwnershipHelp()
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown session ownership command: %s\n", args[0])
		printSessionOwnershipHelp()
		os.Exit(1)
	}
}

func printSessionOwnershipHelp() {
	fmt.Println("Usage: agent-deck session ownership <command> <id|title> [options]")
	fmt.Println()
	fmt.Println("Inspect and recover the processes a session owns (#1873).")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  inspect <id>     Show the ownership receipt and what it verifies to (read-only)")
	fmt.Println("  reconcile <id>   Terminate every process the receipt owns, verify, and clear it")
	fmt.Println("                   (--yes required only when the session's pane is still running)")
	fmt.Println("  abandon <id>     Discard an unverifiable receipt WITHOUT signalling anything")
	fmt.Println()
	fmt.Println("A session records, at spawn, the pid of its pane process bound to that")
	fmt.Println("process's start identity. Only an exact match of both may be signalled;")
	fmt.Println("anything else is reported and left alone.")
}

// resolveOwnershipTarget loads and resolves the session named on the command
// line, exiting with the shared CLI codes when it cannot.
func resolveOwnershipTarget(profile, identifier string, out *CLIOutput) *session.Instance {
	if strings.TrimSpace(identifier) == "" {
		out.Error("session identifier is required", ErrCodeInvalidOperation)
		os.Exit(1)
	}
	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
	}
	return inst
}

func handleSessionOwnershipInspect(profile string, args []string) {
	fs := flag.NewFlagSet("session ownership inspect", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session ownership inspect <id|title> [--json]")
		fmt.Println()
		fmt.Println("Show what this session owns. Read-only: signals nothing, changes nothing.")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	out := NewCLIOutput(*jsonOutput, false)
	inst := resolveOwnershipTarget(profile, fs.Arg(0), out)

	status := inst.OwnershipStatus()
	payload := ownershipPayload(status)
	out.Print(renderOwnershipStatus(inst, status), payload)
	if !status.Admissible() {
		// A non-zero exit lets a script tell "this session is blocked" from
		// "this session is fine" without parsing prose.
		os.Exit(3)
	}
}

func handleSessionOwnershipReconcile(profile string, args []string) {
	fs := flag.NewFlagSet("session ownership reconcile", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	yes := fs.Bool("yes", false, "Confirm reconciling a session whose pane is still running")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session ownership reconcile <id|title> [--yes] [--json]")
		fmt.Println()
		fmt.Println("Terminate every process this session's receipt owns — identity-checked,")
		fmt.Println("SIGTERM then SIGKILL, death verified — and clear the receipt.")
		fmt.Println()
		fmt.Println("Reaping an escaped tree needs no confirmation: its pane is already gone,")
		fmt.Println("which is why the restart was refused. --yes is required only when the")
		fmt.Println("receipt's leader is still the live pane process, because reconciling then")
		fmt.Println("stops the running session rather than cleaning up after a dead one.")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	out := NewCLIOutput(*jsonOutput, false)
	inst := resolveOwnershipTarget(profile, fs.Arg(0), out)

	// Confirmation is asked exactly where the operator might not realise what
	// they are ending. The recovery flow the refusal message points at — an
	// escaped tree whose pane died — is not that case, and making it demand a
	// flag would train people to pass --yes reflexively, which is how a
	// confirmation gate stops being one.
	if status := inst.OwnershipStatus(); status.PaneAttached && !*yes {
		out.Error(fmt.Sprintf(
			"session %s is still running: reconciling would stop its live pane process. "+
				"Re-run with --yes, or use `agent-deck session stop %s`", inst.Title, inst.ID),
			ErrCodeInvalidOperation)
		if !*jsonOutput {
			fmt.Fprintln(os.Stderr, renderOwnershipStatus(inst, status))
		}
		os.Exit(1)
	}

	report, err := inst.ReconcileOwnership()
	if err != nil {
		out.Error(fmt.Sprintf("failed to reconcile ownership: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	payload := map[string]interface{}{
		"instance_id": inst.ID,
		"verdict":     string(report.Verdict),
		"reason":      report.Reason,
		"signalled":   report.Signalled(),
		"outcomes":    reapOutcomePayload(report),
	}
	if report.Verdict != procowner.VerdictClear {
		out.ErrorWithData(
			fmt.Sprintf("ownership could not be fully reconciled: %s", report.Reason),
			ErrCodeInvalidOperation, payload)
		if !*jsonOutput {
			fmt.Fprintln(os.Stderr, report.Describe())
			fmt.Fprintf(os.Stderr,
				"\nNothing unverified was signalled. The receipt is kept so it stays visible;\n"+
					"`agent-deck session ownership abandon %s` discards it without killing anything.\n",
				inst.ID)
		}
		os.Exit(3)
	}
	out.Success(fmt.Sprintf("ownership reconciled: %s", report.Reason), payload)
	if !*jsonOutput && report.Signalled() > 0 {
		fmt.Println(report.Describe())
	}
}

func handleSessionOwnershipAbandon(profile string, args []string) {
	fs := flag.NewFlagSet("session ownership abandon", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	yes := fs.Bool("yes", false, "Confirm: stop managing whatever the receipt named")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session ownership abandon <id|title> --yes [--json]")
		fmt.Println()
		fmt.Println("Discard an ownership receipt WITHOUT signalling anything.")
		fmt.Println()
		fmt.Println("Use this when a receipt can no longer be verified — an unreadable /proc")
		fmt.Println("entry, a truncated receipt — and the session must be startable again.")
		fmt.Println("Any process that receipt named keeps running and agent-deck stops")
		fmt.Println("managing it; find it with the pids from `ownership inspect` first.")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	out := NewCLIOutput(*jsonOutput, false)
	inst := resolveOwnershipTarget(profile, fs.Arg(0), out)

	status := inst.OwnershipStatus()
	if !*yes {
		out.Error("refusing to abandon an ownership receipt without --yes: "+
			"any process it named will keep running unmanaged", ErrCodeInvalidOperation)
		if !*jsonOutput {
			fmt.Fprintln(os.Stderr, renderOwnershipStatus(inst, status))
		}
		os.Exit(1)
	}
	if err := inst.AbandonOwnership(); err != nil {
		out.Error(fmt.Sprintf("failed to abandon ownership receipt: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	out.Success("ownership receipt discarded; nothing was signalled", ownershipPayload(inst.OwnershipStatus()))
}

func renderOwnershipStatus(inst *session.Instance, status session.OwnershipStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Session:  %s (%s)\n", inst.Title, inst.ID)
	switch {
	case status.LoadErr != nil:
		fmt.Fprintf(&b, "Receipt:  UNREADABLE — %v\n", status.LoadErr)
		b.WriteString("Verdict:  unknown (nothing will be signalled)\n")
		return b.String()
	case status.Receipt == nil:
		b.WriteString("Receipt:  none — this session owns no recorded processes\n")
		return b.String()
	}
	r := status.Receipt
	fmt.Fprintf(&b, "Receipt:  generation %d, state %s, provider %s\n", r.Generation, r.State, r.Provider)
	fmt.Fprintf(&b, "Leader:   %s\n", r.Leader)
	fmt.Fprintf(&b, "Members:  %d recorded\n", len(r.Members))
	fmt.Fprintf(&b, "Pane:     %s\n", paneAttachmentLabel(status.PaneAttached))
	fmt.Fprintf(&b, "Verdict:  %s\n", status.Report.Describe())
	if len(status.Survivors) > 0 {
		fmt.Fprintf(&b, "\n%d owned process(es) are alive outside this session's pane:\n", len(status.Survivors))
		for _, m := range status.Survivors {
			fmt.Fprintf(&b, "  %s\n", m)
		}
		fmt.Fprintf(&b, "\nA start or restart is refused until these are reaped:\n")
		fmt.Fprintf(&b, "  agent-deck session ownership reconcile %s\n", inst.ID)
	}
	return b.String()
}

func paneAttachmentLabel(attached bool) string {
	if attached {
		return "the live pane runs this receipt's leader"
	}
	return "no live pane accounts for this receipt"
}

func ownershipPayload(status session.OwnershipStatus) map[string]interface{} {
	payload := map[string]interface{}{
		"instance_id":   status.InstanceID,
		"admissible":    status.Admissible(),
		"reason":        status.Reason(),
		"pane_attached": status.PaneAttached,
		"survivors":     memberPayload(status.Survivors),
	}
	if status.LoadErr != nil {
		payload["receipt_error"] = status.LoadErr.Error()
	}
	if status.Receipt != nil {
		payload["generation"] = status.Receipt.Generation
		payload["state"] = status.Receipt.State
		payload["provider"] = status.Receipt.Provider
		payload["leader"] = memberPayload([]procowner.Member{status.Receipt.Leader})[0]
		payload["members"] = memberPayload(status.Receipt.Members)
		payload["verdict"] = string(status.Report.Verdict)
	}
	return payload
}

func memberPayload(members []procowner.Member) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(members))
	for _, m := range members {
		out = append(out, map[string]interface{}{
			"pid":      m.PID,
			"start_id": m.StartID,
			"pgid":     m.PGID,
			"uid":      m.UID,
			"role":     m.Role,
		})
	}
	return out
}

func reapOutcomePayload(report procowner.ReapReport) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(report.Outcomes))
	for _, o := range report.Outcomes {
		out = append(out, map[string]interface{}{
			"pid":      o.Member.PID,
			"start_id": o.Member.StartID,
			"outcome":  o.Outcome,
			"detail":   o.Detail,
		})
	}
	return out
}
