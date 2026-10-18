package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/macapp"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/termtext"
)

const browserUsage = `Usage: agent-deck browser <op> [flags]

Drive the calling session's own Browser panel in the AgentDeck Mac app.

Ops:
  open <url|file> [--tab ID | --new-tab] [--browser-profile NAME]
  tabs
  close --tab ID
  click (--ref R | --selector S | --text T) [--tab ID]
  type (--ref R | --selector S) --text T [--submit] [--tab ID]
  read [--outline] [--tab ID]
  shot [--full] [--out FILE] [--tab ID]

Common flags:
  --session <id|title>  default $AGENTDECK_INSTANCE_ID, then the calling tmux session
  --json                print the app's response
  --timeout <duration>  default 30s (120s for click and type)

Off until two opt-ins are on (neither is a security boundary):
  agent-deck config set browser.enabled true                  ([browser] enabled, every profile)
  agent-deck config set profiles.<name>.browser.enabled true  (one profile only)
  agent-deck config set macapp.plugins true --yes             ([macapp] plugins)

Exit codes: 0 ok, 1 error, 2 browser.enabled off (JSON code BROWSER_DISABLED),
3 not_connected (no answer in time), 4 owner_driving.
Details: docs/macapp-browser.md`

const (
	browserExitOK           = 0
	browserExitError        = 1
	browserExitDisabled     = 2 // [browser] enabled is off for the profile
	browserExitNotConnected = 3
	browserExitOwnerDriving = 4
)

// browserOpenQueueTTL is how long a queued open stays valid for an app that
// connects later. Interactive ops expire at their CLI timeout.
const browserOpenQueueTTL = 24 * time.Hour

var errBrowserNoAnswer = errors.New("no answer from AgentDeck")

type browserFlags struct {
	session, tab, browserProfile, ref, selector, text, out string
	newTab, submit, outline, full, json                    bool
	timeout                                                time.Duration
}

func handleBrowser(profile string, args []string) {
	if code := runBrowser(profile, args, os.Stdout, os.Stderr); code != browserExitOK {
		os.Exit(code)
	}
}

func runBrowser(profile string, args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, browserUsage)
		return browserExitError
	}
	switch args[0] {
	case "help", "-h", "-help", "--help":
		fmt.Fprintln(out, browserUsage)
		return browserExitOK
	}
	op := macapp.Op(args[0])
	if !op.Valid() {
		fmt.Fprintf(errOut, "Error: browser: unknown op %q\n%s\n", args[0], browserUsage)
		return browserExitError
	}
	code, err := runBrowserOp(profile, op, args[1:], out, errOut)
	if errors.Is(err, errBrowserDisabled) && wantsJSON(args) {
		_ = writeBrowserJSON(out, map[string]any{"success": false, "error": err.Error(), "code": ErrCodeBrowserDisabled})
		return code
	}
	if err != nil {
		fmt.Fprintf(errOut, "Error: browser %s: %v\n", op, err)
	}
	return code
}

func runBrowserOp(profile string, op macapp.Op, args []string, out, errOut io.Writer) (int, error) {
	fs := flag.NewFlagSet("browser "+string(op), flag.ContinueOnError)
	fs.SetOutput(helpOutput(args, out, errOut))
	var f browserFlags
	registerBrowserFlags(fs, op, &f)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), browserUsage); fs.PrintDefaults() }
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return browserExitOK, nil
		}
		return browserExitError, err
	}
	wantArgs := 0
	if op == macapp.OpOpen {
		wantArgs = 1
	}
	if fs.NArg() != wantArgs {
		return browserExitError, errors.New("unexpected arguments; see agent-deck browser --help")
	}
	if f.timeout < 0 {
		return browserExitError, errors.New("--timeout must not be negative")
	}
	timeout := f.timeout
	if timeout == 0 {
		timeout = browserDefaultTimeout(op)
	}
	req, err := newBrowserRequest(op, f, fs.Arg(0), timeout)
	if err != nil {
		return browserExitError, err
	}
	profile, err = session.ResolveProfileForStorage(profile)
	if err != nil {
		return browserExitError, err
	}
	// Two opt-ins, neither a security boundary: an agent on this host
	// controls its config and the log. The Mac app enforces the policy.
	if err := requireBrowserEnabled(profile); err != nil {
		return browserExitDisabled, err
	}
	if err := requireBrowserOptIn(); err != nil {
		return browserExitError, err
	}
	inst, err := resolveCallingSession(profile, f.session, os.Getenv("AGENTDECK_INSTANCE_ID"), currentTmuxSessionName)
	if err != nil {
		return browserExitError, err
	}
	shotPath := ""
	if op == macapp.OpShot {
		if shotPath, err = browserShotPath(f.out, inst.ID, req.ReqID); err != nil {
			return browserExitError, err
		}
	}

	if dir, err := browserResultsDir(profile); err == nil {
		sweepBrowserResults(dir, time.Now())
	}
	bus := events.OpenProfile(profile)
	defer bus.Close()
	resp, err := publishBrowserRequest(bus, inst.ID, req, timeout)
	if errors.Is(err, errBrowserNoAnswer) {
		if op == macapp.OpOpen {
			_, err = fmt.Fprintln(out, openQueuedMessage)
			return browserExitOK, err
		}
		return browserExitNotConnected, fmt.Errorf("%s: no answer from AgentDeck within %s", macapp.CodeNotConnected, timeout)
	}
	if err != nil {
		return browserExitError, err
	}
	if err := checkBrowserResponseVersion(resp); err != nil {
		return browserExitError, err
	}
	if resp.ResultRef != nil {
		if resp.Result, err = loadBrowserResult(profile, resp.ResultRef); err != nil {
			return browserExitError, err
		}
		resp.ResultRef = nil
	}
	if !resp.OK {
		if f.json {
			_ = writeBrowserJSON(out, resp)
		}
		return browserExitCode(resp.Error), browserResponseError(resp.Error)
	}
	if err := writeBrowserResult(out, op, resp, shotPath, f.json); err != nil {
		return browserExitError, err
	}
	return browserExitOK, nil
}

func registerBrowserFlags(fs *flag.FlagSet, op macapp.Op, f *browserFlags) {
	fs.StringVar(&f.session, "session", "", "session id or title (default: $AGENTDECK_INSTANCE_ID, then the calling tmux session)")
	fs.BoolVar(&f.json, "json", false, "print the app's response as JSON")
	fs.DurationVar(&f.timeout, "timeout", 0, "how long to wait for the app (default 30s, 120s for click and type)")
	if op != macapp.OpTabs {
		fs.StringVar(&f.tab, "tab", "", "tab id")
	}
	switch op {
	case macapp.OpOpen:
		fs.BoolVar(&f.newTab, "new-tab", false, "open in a new tab")
		fs.StringVar(&f.browserProfile, "browser-profile", "", "Browser panel profile name (not the agent-deck profile)")
	case macapp.OpClick, macapp.OpType:
		fs.StringVar(&f.ref, "ref", "", "element ref from browser read --outline")
		fs.StringVar(&f.selector, "selector", "", "CSS selector")
		fs.StringVar(&f.text, "text", "", "click: visible text to click; type: text to type")
		if op == macapp.OpType {
			fs.BoolVar(&f.submit, "submit", false, "press Enter after typing")
		}
	case macapp.OpRead:
		fs.BoolVar(&f.outline, "outline", false, "return an outline with element refs instead of text")
	case macapp.OpShot:
		fs.BoolVar(&f.full, "full", false, "capture the full page")
		fs.StringVar(&f.out, "out", "", "image file (default: <data dir>/runtime/browser-shots/<session>/<req_id>.jpg)")
	}
}

func browserDefaultTimeout(op macapp.Op) time.Duration {
	if op == macapp.OpClick || op == macapp.OpType {
		return 120 * time.Second
	}
	return 30 * time.Second
}

func newBrowserRequest(op macapp.Op, f browserFlags, target string, timeout time.Duration) (macapp.BrowserRequest, error) {
	now := time.Now().UTC()
	req := macapp.BrowserRequest{
		V:           macapp.ProtocolVersion,
		Op:          op,
		RequestedAt: now,
		ExpiresAt:   now.Add(timeout),
	}
	req.Args = macapp.BrowserArgs{
		TabID: f.tab, NewTab: f.newTab, BrowserProfile: f.browserProfile,
		Ref: f.ref, Selector: f.selector, Text: f.text, Submit: f.submit, FullPage: f.full,
	}
	var err error
	switch op {
	case macapp.OpOpen:
		req.ExpiresAt = now.Add(browserOpenQueueTTL)
		if req.Args.Path, req.Args.URL, err = resolveOpenTarget(target); err != nil {
			return req, err
		}
	case macapp.OpRead:
		req.Args.Mode = macapp.ReadModeText
		if f.outline {
			req.Args.Mode = macapp.ReadModeOutline
		}
	}
	if req.Host, err = os.Hostname(); err != nil {
		return req, err
	}
	if req.ReqID, err = newBrowserReqID(); err != nil {
		return req, err
	}
	return req, req.Validate()
}

func newBrowserReqID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func browserShotPath(out, sessionID, reqID string) (string, error) {
	if out != "" {
		return filepath.Abs(out)
	}
	return agentpaths.EffectiveDataPath(filepath.Join("runtime", "browser-shots", sessionID, reqID+".jpg"), "runtime")
}

// publishBrowserRequest subscribes before it publishes, so an answer that
// arrives at once is not missed, then waits up to timeout (counted from the
// commit) for the first response matching req_id and the request event_id.
func publishBrowserRequest(bus *events.Bus, sessionID string, req macapp.BrowserRequest, timeout time.Duration) (macapp.BrowserResponse, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before := bus.Stats()
	if !before.Enabled {
		return macapp.BrowserResponse{}, errors.New("events bus is disabled; request was not sent")
	}
	sub, err := bus.Subscribe(ctx, before.Cursor)
	if err != nil {
		return macapp.BrowserResponse{}, err
	}
	eventID := bus.PublishWithID(macapp.KindBrowserRequest, sessionID, req)
	if eventID == "" || !bus.Flush(openCommitTimeout) || bus.Stats().Written <= before.Written {
		return macapp.BrowserResponse{}, errors.New("events bus did not confirm the request in time; it may not have been sent")
	}
	deadline := time.AfterFunc(timeout, cancel)
	defer deadline.Stop()
	for frame := range sub.Frames() {
		if resp, ok := browserResponseFor(frame, sessionID, req.ReqID, eventID); ok {
			return resp, nil
		}
	}
	if err := sub.Err(); err != nil {
		return macapp.BrowserResponse{}, fmt.Errorf("request sent but response stream failed: %w", err)
	}
	return macapp.BrowserResponse{}, errBrowserNoAnswer
}

func browserResponseFor(frame events.Frame, sessionID, reqID, eventID string) (macapp.BrowserResponse, bool) {
	var resp macapp.BrowserResponse
	if frame.Kind != macapp.KindBrowserResponse || frame.SessionID != sessionID || eventID == "" {
		return resp, false
	}
	if json.Unmarshal(frame.Data, &resp) != nil || resp.ReqID != reqID || resp.RequestEventID != eventID {
		return resp, false
	}
	return resp, true
}

func browserExitCode(e *macapp.ErrorInfo) int {
	if e != nil {
		switch e.Code {
		case macapp.CodeNotConnected:
			return browserExitNotConnected
		case macapp.CodeOwnerDriving:
			return browserExitOwnerDriving
		}
	}
	return browserExitError
}

func browserResponseError(e *macapp.ErrorInfo) error {
	if e == nil || e.Code == "" {
		return errors.New("AgentDeck refused the request")
	}
	if e.Message == "" {
		return errors.New(termtext.Clean(e.Code))
	}
	return fmt.Errorf("%s: %s", termtext.Clean(e.Code), termtext.Clean(e.Message))
}

func writeBrowserJSON(out io.Writer, v any) error {
	return json.NewEncoder(out).Encode(v)
}

// writeBrowserResult prints a successful response. A shot is always written
// to shotPath; with --json its result carries the path instead of the image.
func writeBrowserResult(out io.Writer, op macapp.Op, resp macapp.BrowserResponse, shotPath string, asJSON bool) error {
	switch op {
	case macapp.OpShot:
		return writeBrowserShot(out, resp, shotPath, asJSON)
	case macapp.OpRead:
		read, err := macapp.DecodeRead(resp.Result)
		if err != nil {
			return err
		}
		if asJSON {
			return writeBrowserJSON(out, resp)
		}
		// Page content is untrusted: no escape sequence reaches the pane.
		_, err = fmt.Fprintln(out, displayPageText(read.Text))
		return err
	}
	if asJSON {
		return writeBrowserJSON(out, resp)
	}
	switch op {
	case macapp.OpTabs:
		return writeBrowserTabs(out, resp.Result)
	case macapp.OpClose:
		_, err := fmt.Fprintln(out, "Closed")
		return err
	}
	var page macapp.PageResult
	if len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, &page); err != nil {
			return fmt.Errorf("%s result: %w", op, err)
		}
	}
	if op == macapp.OpOpen {
		_, err := fmt.Fprintf(out, "Opened %s (tab %s)\n", termtext.Clean(page.URL), termtext.Clean(page.TabID))
		return err
	}
	_, err := fmt.Fprintln(out, termtext.Clean(page.URL))
	return err
}

// writeBrowserShot saves the decoded image to shotPath and prints the path,
// or with asJSON the response whose result carries the path instead of the
// image.
func writeBrowserShot(out io.Writer, resp macapp.BrowserResponse, shotPath string, asJSON bool) error {
	shot, image, err := macapp.DecodeShot(resp.Result)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(shotPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(shotPath, image, 0o600); err != nil {
		return err
	}
	if !asJSON {
		_, err = fmt.Fprintln(out, shotPath)
		return err
	}
	result, err := json.Marshal(map[string]any{"mime": shot.MIME, "width": shot.Width, "height": shot.Height, "path": shotPath})
	if err != nil {
		return err
	}
	resp.Result = result
	return writeBrowserJSON(out, resp)
}

// writeBrowserTabs prints one line per tab, the active one marked "*",
// then the current driver when the app reports one.
func writeBrowserTabs(out io.Writer, raw json.RawMessage) error {
	var tabs macapp.TabsResult
	if err := json.Unmarshal(raw, &tabs); err != nil {
		return fmt.Errorf("tabs result: %w", err)
	}
	for _, tab := range tabs.Tabs {
		marker := " "
		if tab.Active {
			marker = "*"
		}
		if _, err := fmt.Fprintf(out, "%s %s\t%s\t%s\n", marker, termtext.Clean(tab.TabID), termtext.Clean(tab.URL), termtext.Clean(tab.Title)); err != nil {
			return err
		}
	}
	if tabs.Driver != "" {
		_, err := fmt.Fprintf(out, "driver: %s\n", termtext.Clean(tabs.Driver))
		return err
	}
	return nil
}

// errBrowserOff is returned while [macapp] plugins is off.
var errBrowserOff = errors.New("browser requests are off: set [macapp] plugins = true in config.toml. To enable it, run: agent-deck config set macapp.plugins true --yes (docs/macapp-browser.md)")

// requireBrowserOptIn refuses a browser request unless [macapp] plugins is
// on, the opt-in `events publish --kind macapp.browser.request` needs too.
func requireBrowserOptIn() error {
	cfg, _ := session.LoadUserConfig()
	if cfg == nil || !cfg.Macapp.Plugins {
		return errBrowserOff
	}
	return nil
}

// displayPageText makes page text safe to print: each line goes through
// termtext.Clean (controls incl. ESC/CSI/OSC, C1, DEL, bidi formatting and
// invisible separators dropped) and the line breaks stay. A tab becomes a
// space, since Clean drops tabs.
func displayPageText(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\t", " "), "\n")
	for i, line := range lines {
		lines[i] = termtext.Clean(strings.TrimSuffix(line, "\r"))
	}
	return strings.Join(lines, "\n")
}

// checkBrowserResponseVersion refuses a response in a protocol version this
// CLI does not speak.
func checkBrowserResponseVersion(resp macapp.BrowserResponse) error {
	if resp.V != macapp.ProtocolVersion {
		return fmt.Errorf("AgentDeck answered with protocol version %d; this agent-deck speaks %d", resp.V, macapp.ProtocolVersion)
	}
	return nil
}

// ErrCodeBrowserDisabled is the JSON code of a browser request refused while
// [browser] enabled is off for the profile.
const ErrCodeBrowserDisabled = "BROWSER_DISABLED"

// errBrowserDisabled wraps the refusal while [browser] enabled is off.
var errBrowserDisabled = errors.New("browser requests are off")

type browserDisabledError struct{ profile string }

func (e browserDisabledError) Error() string        { return session.BrowserDisabledMessage(e.profile) }
func (e browserDisabledError) Is(target error) bool { return target == errBrowserDisabled }

// requireBrowserEnabled refuses a browser request unless [browser] enabled
// (or [profiles.<profile>.browser] enabled) is on. Off by default.
func requireBrowserEnabled(profile string) error {
	cfg, _ := session.LoadUserConfig()
	if !session.ResolveBrowserEnabled(cfg, profile) {
		return browserDisabledError{profile: profile}
	}
	return nil
}
