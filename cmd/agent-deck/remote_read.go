package main

import (
	"bytes"
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

// remoteEventsPublishKinds are the only kinds a controller publishes on a
// remote: the Mac app's replies to a box session. Requests (in particular
// macapp.browser.request) are written on the box itself and never travel
// from a controller.
var remoteEventsPublishKinds = map[string]bool{
	events.KindMacappOpenAck:         true,
	events.KindMacappBrowserResponse: true,
	events.KindMacappAppFocus:        true,
}

// remotePublishDataCap bounds the stdin payload of a forwarded events
// publish (a browser response carrying a 1.5 MiB screenshot fits).
const remotePublishDataCap = 2 << 20

// validateRemoteEventsPublishArgs admits `events publish` with a closed
// option set: a reply kind, its data on stdin (--data-file -), and the
// routing options. Inline --data and controller file paths never travel.
func validateRemoteEventsPublishArgs(args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return nil
	}
	options := map[string]bool{"kind": true, "session": true, "data-file": true, "profile": true, "json": false}
	values := map[string]string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
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
			if i+1 == len(args) {
				return fmt.Errorf("option --%s needs a value", name)
			}
			i++
			value = args[i]
		}
		values[name] = value // Go flags use the last assignment.
	}
	if !remoteEventsPublishKinds[values["kind"]] {
		return fmt.Errorf("remote events publish forwards only %s, %s and %s; refused kind %q", events.KindMacappOpenAck, events.KindMacappBrowserResponse, events.KindMacappAppFocus, values["kind"])
	}
	if values["data-file"] != "-" {
		return fmt.Errorf("remote events publish reads its data from stdin: pass --data-file -")
	}
	return nil
}

func isRemoteEventsPublish(args []string) bool {
	return len(args) >= 2 && args[0] == "events" && args[1] == "publish" && !remoteHelpRequested(args)
}

// readRemotePublishData reads the whole payload before SSH is contacted, so
// an oversized reply is refused without reaching the remote.
func readRemotePublishData(input io.Reader) (io.Reader, error) {
	data, err := io.ReadAll(io.LimitReader(input, remotePublishDataCap+1))
	if err != nil {
		return nil, fmt.Errorf("read publish data: %w", err)
	}
	if len(data) > remotePublishDataCap {
		return nil, fmt.Errorf("publish data exceeds %d bytes", remotePublishDataCap)
	}
	return bytes.NewReader(data), nil
}
