package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

func validateRemoteEventsArgs(args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return nil
	}
	if len(args) > 0 && args[0] == "publish" {
		return validateRemoteEventsPublishArgs(args[1:])
	}
	if len(args) == 0 || args[0] != "follow" {
		return fmt.Errorf("unsupported remote command: events forwards follow and reply publish only")
	}
	// --profile selects another existing profile's log on the remote: the
	// global -p of a forwarded command is fixed by the remote's config.
	fs := map[string]bool{"json": false, "jsonl": false, "since": true, "after": true, "kind": true, "session": true, "read-only": false, "profile": true, "help": false, "h": false}
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

// validateRemoteTriggersArgs admits `triggers [list]` with the list's
// filter flags only: a closed, read-only option set.
func validateRemoteTriggersArgs(args []string) error {
	if len(args) > 0 && args[0] == "list" {
		args = args[1:]
	}
	options := map[string]bool{"json": false, "all": false, "session": true, "kind": true, "health": true, "help": false, "h": false}
	for i := 0; i < len(args); i++ {
		name, _, inline := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		takesValue, known := options[name]
		if !strings.HasPrefix(args[i], "-") || !known {
			return fmt.Errorf("unsupported remote command: triggers forwards the read-only list only (--session, --kind, --health, --all, --json), not %q", args[i])
		}
		if !takesValue && inline {
			return fmt.Errorf("option --%s takes no value", name)
		}
		if takesValue && !inline {
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
	return len(args) >= 2 && ((args[0] == "recall" && (args[1] == "timeline" || args[1] == "follow")) || (args[0] == "events" && args[1] == "follow") || (args[0] == "session" && args[1] == "send-status") || (args[0] == "file" && (args[1] == "read" || args[1] == "roots")))
}

type remoteReadRunner interface {
	RunReadIO(context.Context, io.Reader, io.Writer, io.Writer, []byte, bool, ...string) error
}

func runRemoteRead(ctx context.Context, runner remoteReadRunner, name string, input io.Reader, args []string) (int, error) {
	return runRemoteReadIO(ctx, runner, name, input, os.Stdout, os.Stderr, args)
}

func runRemoteReadIO(ctx context.Context, runner remoteReadRunner, name string, input io.Reader, stdout, stderr io.Writer, args []string) (int, error) {
	unsupported := remoteUnsupportedAnswer(name, args)
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

// remotePublishDataCap bounds the stdin payload of a forwarded events
// publish: a browser shot reply at macapp.MaxShotBytes (1,400,000 bytes,
// 1,866,668 in base64) fits with its envelope.
const remotePublishDataCap = 2 << 20

// remotePublishFromRemoteFlag is added to every forwarded publish. The box
// then applies its own reply-only checks (checkRemoteReplyPublish), and a box
// that predates them fails the capability probe on the unknown flag, so the
// controller reports "unsupported remote command" instead of the old box's
// own flag or gate error.
const remotePublishFromRemoteFlag = "--from-remote"

// validateRemoteEventsPublishArgs admits `events publish` with a closed
// option set: a reply kind, its data on stdin (--data-file -), and the
// routing options. Inline --data and controller file paths never travel.
// It is also the only judge of a help request: help is exactly
// `events publish --help` (or -h); option values never start with "-", so
// no -h can hide in a value (--session -h) and skip the payload cap.
func validateRemoteEventsPublishArgs(args []string) error {
	if remoteEventsPublishHelp(args) {
		return nil
	}
	options := map[string]bool{"kind": true, "session": true, "data-file": true, "profile": true, "json": false}
	values := map[string]string{}
	// Consume the options from the front: no index arithmetic (gosec G602).
	rest := args
	for len(rest) > 0 {
		arg := rest[0]
		rest = rest[1:]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			return fmt.Errorf("unsupported argument %q for remote events publish", arg)
		}
		name, value, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		takesValue, known := options[name]
		if !known {
			return fmt.Errorf("unsupported option %q for remote events publish", arg)
		}
		if !takesValue {
			if inline {
				return fmt.Errorf("option --%s takes no value", name)
			}
			continue
		}
		if !inline {
			if len(rest) == 0 {
				return fmt.Errorf("option --%s needs a value", name)
			}
			value = rest[0]
			rest = rest[1:]
		}
		if strings.HasPrefix(value, "-") && !(name == "data-file" && value == "-") {
			return fmt.Errorf("option --%s value %q must not start with \"-\"", name, value)
		}
		values[name] = value // Go flags use the last assignment.
	}
	if !events.IsMacappRemoteReplyKind(values["kind"]) {
		return fmt.Errorf("remote events publish forwards only %s, %s and %s; refused kind %q", events.KindMacappOpenAck, events.KindMacappBrowserResponse, events.KindMacappAppFocus, values["kind"])
	}
	if values["data-file"] != "-" {
		return fmt.Errorf("remote events publish reads its data from stdin: pass --data-file -")
	}
	return nil
}

// remoteEventsPublishHelp reports whether the options after `events publish`
// are a help request (the validator's own rule).
func remoteEventsPublishHelp(opts []string) bool {
	return len(opts) == 1 && (opts[0] == "--help" || opts[0] == "-h")
}

// isRemoteEventsPublish reports a forwarded publish that carries a payload
// (validated args, not a help request).
func isRemoteEventsPublish(args []string) bool {
	return len(args) >= 2 && args[0] == "events" && args[1] == "publish" && !remoteEventsPublishHelp(args[2:])
}

// readRemotePublishData reads the whole payload before SSH is contacted, so
// an oversized reply is refused without reaching the remote.
func readRemotePublishData(input io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(input, remotePublishDataCap+1))
	if err != nil {
		return nil, fmt.Errorf("read publish data: %w", err)
	}
	if len(data) > remotePublishDataCap {
		return nil, fmt.Errorf("publish data exceeds %d bytes", remotePublishDataCap)
	}
	return data, nil
}

type remotePublishRunner interface {
	RunPublishIO(context.Context, []byte, io.Writer, io.Writer, []byte, ...string) error
}

// runRemotePublishIO forwards a reply publish through the same capability
// negotiation as a remote read: the box first answers `<args> --help`, and a
// box that cannot (no `events publish`, no --from-remote) gets the clear
// "unsupported remote command" answer, in JSON with --json.
func runRemotePublishIO(ctx context.Context, runner remotePublishRunner, name string, payload []byte, stdout, stderr io.Writer, args []string) (int, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	forwarded := append(append([]string(nil), args...), remotePublishFromRemoteFlag)
	if err := runner.RunPublishIO(timeoutCtx, payload, stdout, stderr, remoteUnsupportedAnswer(name, args), forwarded...); err != nil {
		return 1, err
	}
	return 0, nil
}

// remoteUnsupportedAnswer is what a box that lacks a forwarded capability
// prints instead of running it (plain text, or JSON for --json/--jsonl).
func remoteUnsupportedAnswer(name string, args []string) []byte {
	msg := fmt.Sprintf("unsupported remote command %q on remote %q; update its agent-deck", strings.Join(args[:2], " "), name)
	if wantsJSON(args) || hasJSONL(args) {
		raw, _ := json.Marshal(map[string]string{"error": msg, "remote": name, "remote_version": remoteVersionUnknown})
		return append(raw, '\n')
	}
	return []byte(msg + "\n")
}
