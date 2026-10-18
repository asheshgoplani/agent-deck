package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/macapp"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

const openUsage = "Usage: agent-deck open <file|url> [--session <id|title>] [--browser-profile <name>] [--wait <duration>]"
const openAckTimeout = 3 * time.Second
const openQueuedMessage = "Queued for AgentDeck: it opens when the app is connected"

// openCommitTimeout bounds the wait for the request frame to reach disk. It is
// generous on purpose: reporting "not queued" for a frame the writer commits a
// moment later would be a false refusal, so only a stuck bus should hit it.
const openCommitTimeout = 10 * time.Second

// MacappOpenRequest is carried in the existing profile event bus. The app
// echoes the frame event_id as open_event_id with opened=true after display.
// RequestID (random hex, never caller-supplied) is an extra hint for the app
// to drop a request it already handled; event_id stays the dedupe key, and
// older CLIs send no request_id. BrowserProfile names an optional Browser
// panel profile in the app. It is not the agent-deck profile (-p/--profile,
// and the profile= query of the launch link).
type MacappOpenRequest struct {
	EventID        string `json:"-"`
	SessionID      string `json:"-"`
	Host           string `json:"host"`
	Path           string `json:"path,omitempty"`
	URL            string `json:"url,omitempty"`
	RequestedAt    string `json:"requested_at"`
	RequestID      string `json:"request_id"`
	BrowserProfile string `json:"browser_profile,omitempty"`
}

func handleOpen(profile string, args []string) {
	if err := runOpen(profile, args, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "Error: open: %v\n", err)
		os.Exit(1)
	}
}

func runOpen(profile string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("open", flag.ContinueOnError)
	fs.SetOutput(helpOutput(args, out, errOut))
	identifier := fs.String("session", "", "session id or title (default: $AGENTDECK_INSTANCE_ID, then the calling tmux session)")
	browserProfile := fs.String("browser-profile", "", "Browser panel profile in the app (optional; letters, digits, '.', '_', '-', at most 64, no leading '.')")
	wait := fs.Duration("wait", openAckTimeout, "how long to wait for the app to acknowledge before reporting Queued (at most 2m0s)")
	fs.Usage = func() { fmt.Fprintln(fs.Output(), openUsage); fs.PrintDefaults() }
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		return errors.New(openUsage)
	}
	if *wait < 0 {
		return errors.New("--wait must not be negative")
	}
	if *wait > openMaxWait {
		return fmt.Errorf("--wait must be at most %s", openMaxWait)
	}
	if *browserProfile != "" && !validBrowserProfileName(*browserProfile) {
		return fmt.Errorf("--browser-profile %q: use letters, digits, '.', '_' and '-' (at most %d, no leading '.')", truncateForError(*browserProfile), browserProfileMaxLen)
	}
	profile, err := session.ResolveProfileForStorage(profile)
	if err != nil {
		return err
	}
	inst, err := resolveCallingSession(profile, *identifier, os.Getenv("AGENTDECK_INSTANCE_ID"), currentTmuxSessionName)
	if err != nil {
		return err
	}
	req, err := newOpenRequest(inst.ID, fs.Arg(0))
	if err != nil {
		return err
	}
	req.BrowserProfile = *browserProfile
	bus := events.OpenProfile(profile)
	defer bus.Close()
	acknowledged, err := publishOpenRequest(bus, req, *wait, func(req MacappOpenRequest) { launchLocalMacapp(profile, req) })
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, openResultMessage(inst.Title, acknowledged))
	return err
}

func openSessionIdentifier(explicit, current string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if current != "" {
		return current, nil
	}
	return "", errors.New("--session is required outside an agent-deck session")
}

// resolveCallingSession is the one session resolver of open and browser:
// --session, then $AGENTDECK_INSTANCE_ID, then the agent-deck session that
// owns the calling tmux session.
func resolveCallingSession(profile, explicit, current string, tmuxName func() string) (*session.InstanceData, error) {
	if explicit != "" || current != "" {
		identifier, err := openSessionIdentifier(explicit, current)
		if err != nil {
			return nil, err
		}
		return readOpenSession(profile, identifier)
	}
	if name := tmuxName(); name != "" {
		instances, err := loadOpenSessions(profile)
		if err != nil {
			return nil, err
		}
		for _, inst := range instances {
			if inst.TmuxSession == name {
				return inst, nil
			}
		}
	}
	return nil, errors.New("--session is required outside an agent-deck session")
}

func currentTmuxSessionName() string {
	if os.Getenv("TMUX") == "" {
		return ""
	}
	name, err := getCurrentTmuxSessionName()
	if err != nil {
		return ""
	}
	return name
}

// Metadata only: no tmux reconnection, migration, or writes to the session DB.
func readOpenSession(profile, identifier string) (*session.InstanceData, error) {
	instances, err := loadOpenSessions(profile)
	if err != nil {
		return nil, err
	}
	// Exact IDs take precedence over titles, especially for the calling ID.
	for _, inst := range instances {
		if inst.ID == identifier {
			return inst, nil
		}
	}
	var match *session.InstanceData
	for _, inst := range instances {
		if inst.Title == identifier {
			if match != nil {
				return nil, fmt.Errorf("session title %q is ambiguous; use its id", identifier)
			}
			match = inst
		}
	}
	if match == nil {
		return nil, fmt.Errorf("session %q not found", identifier)
	}
	return match, nil
}

// loadOpenSessions reads session metadata only, like readOpenSession.
func loadOpenSessions(profile string) ([]*session.InstanceData, error) {
	storage, err := session.NewLiveReadOnlyStorageWithProfile(profile)
	if err != nil {
		return nil, err
	}
	defer storage.Close()
	instances, _, err := storage.LoadLite()
	return instances, err
}

func newOpenRequest(sessionID, target string) (MacappOpenRequest, error) {
	req := MacappOpenRequest{SessionID: sessionID, RequestedAt: time.Now().UTC().Format(time.RFC3339Nano), RequestID: events.NewID()}
	var err error
	req.Host, err = os.Hostname()
	if err != nil {
		return req, err
	}
	req.Path, req.URL, err = resolveOpenTarget(target)
	return req, err
}

// openCallerIsRemote reports whether this CLI runs on a host the Mac app
// reaches as a remote (not macOS, or inside an SSH session): the requests it
// writes are then "from a remote" in the app's policy.
var openCallerIsRemote = func() bool { return !localMacappLaunchAllowed(runtime.GOOS, os.Getenv) }

func resolveOpenTarget(target string) (path, webURL string, err error) {
	webURL, err = macapp.ValidateURL(target)
	if err == nil {
		// Defence in depth for the app's policy: a remote host never asks
		// for a loopback, private or link-local address (macapp.CheckRemoteURL).
		if openCallerIsRemote() {
			if err := macapp.CheckRemoteURL(webURL); err != nil {
				return "", "", err
			}
		}
		return "", webURL, nil
	}
	if !errors.Is(err, macapp.ErrNotURL) {
		return "", "", err
	}
	path, err = filepath.Abs(target)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", "", err
	}
	if info.IsDir() {
		path = filepath.Join(path, "index.html")
		info, err = os.Stat(path)
		if err != nil {
			return "", "", err
		}
	}
	if !info.Mode().IsRegular() {
		return "", "", errors.New("target must be a regular file or a directory containing index.html")
	}
	return path, "", nil
}

func publishOpenRequest(bus *events.Bus, req MacappOpenRequest, timeout time.Duration, launch func(MacappOpenRequest)) (bool, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before := bus.Stats()
	if !before.Enabled {
		return false, errors.New("events bus is disabled; request was not queued")
	}
	sub, err := bus.Subscribe(ctx, before.Cursor)
	if err != nil {
		return false, err
	}
	req.EventID = bus.PublishWithID(events.KindMacappOpen, req.SessionID, req)
	if req.EventID == "" || !bus.Flush(openCommitTimeout) || bus.Stats().Written <= before.Written {
		return false, errors.New("events bus did not commit the request; request was not queued")
	}
	// The acknowledgment window starts once the request is on disk, so a slow
	// commit never eats into the time the app has to answer.
	deadline := time.AfterFunc(timeout, cancel)
	defer deadline.Stop()
	// A connected app acknowledges during the deadline. Only an unacknowledged
	// local request needs the URL-scheme launch fallback; remote hosts never do.
	for frame := range sub.Frames() {
		if matched, err := openAcknowledged(frame, req); matched {
			return err == nil, err
		}
	}
	if err := sub.Err(); err != nil {
		return false, fmt.Errorf("request queued but acknowledgment stream failed: %w", err)
	}
	if launch != nil {
		launch(req)
	}
	return false, nil
}

func openAcknowledged(frame events.Frame, req MacappOpenRequest) (bool, error) {
	if frame.Kind != events.KindMacappOpenAck || frame.SessionID != req.SessionID {
		return false, nil
	}
	var ack struct {
		OpenEventID string `json:"open_event_id"`
		Opened      bool   `json:"opened"`
		Error       string `json:"error"`
	}
	if json.Unmarshal(frame.Data, &ack) != nil || req.EventID == "" || ack.OpenEventID != req.EventID {
		return false, nil
	}
	if !ack.Opened || ack.Error != "" {
		if ack.Error == "" {
			ack.Error = "the app did not open the page"
		}
		return true, fmt.Errorf("AgentDeck could not open the page: %s", ack.Error)
	}
	return true, nil
}

func openResultMessage(title string, acknowledged bool) string {
	if acknowledged {
		return fmt.Sprintf("Opened in AgentDeck (session %s)", title)
	}
	return openQueuedMessage
}

func localMacappLaunchAllowed(goos string, getenv func(string) string) bool {
	return goos == "darwin" && getenv("SSH_CONNECTION") == "" && getenv("SSH_CLIENT") == "" && getenv("SSH_TTY") == ""
}

func launchLocalMacapp(profile string, req MacappOpenRequest) {
	if !localMacappLaunchAllowed(runtime.GOOS, os.Getenv) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// The committed event remains the source of truth if LaunchServices has
	// no registered app. No shell interpolation or local action on SSH hosts.
	_ = exec.CommandContext(ctx, "/usr/bin/open", macappOpenLink(profile, req)).Run() //nolint:gosec // G204: fixed binary, no shell; the one argv element is "agentdeck://open?" plus url.Values.Encode output, so it can never be read as an open(1) flag
}

// Match the app's MacappOpenLink parser: session and event are query names,
// while the event frame uses session_id and event_id at its top level.
func macappOpenLink(profile string, req MacappOpenRequest) string {
	q := url.Values{"profile": {profile}, "session": {req.SessionID}, "host": {req.Host}, "event": {req.EventID}, "requested_at": {req.RequestedAt}}
	if req.Path != "" {
		q.Set("path", req.Path)
	} else {
		q.Set("url", req.URL)
	}
	return "agentdeck://open?" + q.Encode()
}

// openMaxWait caps --wait: the request stays queued for a later app anyway.
const openMaxWait = 120 * time.Second

// browserProfileMaxLen caps a Browser panel profile name.
const browserProfileMaxLen = macapp.MaxBrowserProfileLen

// validBrowserProfileName is the plain-name rule for --browser-profile:
// [A-Za-z0-9._-], no leading '.', at most browserProfileMaxLen bytes.
func validBrowserProfileName(name string) bool { return macapp.ValidBrowserProfile(name) }

// truncateForError keeps a refused value short in the error message.
func truncateForError(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}
