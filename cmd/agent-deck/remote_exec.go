package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// remoteCommandArgs accepts only commands with a meaningful server-side scope.
// In particular, a typo can never fall through into a local command handler.
func remoteCommandArgs(args []string) ([]string, error) {
	if len(args) > 0 {
		switch args[0] {
		case "list", "status", "health", "add", "launch":
			return append([]string(nil), args...), nil
		case "remote":
			if len(args) > 1 && (args[1] == "list" || args[1] == "ls") {
				return append([]string(nil), args...), nil
			}
		case "show", "output", "send":
			return append([]string{"session"}, args...), nil
		case "session":
			if len(args) > 1 {
				switch args[1] {
				case "show", "output", "send", "start", "stop", "restart", "fork", "archive", "unarchive", "set", "context":
					return append([]string(nil), args...), nil
				case "switch", "switch-preview", "switch-account":
					if err := validateRemoteSwitchArgs(args[1], args[2:]); err != nil {
						return nil, err
					}
					return append([]string(nil), args...), nil
				}
			}
		case "worktree":
			if len(args) > 1 {
				switch args[1] {
				case "list", "info", "cleanup":
					return append([]string(nil), args...), nil
				}
			}
		case "mcp", "skill":
			// list is read-only: the TUI's remote new-session dialog offers
			// the server's MCP names from it (mcp list --quiet, names only).
			if len(args) > 1 && (args[1] == "attach" || args[1] == "list") {
				return append([]string(nil), args...), nil
			}
		case "group":
			if len(args) > 1 {
				switch args[1] {
				case "list", "reorder":
					return append([]string(nil), args...), nil
				}
			}
		}
	}
	return nil, fmt.Errorf("unsupported remote command %q; run 'agent-deck remote' for supported commands", strings.Join(args, " "))
}

// remoteSwitchOptions is the closed option set forwarded for each switch verb.
// true marks an option that takes a value. Anything else (in particular any
// path-shaped or unknown option) is refused here, before SSH: the switch
// engine runs on the remote host and may only be pointed at the remote's own
// configured account slots and harnesses, never at a controller path.
var remoteSwitchOptions = map[string]map[string]bool{
	"switch":         {"to-harness": true, "to-account": true, "max-bytes": true, "no-start": false, "confirm-context-loss": false, "json": false},
	"switch-preview": {"to-harness": true, "to-account": true, "max-chars": true, "json": false},
	"switch-account": {"no-restart": false, "json": false, "quiet": false, "q": false},
}

// remoteSwitchValueOK admits the value shapes each option can take: a plain
// account/harness token, or a decimal byte budget.
func remoteSwitchValueOK(name, value string) bool {
	if value == "" {
		return false
	}
	switch name {
	case "max-bytes", "max-chars":
		_, err := strconv.Atoi(value)
		return err == nil
	default:
		return session.ValidateRemoteSwitchToken(name, value) == nil
	}
}

// validateRemoteSwitchArgs checks the arguments of `session switch`,
// `session switch-preview` and `session switch-account` before they are
// forwarded. A help request passes through unchanged so the remote's own
// usage text answers.
func validateRemoteSwitchArgs(verb string, args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return nil
	}
	options := remoteSwitchOptions[verb]
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		// A bare "--" trims to an empty name and is refused as unknown.
		name, value, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		takesValue, known := options[name]
		if !known {
			return fmt.Errorf("unsupported option %q for remote session %s", arg, verb)
		}
		if !takesValue {
			if inline {
				return fmt.Errorf("option --%s takes no value", name)
			}
			continue
		}
		if !inline {
			rest := args[i+1:]
			if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
				return fmt.Errorf("option --%s needs a value", name)
			}
			value = rest[0]
			i++
		}
		if !remoteSwitchValueOK(name, value) {
			return fmt.Errorf("invalid value %q for --%s", value, name)
		}
	}
	want := 1
	if verb == "switch-account" {
		want = 2
	}
	if len(positional) != want {
		return fmt.Errorf("remote session %s expects %d positional argument(s), got %d", verb, want, len(positional))
	}
	if err := session.ValidateRemoteSwitchSelector(positional[0]); err != nil {
		return err
	}
	if verb == "switch-account" {
		return session.ValidateRemoteSwitchToken("account", positional[1])
	}
	return nil
}

// Forward message files through stdin: their paths belong to the controller,
// while all project/worktree paths deliberately belong to the remote host.
func remoteMessageInput(args []string) ([]string, io.Reader, func(), error) {
	closeInput := func() {}
	offset := 1
	if args[0] != "launch" {
		if len(args) < 2 || args[0] != "session" || (args[1] != "send" && args[1] != "start") {
			return args, os.Stdin, closeInput, nil
		}
		offset = 2
	}
	// The boolean options of launch/start/send do not consume a following token.
	// Unknown options are conservatively treated as value-taking: they must never
	// cause a flag-shaped value to be opened as a controller file.
	boolOptions := " json quiet q no-wait wait stream draft defer-if-busy assert-done no-assert-done no-parent inherit-group no-transition-notify title-lock no-title-sync inherit-telegram-env no-identity b new-branch no-channel-link sandbox yolo gemini-yolo attach allow-repo-scripts "
	// Creation booleans come from the same registered parser as capabilities.
	// Otherwise a new boolean can swallow --message-file as its apparent value.
	if args[0] == "launch" {
		boolOptions = " "
		for _, field := range creationCommandFields("launch") {
			if !field.TakesValue {
				boolOptions += field.Name + " "
			}
		}
	}
	forwarded := append([]string(nil), args[:offset]...)
	messagePath := ""
	found := false
	for i := offset; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			forwarded = append(forwarded, args[i:]...)
			break
		}
		name, value, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			forwarded = append(forwarded, arg)
			continue
		}
		if name != "message-file" {
			forwarded = append(forwarded, arg)
			if !inline && !strings.Contains(boolOptions, " "+name+" ") && i+1 < len(args) {
				i++
				forwarded = append(forwarded, args[i])
			}
			continue
		}
		if !inline {
			if i+1 == len(args) {
				return nil, nil, closeInput, fmt.Errorf("--message-file needs a value")
			}
			i++
			value = args[i]
		}
		messagePath, found = value, true // Go flags use the last assignment.
	}
	if !found || messagePath == "" {
		return args, os.Stdin, closeInput, nil
	}
	var input io.Reader = os.Stdin
	if messagePath != "-" {
		file, err := os.Open(messagePath)
		if err != nil {
			return nil, nil, closeInput, fmt.Errorf("read message file: %w", err)
		}
		input = file
		closeInput = func() { _ = file.Close() }
	}
	// Place the effective option before any positional/-- terminator.
	forwarded = append(forwarded[:offset:offset], append([]string{"--message-file", "-"}, forwarded[offset:]...)...)
	return forwarded, input, closeInput, nil
}

func handleRemoteExec(name string, args []string) {
	code, err := runRemoteExec(name, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
	}
	if code != 0 {
		os.Exit(code)
	}
}

func runRemoteExec(name string, args []string) (int, error) {
	args, err := remoteCommandArgs(args)
	if err != nil {
		return 2, err
	}
	config, err := session.LoadUserConfig()
	if err != nil {
		return 1, err
	}
	rc, ok := config.Remotes[name]
	if !ok {
		return 1, fmt.Errorf("remote %q not found", name)
	}
	args, input, closeInput, err := remoteMessageInput(args)
	if err != nil {
		return 1, err
	}
	defer closeInput()
	runner := session.NewSSHRunner(name, rc)
	interactive, err := preflightRemoteCreationMode(context.Background(), runner, args)
	if err != nil {
		return 2, err
	}
	if interactive {
		if err := runner.RunInteractiveCreation(args...); err != nil {
			return 1, err
		}
		return 0, nil
	}
	if err := runner.RunIO(context.Background(), input, os.Stdout, os.Stderr, args...); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
			return exitErr.ExitCode(), nil // SSH already forwarded the diagnostic.
		}
		return 1, err
	}
	return 0, nil
}

// The public CLI and TUI negotiate the same owner-host flag catalog. Exact
// help/catalog requests are read-only and do not depend on a newer binary.
type remoteCreationCatalogRunner interface {
	FetchCreationCatalog(context.Context) (*session.RemoteCreationCatalog, error)
}

func preflightRemoteCreation(ctx context.Context, runner remoteCreationCatalogRunner, args []string) error {
	_, err := preflightRemoteCreationMode(ctx, runner, args)
	return err
}

func preflightRemoteCreationMode(ctx context.Context, runner remoteCreationCatalogRunner, args []string) (bool, error) {
	if len(args) == 0 || (args[0] != "add" && args[0] != "launch") {
		return false, nil
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-help" || args[1] == "-h") {
		return false, nil
	}
	if len(args) == 3 && args[1] == "--capabilities" && args[2] == "--json" {
		return false, nil
	}
	catalog, err := runner.FetchCreationCatalog(ctx)
	if err != nil {
		return false, err
	}
	if err := catalog.ValidateArgs(args); err != nil {
		return false, err
	}
	attachValue, _ := catalog.FlagValue(args, "attach")
	attach, _ := strconv.ParseBool(attachValue)
	if attach {
		jsonValue, _ := catalog.FlagValue(args, "json")
		jsonOutput, _ := strconv.ParseBool(jsonValue)
		if jsonOutput {
			return false, fmt.Errorf("--attach cannot be combined with --json; no remote session was created")
		}
		if !stdinStdoutIsTerminal() {
			return false, fmt.Errorf("--attach requires an interactive terminal; no remote session was created")
		}
	}
	return attach, nil
}
