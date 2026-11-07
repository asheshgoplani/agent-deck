package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

const switchUsage = "Usage: agent-deck switch <query> [--json] [--no-attach] [--wait <duration>]"

// switchAckTimeout is the default wait for the app's macapp.switch.ack.
const switchAckTimeout = 2 * time.Second

// Exit codes beyond 0 (switched, attached or queued) and 1 (error).
const (
	switchExitNotFound  = 2
	switchExitAmbiguous = 3
)

// Statuses and routes of the --json result (docs/switch.md).
const (
	switchStatusSwitched  = "switched"
	switchStatusQueued    = "queued"
	switchStatusAttached  = "attached"
	switchStatusAmbiguous = "ambiguous"
	switchStatusNotFound  = "not_found"

	switchViaApp      = "app"
	switchViaTerminal = "terminal"
	switchViaNone     = "none"
)

// switchResult is the --json output.
type switchResult struct {
	Status     string            `json:"status"`
	Query      string            `json:"query"`
	Session    *switchTarget     `json:"session,omitempty"`
	Via        string            `json:"via"`
	Candidates []switchCandidate `json:"candidates,omitempty"`
	ElapsedMs  int64             `json:"elapsed_ms"`
}

// MacappSwitchRequest is the data of a macapp.switch frame; the frame's
// top-level session_id is the session to show.
type MacappSwitchRequest struct {
	RequestID   string `json:"request_id"`
	RequestedAt string `json:"requested_at"`
	Host        string `json:"host"`
}

// switchDelivery is how a resolved switch reaches the app or the terminal.
// The funcs are injected so the decision is testable without open(1), an
// events bus or tmux.
type switchDelivery struct {
	// localMac: the CLI runs on the Mac itself (localMacappLaunchAllowed).
	localMac bool
	// openApp opens agentdeck://focus?session=<id> with /usr/bin/open.
	openApp func(id string) error
	// publish writes a macapp.switch frame and waits up to wait for its ack.
	// focused reports an ack with focused=true; err means nothing was queued.
	publish func(id string, wait time.Duration) (focused bool, err error)
	// canAttach: stdin and stdout are a terminal and neither --json nor
	// --no-attach was given.
	canAttach bool
	// attach shows the session in this terminal (switch-client inside tmux).
	attach func(id string) error
}

// deliverSwitch returns the status and route of a switch to id: the URL
// scheme on the Mac, then the events-bus frame, then the terminal.
func deliverSwitch(d switchDelivery, id string, wait time.Duration) (status, via string, err error) {
	if d.localMac && d.openApp != nil && d.openApp(id) == nil {
		return switchStatusSwitched, switchViaApp, nil
	}
	var publishErr error
	if d.publish != nil {
		focused, err := d.publish(id, wait)
		if err == nil && focused {
			return switchStatusSwitched, switchViaApp, nil
		}
		publishErr = err
	} else {
		publishErr = errors.New("no events bus")
	}
	if d.canAttach && d.attach != nil {
		if err := d.attach(id); err != nil {
			return "", "", err
		}
		return switchStatusAttached, switchViaTerminal, nil
	}
	if publishErr != nil {
		return "", "", publishErr
	}
	return switchStatusQueued, switchViaNone, nil
}

// switchEnv is what runSwitch needs from the outside world.
type switchEnv struct {
	load     func() ([]switchTarget, error)
	delivery func(jsonOut, noAttach bool) switchDelivery
	now      func() time.Time
}

func handleSwitch(profile string, args []string) {
	resolved, err := session.ResolveProfileForStorage(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: switch: %v\n", err)
		os.Exit(1)
	}
	env := switchEnv{
		load: func() ([]switchTarget, error) {
			instances, err := loadOpenSessions(resolved)
			if errors.Is(err, fs.ErrNotExist) {
				return nil, nil // no state yet: no sessions, so not_found
			}
			if err != nil {
				return nil, err
			}
			return switchTargets(instances), nil
		},
		delivery: func(jsonOut, noAttach bool) switchDelivery {
			return switchDelivery{
				localMac:  localMacappLaunchAllowed(runtime.GOOS, os.Getenv),
				openApp:   openMacappFocus,
				publish:   func(id string, wait time.Duration) (bool, error) { return publishSwitchRequest(resolved, id, wait) },
				canAttach: switchCanAttach(jsonOut, noAttach, stdinStdoutIsTerminal),
				attach:    func(id string) error { return attachSwitchTarget(resolved, id) },
			}
		},
		now: time.Now,
	}
	os.Exit(runSwitch(args, os.Stdout, os.Stderr, env))
}

// switchCanAttach: the terminal fallback only without --json and
// --no-attach, and only on a terminal (never probed under --json).
func switchCanAttach(jsonOut, noAttach bool, isTerminal func() bool) bool {
	return !jsonOut && !noAttach && isTerminal()
}

// runSwitch parses args, resolves the query and delivers the switch. It
// returns the exit code. With --json it never prompts or attaches.
func runSwitch(args []string, out, errOut io.Writer, env switchEnv) int {
	start := env.now()
	fs := flag.NewFlagSet("switch", flag.ContinueOnError)
	fs.SetOutput(helpOutput(args, out, errOut))
	jsonOut := fs.Bool("json", false, "print one JSON object; never attaches in the terminal")
	noAttach := fs.Bool("no-attach", false, "do not attach in this terminal when the app is not reached")
	wait := fs.Duration("wait", switchAckTimeout, "how long to wait for the app to acknowledge (at most 2m0s)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), switchUsage)
		fmt.Fprintln(fs.Output(), "\nShow a session in the AgentDeck app (or this terminal). <query> is an id, an id prefix, a title or part of one; group names count too.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	fail := func(err error) int {
		fmt.Fprintf(errOut, "Error: switch: %v\n", err)
		return 1
	}
	if fs.NArg() != 1 {
		return fail(errors.New(switchUsage))
	}
	if *wait < 0 {
		return fail(errors.New("--wait must not be negative"))
	}
	if *wait > openMaxWait {
		return fail(fmt.Errorf("--wait must be at most %s", openMaxWait))
	}
	query := fs.Arg(0)
	targets, err := env.load()
	if err != nil {
		return fail(fmt.Errorf("loading sessions: %w", err))
	}
	res := switchResult{Query: query, Via: switchViaNone}
	emit := func(code int) int {
		res.ElapsedMs = env.now().Sub(start).Milliseconds()
		if *jsonOut {
			if res.Status == switchStatusQueued {
				fmt.Fprintf(errOut, "AgentDeck not reached; to open it here: agent-deck session attach %s\n", res.Session.ID)
			}
			if err := json.NewEncoder(out).Encode(res); err != nil {
				return 1
			}
			return code
		}
		printSwitchText(out, errOut, res)
		return code
	}
	m := matchSwitchTarget(query, targets)
	switch m.Kind {
	case switchMatchNone:
		res.Status = switchStatusNotFound
		return emit(switchExitNotFound)
	case switchMatchAmbiguous:
		res.Status = switchStatusAmbiguous
		res.Candidates = m.Candidates
		return emit(switchExitAmbiguous)
	}
	target := m.Target
	res.Session = &target
	d := env.delivery(*jsonOut, *noAttach)
	if d.canAttach && !*jsonOut {
		// The attach takes the terminal over, so say where it goes first.
		d.attach = withAttachNotice(d.attach, out, target.Title)
	}
	res.Status, res.Via, err = deliverSwitch(d, target.ID, *wait)
	if err != nil {
		return fail(err)
	}
	if res.Status == switchStatusAttached && !*jsonOut {
		return 0
	}
	return emit(0)
}

func withAttachNotice(attach func(string) error, out io.Writer, title string) func(string) error {
	return func(id string) error {
		fmt.Fprintf(out, "AgentDeck not reached; attaching %s here\n", title)
		return attach(id)
	}
}

func printSwitchText(out, errOut io.Writer, res switchResult) {
	switch res.Status {
	case switchStatusSwitched:
		fmt.Fprintf(out, "Switched to %s in AgentDeck\n", res.Session.Title)
	case switchStatusQueued:
		fmt.Fprintf(out, "Queued for AgentDeck: it shows %s when the app is connected\n", res.Session.Title)
		fmt.Fprintf(errOut, "To open it here: agent-deck session attach %s\n", res.Session.ID)
	case switchStatusAmbiguous:
		fmt.Fprintf(out, "%q matches more than one session; which one?\n", res.Query)
		for _, c := range res.Candidates {
			group := ""
			if c.Group != "" {
				group = " (" + c.Group + ")"
			}
			fmt.Fprintf(out, "  %s%s  %s  %s\n", c.Title, group, c.ID, c.Status)
		}
	case switchStatusNotFound:
		fmt.Fprintf(errOut, "No session matches %q (agent-deck list shows them all)\n", res.Query)
	}
}

// openMacappFocus asks LaunchServices to show the session in the app. A
// plain open (no -g) brings the app to the front.
func openMacappFocus(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/bin/open", macappFocusLink(id)).Run() //nolint:gosec // G204: fixed binary, no shell; the one argv element is "agentdeck://focus?" plus url.Values.Encode output, so it can never be read as an open(1) flag
}

// macappFocusLink matches the app's SessionFocusLink parser.
func macappFocusLink(id string) string {
	return "agentdeck://focus?" + url.Values{"session": {id}}.Encode()
}

// publishSwitchRequest writes a macapp.switch frame on the profile's events
// bus and waits up to wait for the app's macapp.switch.ack with the same
// request_id. The frame stays for an app that connects later.
func publishSwitchRequest(profile, id string, wait time.Duration) (bool, error) {
	host, err := os.Hostname()
	if err != nil {
		return false, fmt.Errorf("reading the host name: %w", err)
	}
	req := MacappSwitchRequest{RequestID: events.NewID(), RequestedAt: time.Now().UTC().Format(time.RFC3339Nano), Host: host}
	bus := events.OpenProfile(profile)
	defer bus.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before := bus.Stats()
	if !before.Enabled {
		return false, errors.New("events bus is disabled; the switch was not queued")
	}
	sub, err := bus.Subscribe(ctx, before.Cursor)
	if err != nil {
		return false, err
	}
	if bus.PublishWithID(events.KindMacappSwitch, id, req) == "" || !bus.Flush(openCommitTimeout) || bus.Stats().Written <= before.Written {
		return false, errors.New("events bus did not commit the switch; it was not queued")
	}
	deadline := time.AfterFunc(wait, cancel)
	defer deadline.Stop()
	for frame := range sub.Frames() {
		if matched, focused := switchAcknowledged(frame, req.RequestID); matched {
			return focused, nil
		}
	}
	return false, nil
}

// switchAcknowledged reports whether frame is the app's ack of requestID and
// whether the app focused the session.
func switchAcknowledged(frame events.Frame, requestID string) (matched, focused bool) {
	if frame.Kind != events.KindMacappSwitchAck {
		return false, false
	}
	var ack struct {
		RequestID string `json:"request_id"`
		Focused   bool   `json:"focused"`
		Error     string `json:"error"`
	}
	if json.Unmarshal(frame.Data, &ack) != nil || requestID == "" || ack.RequestID != requestID {
		return false, false
	}
	return true, ack.Focused && ack.Error == ""
}

// attachSwitchTarget shows the session in this terminal: switch-client
// when already inside tmux, else the interactive attach of `session attach`.
func attachSwitchTarget(profile, id string) error {
	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		return err
	}
	defer storage.Close()
	inst := findFocusInstance(instances, id)
	if inst == nil {
		return fmt.Errorf("session %q not found", id)
	}
	if !inst.Exists() {
		return fmt.Errorf("session %q is not running; start it with: agent-deck session start %s", inst.Title, inst.ID)
	}
	ts := inst.GetTmuxSession()
	if ts == nil {
		return fmt.Errorf("no tmux session for %q", inst.Title)
	}
	if os.Getenv("TMUX") != "" {
		if _, err := tmux.OutputBounded(inst.TmuxSocketName, "switch-client", "-t", ts.Name); err != nil {
			return fmt.Errorf("switching this tmux client to %q: %w", inst.Title, err)
		}
		return nil
	}
	return attachInstanceInteractive(inst)
}
