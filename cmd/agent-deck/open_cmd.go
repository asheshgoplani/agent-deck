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
	"github.com/asheshgoplani/agent-deck/internal/filelinks"
	"github.com/asheshgoplani/agent-deck/internal/macapp"
	"github.com/asheshgoplani/agent-deck/internal/openpages"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

const openUsage = "Usage: agent-deck open <file|url> [--session <id|title>] [--json] [--browser-profile <name>] [--wait <duration>]\n" +
	"       agent-deck open --last --session <id|title> [--json]\n" +
	"       agent-deck open --list --session <id|title> --json\n" +
	"       agent-deck open --show <page-id> --session <id|title> [--json]"
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
		var written errOpenJSONWritten
		if errors.As(err, &written) {
			if written.print {
				fmt.Fprintf(os.Stderr, "Error: open: %v\n", err)
			}
			os.Exit(written.exit)
		}
		fmt.Fprintf(os.Stderr, "Error: open: %v\n", err)
		os.Exit(sessionRefExitCode(err))
	}
}

func runOpen(profile string, args []string, out, errOut io.Writer) error {
	started := time.Now()
	fs := flag.NewFlagSet("open", flag.ContinueOnError)
	fs.SetOutput(helpOutput(args, out, errOut))
	identifier := fs.String("session", "", "session id or title (default: $AGENTDECK_INSTANCE_ID, then the calling tmux session)")
	browserProfile := fs.String("browser-profile", "", "Browser panel profile in the app (optional; letters, digits, '.', '_', '-', at most 64, no leading '.')")
	wait := fs.Duration("wait", openAckTimeout, "how long to wait for the app to acknowledge before reporting Queued (at most 2m0s)")
	jsonOut := fs.Bool("json", false, "print one JSON object: {status, where, session, title, path, url, page_id, page_title, name, kind, phone, elapsed_ms}")
	last := fs.Bool("last", false, "open the newest page this session opened again (exit 2 when it has none)")
	list := fs.Bool("list", false, "list the pages this session opened, newest first (with --json)")
	show := fs.String("show", "", "print one opened page as a self-contained HTML document (--json: with metadata)")
	fs.Usage = func() { fmt.Fprintln(fs.Output(), openUsage); fs.PrintDefaults() }
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	modes := 0
	for _, on := range []bool{*last, *list, *show != ""} {
		if on {
			modes++
		}
	}
	if modes > 1 {
		return errors.New("use only one of --last, --list and --show")
	}
	if modes == 1 && fs.NArg() != 0 || modes == 0 && fs.NArg() != 1 {
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
	fail := func(err error, code string) error {
		if *jsonOut {
			_ = json.NewEncoder(out).Encode(map[string]string{"error": err.Error(), "code": code})
			return errOpenJSONWritten{err: err, exit: openExitFor(code)}
		}
		if code == "no_page" {
			return errOpenJSONWritten{err: err, exit: 2, print: true}
		}
		return err
	}
	profile, err := session.ResolveProfileForStorage(profile)
	if err != nil {
		return fail(err, "error")
	}
	inst, err := resolveCallingSession(profile, *identifier, os.Getenv("AGENTDECK_INSTANCE_ID"), currentTmuxSessionName)
	if err != nil {
		return fail(err, "session_not_found")
	}
	switch {
	case *list:
		return writeOpenPageList(out, profile, inst, *jsonOut, fail)
	case *show != "":
		return writeOpenPageShow(out, profile, inst, *show, *jsonOut, fail)
	}
	target := ""
	if *last {
		page, err := openpages.LastExisting(profile, inst.ID)
		if err != nil {
			if errors.Is(err, openpages.ErrNotFound) {
				return fail(errors.New("no page opened for this session"), "no_page")
			}
			return fail(err, "error")
		}
		target = page.Path
		if page.Kind == openpages.KindURL {
			target = page.URL
		}
	} else {
		target = fs.Arg(0)
	}
	req, err := newOpenRequest(inst.ID, target)
	if err != nil {
		return fail(err, openTargetErrorCode(err))
	}
	req.BrowserProfile = *browserProfile
	bus := events.OpenProfile(profile)
	defer bus.Close()
	acknowledged, err := publishOpenRequest(bus, req, *wait, func(req MacappOpenRequest) { launchLocalMacapp(profile, req) })
	if err != nil {
		return fail(err, "error")
	}
	page, phone := recordOpenedPage(profile, bus, inst, req, errOut)
	if !*jsonOut {
		// The text answer is unchanged for scripts; where the page went
		// (phone, outside the roots) is in --json.
		_, err = fmt.Fprintln(out, openResultMessage(inst.Title, acknowledged))
		return err
	}
	return json.NewEncoder(out).Encode(openResultJSON(inst, req, page, phone, acknowledged, time.Since(started)))
}

// Values of the "phone" field of `open --json`.
const (
	openPhoneShown   = "shown"
	openPhoneURL     = "url"
	openPhoneOutside = "outside_roots"
	openPhonePrivate = "private"
	openPhoneKind    = "unsupported"
	openPhoneLarge   = "too_large"
	openPhoneError   = "error"
)

// phonePolicyCode names why a file stays off the phone.
func phonePolicyCode(err error) string {
	switch {
	case errors.Is(err, openpages.ErrPrivate):
		return openPhonePrivate
	case errors.Is(err, openpages.ErrUnsupported):
		return openPhoneKind
	case errors.Is(err, openpages.ErrTooLarge), errors.Is(err, filelinks.ErrTooLarge):
		return openPhoneLarge
	}
	return openPhoneOutside
}

// errOpenJSONWritten carries an exit code for a refusal already printed.
type errOpenJSONWritten struct {
	err   error
	exit  int
	print bool
}

func (e errOpenJSONWritten) Error() string { return e.err.Error() }

func openExitFor(code string) int {
	if code == "no_page" {
		return 2
	}
	return 1
}

func openTargetErrorCode(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "not_found"
	}
	return "invalid_target"
}

// openResult is the `open --json` object (the voice desk codes against it).
type openResult struct {
	Status    string `json:"status"`
	Where     string `json:"where"`
	Session   string `json:"session"`
	Title     string `json:"title"`
	Path      string `json:"path"`
	URL       string `json:"url"`
	PageID    string `json:"page_id"`
	PageTitle string `json:"page_title"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Phone     string `json:"phone"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

func openResultJSON(inst *session.InstanceData, req MacappOpenRequest, page openpages.Page, phone string, acknowledged bool, elapsed time.Duration) openResult {
	r := openResult{Status: "queued", Session: inst.ID, Title: inst.Title, Path: req.Path, URL: req.URL, PageID: page.ID, PageTitle: page.Title, Name: page.Name, Kind: page.Kind, Phone: phone, ElapsedMS: elapsed.Milliseconds()}
	if acknowledged {
		r.Status = "opened"
	}
	onPhone := phone == openPhoneShown || phone == openPhoneURL
	switch {
	case onPhone && acknowledged:
		r.Where = "both"
	case onPhone:
		r.Where = "phone"
	default:
		r.Where = "mac"
	}
	if r.Name == "" && req.Path != "" {
		// Mac-only (outside the roots): still name the page for the caller.
		r.Name = filepath.Base(req.Path)
		r.PageTitle = r.Name
	}
	if r.Kind == "" {
		if req.URL != "" {
			r.Kind = openpages.KindURL
		} else {
			r.Kind = openpages.KindOf(req.Path)
		}
	}
	return r
}

// openPageRoots are the roots a session's pages are read under on the
// phone path: [web] file_link_roots plus the session's own project path.
func openPageRoots(inst *session.InstanceData) []string {
	return filelinks.WithProjectRoot(session.GetWebFileLinkRoots(), inst.ProjectPath)
}

// recordOpenedPage puts the request on the session's page list for the
// phone when the file is under the session's roots (URLs always), and
// commits page.opened. The Mac path never depends on it: a failure here is
// reported, not fatal.
func recordOpenedPage(profile string, bus *events.Bus, inst *session.InstanceData, req MacappOpenRequest, errOut io.Writer) (openpages.Page, string) {
	p := openpages.Page{SessionID: inst.ID, Path: req.Path, URL: req.URL, EventID: req.EventID}
	phone := openPhoneURL
	if req.URL != "" {
		p.Kind = openpages.KindURL
	} else {
		// The whole phone policy (roots, private names, kind, size), the
		// same check --show and the web route run again at every read.
		real, kind, err := openpages.CheckPage(openPageRoots(inst), req.Path)
		if err != nil {
			return openpages.Page{}, phonePolicyCode(err)
		}
		p.Path, p.Kind = real, kind
		phone = openPhoneShown
		p.Title = openpages.TitleOf(openPageRoots(inst), p.Path, p.Kind)
	}
	page, err := openpages.Record(profile, p)
	if err != nil {
		fmt.Fprintf(errOut, "Warning: open: the page list for the phone was not updated: %v\n", err)
		return openpages.Page{}, openPhoneError
	}
	bus.Publish(events.KindPageOpened, inst.ID, map[string]any{
		"page_id": page.ID, "kind": page.Kind, "title": page.Title, "name": page.Name,
		"url": page.URL, "opened_at": page.OpenedAt.Format(time.RFC3339Nano), "host": req.Host,
	})
	bus.Flush(openCommitTimeout)
	return page, phone
}

// openPageListJSON is `open --list --json`.
type openPageListJSON struct {
	Session string           `json:"session"`
	Title   string           `json:"title"`
	Pages   []openpages.Page `json:"pages"`
}

func writeOpenPageList(out io.Writer, profile string, inst *session.InstanceData, jsonOut bool, fail func(error, string) error) error {
	pages, err := openpages.List(profile, inst.ID)
	if err != nil {
		return fail(err, "error")
	}
	for i := range pages {
		pages[i] = pages[i].ForPhone() // no host paths to the phone
	}
	if !jsonOut {
		for _, p := range pages {
			if _, err := fmt.Fprintf(out, "%s  %-8s  %s  (%s)\n", p.ID, p.Kind, p.Title, p.Name); err != nil {
				return err
			}
		}
		return nil
	}
	return json.NewEncoder(out).Encode(openPageListJSON{Session: inst.ID, Title: inst.Title, Pages: pages})
}

// openPageShowJSON is `open --show <id> --json`.
type openPageShowJSON struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	HTML       string `json:"html"`
	Bytes      int    `json:"bytes"`
	Inlined    int    `json:"inlined"`
	Skipped    int    `json:"skipped"`
	ModifiedAt string `json:"modified_at"`
}

func writeOpenPageShow(out io.Writer, profile string, inst *session.InstanceData, pageID string, jsonOut bool, fail func(error, string) error) error {
	page, err := openpages.Get(profile, inst.ID, pageID)
	if err != nil {
		return fail(err, "not_found")
	}
	all, _ := openpages.List(profile, inst.ID)
	r, err := openpages.RenderLinked(openPageRoots(inst), page, openpages.Links(all))
	if err != nil {
		return fail(err, pageRenderErrorCode(err))
	}
	if !jsonOut {
		if page.Kind == openpages.KindURL {
			_, err = fmt.Fprintln(out, page.URL)
			return err
		}
		_, err = out.Write(r.HTML)
		return err
	}
	resp := openPageShowJSON{ID: page.ID, Kind: page.Kind, Title: r.Title, Name: page.Name, URL: page.URL, HTML: string(r.HTML), Bytes: len(r.HTML), Inlined: r.Inlined, Skipped: r.Skipped}
	if !r.ModifiedAt.IsZero() {
		resp.ModifiedAt = r.ModifiedAt.Format(time.RFC3339)
	}
	return json.NewEncoder(out).Encode(resp)
}

func pageRenderErrorCode(err error) string {
	switch {
	case errors.Is(err, openpages.ErrTooLarge), errors.Is(err, filelinks.ErrTooLarge):
		return "too_large"
	case errors.Is(err, openpages.ErrPrivate), errors.Is(err, openpages.ErrUnsupported):
		// The gateway and the phone already explain outside_roots as
		// "not shareable with the phone"; the message says which rule.
		return "outside_roots"
	case errors.Is(err, filelinks.ErrOutside), errors.Is(err, filelinks.ErrNotRegular), errors.Is(err, filelinks.ErrDisabled), errors.Is(err, filelinks.ErrNotAbsolute):
		return "outside_roots"
	case errors.Is(err, filelinks.ErrNotFound):
		return "not_found"
	}
	return "error"
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

// sessionRefError is a session reference that did not resolve to one
// session: ResolveSession's message with its code (ErrCodeNotFound or
// ErrCodeAmbiguous), for commands that return errors instead of exiting.
type sessionRefError struct {
	msg  string
	code string
}

func (e *sessionRefError) Error() string { return e.msg }

// sessionRefExitCode is the exit code of a failed command: 2 when its error
// is a session that was not found, as for every `session` verb, else 1.
func sessionRefExitCode(err error) int {
	var refErr *sessionRefError
	if errors.As(err, &refErr) && refErr.code == ErrCodeNotFound {
		return 2
	}
	return 1
}

// readOpenSession resolves identifier with ResolveSession, the resolver of
// every `session` verb, so open, browser and ask take the same names and
// answer with the same words.
// Metadata only: no tmux reconnection, migration, or writes to the session DB.
func readOpenSession(profile, identifier string) (*session.InstanceData, error) {
	instances, err := loadOpenSessions(profile)
	if err != nil {
		return nil, err
	}
	// ResolveSession reads only these fields; the shells are never started
	// or saved.
	shells := make([]*session.Instance, 0, len(instances))
	byID := make(map[string]*session.InstanceData, len(instances))
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		shells = append(shells, &session.Instance{
			ID:            inst.ID,
			Title:         inst.Title,
			ProjectPath:   inst.ProjectPath,
			IsConductor:   inst.IsConductor,
			ConductorName: inst.ConductorName,
			SSHHost:       inst.SSHHost,
			SSHRemotePath: inst.SSHRemotePath,
		})
		byID[inst.ID] = inst
	}
	match, errMsg, errCode := ResolveSession(identifier, shells)
	if match == nil {
		return nil, &sessionRefError{msg: errMsg, code: errCode}
	}
	return byID[match.ID], nil
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
				// A page this box serves can still reach the app as files,
				// bundled over the app's own connection to the box.
				return "", "", fmt.Errorf("%w; to show a page from this host, open its file instead: agent-deck open <file>", err)
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
