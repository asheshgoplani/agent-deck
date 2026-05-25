package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
		printKanbanHelp(os.Stderr)
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
		printKanbanHelp(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown kanban command '%s'\n", args[0])
		fmt.Fprintln(os.Stderr, "Valid commands: list, show, create, block, unblock, complete, comment, attach, help")
		printKanbanHelp(os.Stderr)
		os.Exit(1)
	}
}

// printKanbanHelp prints usage for `agent-deck kanban` to the given writer.
// Pass os.Stdout when help is explicitly requested; os.Stderr when help is
// printed alongside an error (no args, unknown verb).
func printKanbanHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage: agent-deck kanban <command> [options]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Manage Hermes Kanban tasks from agent-deck.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  list [-p profile] [--status <status>] [--status <status>...]")
	fmt.Fprintln(w, "                          List tasks (e.g. --status running, --status blocked)")
	fmt.Fprintln(w, "  show <task-id>          Show task details")
	fmt.Fprintln(w, "  create \"<title>\" --session <id> [--body \"...\"]")
	fmt.Fprintln(w, "                          Create a task and link it to a session")
	fmt.Fprintln(w, "  block <task-id> \"<reason>\"")
	fmt.Fprintln(w, "                          Mark a task as blocked")
	fmt.Fprintln(w, "  unblock <task-id>       Remove blocked status")
	fmt.Fprintln(w, "  complete <task-id> [--summary \"...\"]")
	fmt.Fprintln(w, "                          Mark a task as complete")
	fmt.Fprintln(w, "  comment <task-id> \"<text>\"")
	fmt.Fprintln(w, "                          Add a comment to a task")
	fmt.Fprintln(w, "  attach <session-id-or-title> <task-id>")
	fmt.Fprintln(w, "                          Link an existing session to a task")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flag forms:")
	fmt.Fprintln(w, "  --status accepts both space-separated repeats (--status running --status blocked)")
	fmt.Fprintln(w, "           and the equals form (--status=running).")
	fmt.Fprintln(w, "  -p / --profile is accepted on every verb but has no effect (kanban is global).")
	fmt.Fprintln(w, "  --session is meaningful only for 'create'; on other verbs it is stripped with a note.")
	fmt.Fprintln(w, "  Any unrecognised flags pass through to hermes verbatim (e.g. --json on 'list').")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  agent-deck kanban list")
	fmt.Fprintln(w, "  agent-deck kanban list --status done")
	fmt.Fprintln(w, "  agent-deck kanban show TASK-42")
	fmt.Fprintln(w, "  agent-deck kanban create \"Fix login bug\" --session my-project")
	fmt.Fprintln(w, "  agent-deck kanban block TASK-42 \"waiting for API keys\"")
	fmt.Fprintln(w, "  agent-deck kanban unblock TASK-42")
	fmt.Fprintln(w, "  agent-deck kanban complete TASK-42 --summary \"deployed to prod\"")
	fmt.Fprintln(w, "  agent-deck kanban comment TASK-42 \"checked in PR #123\"")
	fmt.Fprintln(w, "  agent-deck kanban attach my-project TASK-42")
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

// handleKanbanPassthrough runs `hermes kanban <verb> <args...>`, stripping any
// -p/--profile flag (Kanban is global) and any --session flag (hermes does not
// understand it, and these verbs do not perform session auto-attach) before
// forwarding.
func handleKanbanPassthrough(verb string, args []string) {
	afterProfile, profileVal := extractKanbanProfileFlag(args)
	if profileVal != "" {
		fmt.Fprintf(os.Stderr, "Note: Hermes Kanban is global — -p/--profile flag is accepted but has no effect.\n")
	}
	remaining, session := extractKanbanSessionFlag(afterProfile)
	if session != nil {
		fmt.Fprintf(os.Stderr, "Note: --session has no effect on 'kanban %s'; flag ignored.\n", verb)
	}
	hermesArgs := append([]string{"kanban", verb}, remaining...)
	runHermes(hermesArgs)
}

// handleKanbanCreate creates a task via `hermes kanban create … --json`, parses
// the task ID from the JSON output, then auto-attaches the session.
func handleKanbanCreate(args []string) {
	// Extract --session flag from args before forwarding.
	remaining, session := extractKanbanSessionFlag(args)

	// --session was present but had no valid value — fail loudly rather than
	// silently skipping the auto-attach the user requested.
	if session != nil && *session == "" {
		fmt.Fprintln(os.Stderr, "Error: --session requires a non-empty value that does not start with '-'")
		fmt.Fprintln(os.Stderr, "Usage: agent-deck kanban create \"<title>\" --session <id> [--body \"...\"]")
		os.Exit(1)
	}

	if session == nil {
		// No --session specified: delegate directly, no auto-attach.
		hermesArgs := append([]string{"kanban", "create"}, remaining...)
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
		fmt.Fprintf(os.Stderr, "Error: --session %q was requested but auto-attach failed: could not extract task ID from hermes response.\n", *session)
		fmt.Fprintf(os.Stderr, "       The task may have been created; re-run `agent-deck kanban attach %s <task-id>` manually once you have the task ID.\n", *session)
		os.Exit(1)
	}

	// Auto-attach the session to the newly created task.
	attachSession(*session, taskID)
}

// parseKanbanAttachArgs validates and extracts session and task IDs from attach
// args. Returned as a pure helper so the validation logic is testable without
// forking a subprocess or capturing os.Exit.
func parseKanbanAttachArgs(args []string) (sessionID, taskID string, err error) {
	if len(args) < 2 {
		return "", "", fmt.Errorf("kanban attach requires <session-id-or-title> and <task-id>")
	}
	if args[0] == "" || args[1] == "" {
		return "", "", fmt.Errorf("kanban attach session and task IDs must be non-empty")
	}
	return args[0], args[1], nil
}

// handleKanbanAttach links an existing agent-deck session to a Kanban task.
// Usage: attach <session-id-or-title> <task-id>
func handleKanbanAttach(args []string) {
	sessionID, taskID, err := parseKanbanAttachArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintln(os.Stderr, "Usage: agent-deck kanban attach <session-id-or-title> <task-id>")
		os.Exit(1)
	}
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
//
// The returned `session` discriminates three states the old (string, bool)
// return conflated — and makes the fourth (impossible) state unrepresentable:
//   - session == nil          → flag absent
//   - session != nil && *session == ""  → flag present but malformed (missing
//     value, value started with '-', or `--session=`)
//   - session != nil && *session != ""  → flag present with a usable value
//
// Callers that require a value (e.g. handleKanbanCreate) should hard-error on
// the malformed case; passthrough callers can treat it the same as the
// well-formed case (the flag is ignored either way, but the user gets a note).
func extractKanbanSessionFlag(args []string) (remaining []string, session *string) {
	var val string
	seen := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--session" {
			seen = true
			if i+1 < len(args) && args[i+1] != "" && !strings.HasPrefix(args[i+1], "-") {
				val = args[i+1]
				i++
			}
			continue
		}
		if strings.HasPrefix(arg, "--session=") {
			seen = true
			if v := strings.TrimPrefix(arg, "--session="); v != "" {
				val = v
			}
			continue
		}
		remaining = append(remaining, arg)
	}
	if seen {
		session = &val
	}
	return remaining, session
}
