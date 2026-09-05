package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// remoteCommandArgs accepts only commands with a meaningful server-side scope.
// In particular, a typo can never fall through into a local command handler.
func remoteCommandArgs(args []string) ([]string, error) {
	if len(args) > 0 {
		switch args[0] {
		case "list", "status", "add", "launch":
			return append([]string(nil), args...), nil
		case "show", "output", "send":
			return append([]string{"session"}, args...), nil
		case "session":
			if len(args) > 1 {
				switch args[1] {
				case "show", "output", "send", "start", "stop", "restart":
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
			if len(args) > 1 && args[1] == "attach" {
				return append([]string(nil), args...), nil
			}
		}
	}
	return nil, fmt.Errorf("unsupported remote command %q; run 'agent-deck remote' for supported commands", strings.Join(args, " "))
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
	boolOptions := " json quiet q no-wait wait stream draft defer-if-busy assert-done no-assert-done no-parent inherit-group no-transition-notify title-lock no-title-sync inherit-telegram-env b new-branch no-channel-link sandbox yolo gemini-yolo attach allow-repo-scripts "
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
	if err := runner.RunIO(context.Background(), input, os.Stdout, os.Stderr, args...); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
			return exitErr.ExitCode(), nil // SSH already forwarded the diagnostic.
		}
		return 1, err
	}
	return 0, nil
}
