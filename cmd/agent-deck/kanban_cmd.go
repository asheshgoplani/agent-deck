package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleKanban is the top-level dispatcher for `agent-deck kanban <verb> …`.
// Most subcommands delegate directly to `hermes kanban <verb> …`, streaming
// stdout/stderr so the user sees the output in real time.  The one exception
// is `attach`, which touches agent-deck internals to link a session record to
// a Kanban task ID.
func handleKanban(args []string) {
	if len(args) == 0 {
		printKanbanHelp()
		os.Exit(1)
	}

	switch args[0] {
	case "list":
		handleKanbanList(args[1:])
	case "show":
		handleKanbanPassthrough("show", args[1:])
	case "create":
		handleKanbanCreate(args[1:])
	case "block":
		handleKanbanPassthrough("block", args[1:])
	case "unblock":
		handleKanbanPassthrough("unblock", args[1:])
	case "complete":
		handleKanbanPassthrough("complete", args[1:])
	case "comment":
		handleKanbanPassthrough("comment", args[1:])
	case "attach":
		handleKanbanAttach(args[1:])
	case "help", "--help", "-h":
		printKanbanHelp()
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown kanban command '%s'\n", args[0])
		printKanbanHelp()
		os.Exit(1)
	}
}

// printKanbanHelp prints usage for `agent-deck kanban`.
func printKanbanHelp() {
	fmt.Println("Usage: agent-deck kanban <command> [options]")
	fmt.Println()
	fmt.Println("Manage Hermes Kanban tasks from agent-deck.")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  list [-p profile] [--status <status>] [--status <status>...]")
	fmt.Println("                          List tasks (e.g. --status running, --status blocked)")
	fmt.Println("  show <task-id>          Show task details")
	fmt.Println("  create \"<title>\" --session <id> [--body \"...\"]")
	fmt.Println("                          Create a task and link it to a session")
	fmt.Println("  block <task-id> \"<reason>\"")
	fmt.Println("                          Mark a task as blocked")
	fmt.Println("  unblock <task-id>       Remove blocked status")
	fmt.Println("  complete <task-id> [--summary \"...\"]")
	fmt.Println("                          Mark a task as complete")
	fmt.Println("  comment <task-id> \"<text>\"")
	fmt.Println("                          Add a comment to a task")
	fmt.Println("  attach <session-id-or-title> <task-id>")
	fmt.Println("                          Link an existing session to a task")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  agent-deck kanban list")
	fmt.Println("  agent-deck kanban list --status done")
	fmt.Println("  agent-deck kanban show TASK-42")
	fmt.Println("  agent-deck kanban create \"Fix login bug\" --session my-project")
	fmt.Println("  agent-deck kanban block TASK-42 \"waiting for API keys\"")
	fmt.Println("  agent-deck kanban unblock TASK-42")
	fmt.Println("  agent-deck kanban complete TASK-42 --summary \"deployed to prod\"")
	fmt.Println("  agent-deck kanban comment TASK-42 \"checked in PR #123\"")
	fmt.Println("  agent-deck kanban attach my-project TASK-42")
}

// handleKanbanList delegates to `hermes kanban list` with optional status filter.
// The -p / --profile flag is accepted but is a no-op because Hermes Kanban is
// global, not per-profile.
func handleKanbanList(args []string) {
	// Strip -p / --profile flags from args (no-op with note).
	filtered, profileVal := extractKanbanProfileFlag(args)
	if profileVal != "" {
		fmt.Fprintf(os.Stderr, "Note: Hermes Kanban is global — -p/--profile flag is accepted but has no effect.\n")
	}

	// Check for --status flag in remaining args.
	clean, statusVal := extractKanbanStatusFlag(filtered)

	// hermes --status only accepts a single value. When multiple statuses are
	// requested (comma-separated), run one hermes call per status so results
	// are combined. With no filter, list all tasks.
	if statusVal == "" {
		runHermes(append([]string{"kanban", "list"}, clean...))
		return
	}

	statuses := strings.Split(statusVal, ",")
	if len(statuses) == 1 {
		hermesArgs := append([]string{"kanban", "list", "--status", statuses[0]}, clean...)
		runHermes(hermesArgs)
		return
	}

	for _, s := range statuses {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		hermesArgs := append([]string{"kanban", "list", "--status", s}, clean...)
		runHermes(hermesArgs)
	}
}

// handleKanbanPassthrough runs `hermes kanban <verb> <args...>` verbatim,
// streaming output directly to the terminal.
func handleKanbanPassthrough(verb string, args []string) {
	hermesArgs := append([]string{"kanban", verb}, args...)
	runHermes(hermesArgs)
}

// handleKanbanCreate creates a task via `hermes kanban create … --json`, parses
// the task ID from the JSON output, then auto-attaches the session.
func handleKanbanCreate(args []string) {
	// Extract --session flag from args before forwarding.
	remaining, sessionID := extractKanbanSessionFlag(args)

	if sessionID == "" {
		// No --session specified: delegate directly, no auto-attach.
		hermesArgs := append([]string{"kanban", "create"}, args...)
		runHermes(hermesArgs)
		return
	}

	// Run hermes kanban create with --json so we can parse the task ID.
	hermesArgs := append([]string{"kanban", "create", "--json"}, remaining...)
	cmd := exec.Command("hermes", hermesArgs...) //nolint:gosec
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: hermes kanban create failed: %v\n", err)
		os.Exit(1)
	}

	// Print JSON output so the user sees it.
	fmt.Print(out.String())

	// Parse task ID from JSON output.
	taskID := parseTaskIDFromJSON(out.Bytes())
	if taskID == "" {
		fmt.Fprintf(os.Stderr, "Warning: could not parse task ID from hermes output; skipping auto-attach.\n")
		return
	}

	// Auto-attach the session to the newly created task.
	attachSession(sessionID, taskID)
}

// handleKanbanAttach links an existing agent-deck session to a Kanban task.
// Usage: attach <session-id-or-title> <task-id>
func handleKanbanAttach(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: agent-deck kanban attach <session-id-or-title> <task-id>")
		os.Exit(1)
	}
	sessionID := args[0]
	taskID := args[1]
	attachSession(sessionID, taskID)
}

// attachSession loads storage, resolves the session, sets KanbanTaskID, and saves.
func attachSession(sessionID, taskID string) {
	storage, instances, groups, err := loadSessionData("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	inst, errMsg, _ := ResolveSession(sessionID, instances)
	if inst == nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", errMsg)
		os.Exit(2)
	}

	inst.KanbanTaskID = taskID

	groupTree := session.NewGroupTreeWithGroups(instances, groups)
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to save session: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Linked session '%s' to kanban task %s\n", inst.Title, taskID)
}

// runHermes runs `hermes <args...>` with stdout and stderr wired to the
// terminal.  Exits with hermes's exit code on failure.
func runHermes(args []string) {
	cmd := exec.Command("hermes", args...) //nolint:gosec
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "Error: hermes: %v\n", err)
		os.Exit(1)
	}
}

// parseTaskIDFromJSON tries to extract a task "id" field from JSON output
// produced by `hermes kanban create --json`.
func parseTaskIDFromJSON(data []byte) string {
	var result map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(data), &result); err != nil {
		return ""
	}
	for _, key := range []string{"id", "task_id", "taskId", "task-id"} {
		if v, ok := result[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// extractKanbanProfileFlag removes -p / --profile <value> from args and
// returns (remaining, profileValue).
func extractKanbanProfileFlag(args []string) ([]string, string) {
	var remaining []string
	var profileVal string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-p" || arg == "--profile" {
			if i+1 < len(args) && args[i+1] != "" && !strings.HasPrefix(args[i+1], "-") {
				profileVal = args[i+1]
				i++
			} else {
				fmt.Fprintln(os.Stderr, "warning: --profile requires a non-empty value; flag ignored")
			}
			continue
		}
		if strings.HasPrefix(arg, "-p=") {
			if val := strings.TrimPrefix(arg, "-p="); val != "" {
				profileVal = val
			} else {
				fmt.Fprintln(os.Stderr, "warning: -p= requires a non-empty value; flag ignored")
			}
			continue
		}
		if strings.HasPrefix(arg, "--profile=") {
			if val := strings.TrimPrefix(arg, "--profile="); val != "" {
				profileVal = val
			} else {
				fmt.Fprintln(os.Stderr, "warning: --profile= requires a non-empty value; flag ignored")
			}
			continue
		}
		remaining = append(remaining, arg)
	}
	return remaining, profileVal
}

// extractKanbanStatusFlag removes all --status <value> occurrences from args and
// returns the remaining args along with a comma-joined string of all status values.
// Multiple --status flags (e.g. --status running --status blocked) are supported.
func extractKanbanStatusFlag(args []string) ([]string, string) {
	var remaining []string
	var statuses []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--status" {
			if i+1 < len(args) && args[i+1] != "" && !strings.HasPrefix(args[i+1], "-") {
				statuses = append(statuses, args[i+1])
				i++
			} else {
				fmt.Fprintln(os.Stderr, "warning: --status requires a non-empty value; flag ignored")
			}
			continue
		}
		if strings.HasPrefix(arg, "--status=") {
			if val := strings.TrimPrefix(arg, "--status="); val != "" {
				statuses = append(statuses, val)
			} else {
				fmt.Fprintln(os.Stderr, "warning: --status= requires a non-empty value; flag ignored")
			}
			continue
		}
		remaining = append(remaining, arg)
	}
	return remaining, strings.Join(statuses, ",")
}

// extractKanbanSessionFlag removes --session <value> from args.
func extractKanbanSessionFlag(args []string) ([]string, string) {
	var remaining []string
	var sessionVal string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--session" {
			if i+1 < len(args) && args[i+1] != "" && !strings.HasPrefix(args[i+1], "-") {
				sessionVal = args[i+1]
				i++
			} else {
				fmt.Fprintln(os.Stderr, "warning: --session requires a non-empty value; flag ignored")
			}
			continue
		}
		if strings.HasPrefix(arg, "--session=") {
			if val := strings.TrimPrefix(arg, "--session="); val != "" {
				sessionVal = val
			} else {
				fmt.Fprintln(os.Stderr, "warning: --session= requires a non-empty value; flag ignored")
			}
			continue
		}
		remaining = append(remaining, arg)
	}
	return remaining, sessionVal
}
