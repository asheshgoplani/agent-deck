package issuereport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/childenv"
	"github.com/asheshgoplani/agent-deck/internal/feedback"
	"github.com/asheshgoplani/agent-deck/internal/platform"
)

// ErrCode classifies a failure for callers that report it as data (the
// --json CLI output the Mac app reads).
type ErrCode string

const (
	ErrInvalidInput  ErrCode = "invalid_input"
	ErrGhFailed      ErrCode = "gh_failed"
	ErrBrowserFailed ErrCode = "browser_failed"
	ErrCancelled     ErrCode = "cancelled"
)

// Error is the error type File and Validate return.
type Error struct {
	Code    ErrCode
	Message string
	// URL is the prefilled browser URL when one exists, so a caller can
	// still offer "open in browser" after gh or the opener failed.
	URL string
	Err error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// CodeOf returns the ErrCode carried by err, or "" when there is none.
func CodeOf(err error) ErrCode {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// FileResult says how the issue was filed. Method is "gh" (URL is the
// created issue) or "browser" (URL is the prefilled new-issue page the
// user submits themselves).
type FileResult struct {
	Method string
	URL    string
	// Opened is true when File opened URL in a browser. A "browser"
	// result with Opened false means the caller must show URL so the
	// user can open it (OpenBrowser was false, or no browser can open
	// on this machine).
	Opened bool
}

// FileOptions tunes File.
type FileOptions struct {
	// OpenBrowser opens the prefilled URL when gh is not usable. False
	// returns the URL only (the --json CLI path: the caller opens it).
	OpenBrowser bool
	// Opener opens a URL; nil uses the platform opener (open / xdg-open).
	Opener func(url string) error
}

// noBrowser reports that no browser can be opened from this process: a
// Linux/BSD host without DISPLAY/WAYLAND_DISPLAY, or an SSH session
// (platform.IsHeadless). macOS and Windows have no DISPLAY variable but can
// always open a URL, so they never count as headless here. A var so tests
// can pin it.
var noBrowser = func() bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return false
	}
	return platform.IsHeadless()
}

// BrowserAvailable reports whether File, asked to OpenBrowser, would try to
// open one here (false on a headless or SSH host). Front ends use it to
// say up front how the browser fallback will go.
func BrowserAvailable() bool { return !noBrowser() }

var issueURLPattern = regexp.MustCompile(`https://github\.com/[^\s/]+/[^\s/]+/issues/\d+`)

const ghTimeout = 60 * time.Second

func ghEnv() []string {
	return append(childenv.ForLaunch(""), "GH_PROMPT_DISABLED=1", "NO_COLOR=1", "GH_NO_UPDATE_NOTIFIER=1")
}

// GhReady reports whether a gh binary is on PATH and `gh auth status`
// succeeds, i.e. whether File would use gh.
func GhReady(ctx context.Context) bool {
	gh, err := exec.LookPath("gh")
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, gh, "auth", "status", "--hostname", "github.com") // #nosec G204 -- fixed args
	cmd.Env = ghEnv()
	return cmd.Run() == nil
}

// File files is on GitHub. Call it only after the user saw is (Issue.Text)
// and confirmed. With gh on PATH and logged in it runs
// `gh issue create --repo Repo --title T --body-file - --label L...` and
// returns the created issue URL; otherwise it returns (and, with
// OpenBrowser on a machine that can show a browser, opens) the prefilled
// new-issue URL. On a headless or SSH host it does not try to open one:
// FileResult.Opened stays false and the caller shows the URL.
func File(ctx context.Context, is Issue, opts FileOptions) (FileResult, error) {
	if strings.TrimSpace(is.Title) == "" || strings.TrimSpace(is.Body) == "" {
		return FileResult{}, &Error{Code: ErrInvalidInput, Message: "issue title and body must not be empty"}
	}
	if err := ctx.Err(); err != nil {
		return FileResult{}, &Error{Code: ErrCancelled, Message: "cancelled before filing", Err: err}
	}
	fallback, _ := NewIssueURL(is)

	if GhReady(ctx) {
		gh, _ := exec.LookPath("gh")
		args := []string{"issue", "create", "--repo", Repo, "--title", is.Title, "--body-file", "-"}
		for _, l := range is.Labels {
			args = append(args, "--label", l)
		}
		cctx, cancel := context.WithTimeout(ctx, ghTimeout)
		defer cancel()
		cmd := exec.CommandContext(cctx, gh, args...) // #nosec G204 -- gh with our own args
		cmd.Env = ghEnv()
		cmd.Stdin = strings.NewReader(is.Body)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		runErr := cmd.Run()
		if ctx.Err() != nil {
			return FileResult{}, &Error{Code: ErrCancelled, Message: "cancelled while filing with gh", URL: fallback, Err: ctx.Err()}
		}
		if runErr != nil {
			home, _ := os.UserHomeDir()
			msg := strings.TrimSpace(Scrub(firstLine(strings.TrimSpace(stderr.String())), home))
			if msg == "" {
				msg = runErr.Error()
			}
			return FileResult{}, &Error{Code: ErrGhFailed, Message: "gh issue create failed: " + msg, URL: fallback, Err: runErr}
		}
		u := issueURLPattern.FindString(stdout.String())
		if u == "" {
			return FileResult{}, &Error{Code: ErrGhFailed, Message: "gh issue create printed no issue URL", URL: fallback}
		}
		return FileResult{Method: "gh", URL: u}, nil
	}

	if opts.OpenBrowser && !noBrowser() {
		open := opts.Opener
		if open == nil {
			open = feedback.NewSender().BrowserCmd
		}
		if err := open(fallback); err != nil {
			return FileResult{}, &Error{Code: ErrBrowserFailed, Message: fmt.Sprintf("could not open a browser; open this URL yourself: %s", fallback), URL: fallback, Err: err}
		}
		return FileResult{Method: "browser", URL: fallback, Opened: true}, nil
	}
	return FileResult{Method: "browser", URL: fallback}, nil
}
