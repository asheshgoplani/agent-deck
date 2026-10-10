package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func validateRemoteEventsArgs(args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return nil
	}
	if len(args) == 0 || args[0] != "follow" {
		return fmt.Errorf("unsupported remote command: events forwards follow only")
	}
	fs := map[string]bool{"json": false, "jsonl": false, "since": true, "after": true, "kind": true, "session": true, "help": false, "h": false}
	for i := 1; i < len(args); i++ {
		name, _, inline := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		value, known := fs[name]
		if !strings.HasPrefix(args[i], "-") || !known {
			return fmt.Errorf("unsupported option %q for remote events follow", args[i])
		}
		if !value && inline {
			return fmt.Errorf("option --%s takes no value", name)
		}
		if value && !inline {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				continue
			}
			return fmt.Errorf("option --%s needs a value", name)
		}
	}
	return nil
}

func isRemoteFollow(args []string) bool {
	return len(args) >= 2 && (args[0] == "recall" || args[0] == "events") && args[1] == "follow" && !remoteHelpRequested(args)
}

func remoteHelpRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func isRemoteReadCapability(args []string) bool {
	return len(args) >= 2 && ((args[0] == "recall" && (args[1] == "timeline" || args[1] == "follow")) || (args[0] == "events" && args[1] == "follow") || (args[0] == "session" && args[1] == "send-status"))
}

type remoteReadRunner interface {
	RunReadIO(context.Context, io.Reader, io.Writer, io.Writer, []byte, bool, ...string) error
}

func runRemoteRead(ctx context.Context, runner remoteReadRunner, name string, input io.Reader, args []string) (int, error) {
	return runRemoteReadIO(ctx, runner, name, input, os.Stdout, os.Stderr, args)
}

func runRemoteReadIO(ctx context.Context, runner remoteReadRunner, name string, input io.Reader, stdout, stderr io.Writer, args []string) (int, error) {
	msg := fmt.Sprintf("unsupported remote command %q on remote %q; update its agent-deck", strings.Join(args[:2], " "), name)
	unsupported := []byte(msg + "\n")
	if wantsJSON(args) || hasJSONL(args) {
		unsupported, _ = json.Marshal(map[string]string{"error": msg, "remote": name, "remote_version": remoteVersionUnknown})
		unsupported = append(unsupported, '\n')
	}
	follow := isRemoteFollow(args)
	if !follow {
		timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		ctx = timeoutCtx
	}
	err := runner.RunReadIO(ctx, input, stdout, stderr, unsupported, follow, args...)
	if ctx.Err() != nil && follow {
		return 0, nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

func hasJSONL(args []string) bool {
	for _, arg := range args {
		if arg == "--jsonl" || arg == "-jsonl" {
			return true
		}
	}
	return false
}
