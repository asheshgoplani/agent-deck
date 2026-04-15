package feedback

import (
	"os/exec"
	"runtime"

	"github.com/asheshgoplani/agent-deck/internal/clipboard"
	"github.com/asheshgoplani/agent-deck/internal/platform"
)

// DiscussionNodeID is the GitHub Discussion node ID for the Feedback Hub.
// Must be replaced with the real node ID before release.
// Retrieve after creating the Discussion at:
//
//	https://github.com/asheshgoplani/agent-deck/discussions
//
// via: gh api graphql -f query='{ repository(owner:"asheshgoplani",name:"agent-deck") { discussions(first:5) { nodes { id title } } } }'
const DiscussionNodeID = "D_kwDOQh82-s4Alt9V"

// DiscussionURL is the GitHub Discussions page for agent-deck feedback.
// Note: GitHub Discussions does NOT support ?body= URL parameter prefill
// (only GitHub Issues supports this). The browser fallback opens this URL
// and relies on the user pasting from clipboard into the Discussion form.
const DiscussionURL = "https://github.com/asheshgoplani/agent-deck/discussions"

// Sender holds the three-tier send mechanism for feedback submissions.
// All four function fields are injectable for testing.
type Sender struct {
	// GhCmd runs the gh CLI with the given arguments.
	// Real implementation: exec.Command("gh", args...).CombinedOutput().
	GhCmd func(args ...string) error

	// BrowserCmd opens the given URL in the default browser.
	// Real implementation: runtime.GOOS switch (open/xdg-open).
	BrowserCmd func(url string) error

	// ClipboardCmd copies the given text to the system clipboard.
	// Receives the formatted COMMENT BODY (not a URL).
	// Real implementation: clipboard.Copy(text, false).
	ClipboardCmd func(text string) error

	// IsHeadlessFunc returns true when no graphical display is available.
	// Real implementation: platform.IsHeadless().
	IsHeadlessFunc func() bool
}

// NewSender returns a *Sender with all four fields populated with real implementations.
func NewSender() *Sender {
	return &Sender{
		GhCmd: func(args ...string) error {
			_, err := exec.Command("gh", args...).CombinedOutput()
			return err
		},
		BrowserCmd: func(url string) error {
			var cmd *exec.Cmd
			switch runtime.GOOS {
			case "darwin":
				cmd = exec.Command("open", url)
			default:
				// Linux and other UNIX-like systems
				cmd = exec.Command("xdg-open", url)
			}
			return cmd.Run()
		},
		ClipboardCmd: func(text string) error {
			_, err := clipboard.Copy(text, false)
			return err
		},
		IsHeadlessFunc: func() bool {
			return platform.IsHeadless()
		},
	}
}

// Send submits feedback using a three-tier fallback chain:
//  1. gh CLI via GraphQL mutation (primary)
//  2. If gh fails and not headless: copy body to clipboard, then open Discussion URL in browser
//  3. If gh fails and headless: copy body to clipboard only (no browser)
//
// version, rating, goos, goarch, comment are used to build the formatted comment body
// via FormatComment. Send returns nil in all fallback cases — the caller should
// inform the user that clipboard/browser fallback was used.
func (s *Sender) Send(version string, rating int, goos, goarch, comment string) error {
	body := FormatComment(version, rating, goos, goarch, comment)

	const ghQuery = `mutation($id:ID!,$body:String!){addDiscussionComment(input:{discussionId:$id,body:$body}){comment{id}}}`
	err := s.GhCmd(
		"api", "graphql",
		"-f", "query="+ghQuery,
		"-f", "id="+DiscussionNodeID,
		"-f", "body="+body,
	)
	if err == nil {
		// gh CLI succeeded — comment posted to GitHub Discussion
		return nil
	}

	// gh failed (auth error, not installed, network error, etc.)
	// Fall through to clipboard + optional browser fallback.
	// All gh failures use the same fallback path regardless of exit code.

	if !s.IsHeadlessFunc() {
		// Non-headless: copy formatted comment body to clipboard, then open Discussion URL.
		// User pastes from clipboard into the GitHub Discussion form.
		if clipErr := s.ClipboardCmd(body); clipErr != nil {
			// Best-effort clipboard copy — ignore error, still try browser
			_ = clipErr
		}
		if browserErr := s.BrowserCmd(DiscussionURL); browserErr != nil {
			// Best-effort browser open — ignore error
			_ = browserErr
		}
		return nil
	}

	// Headless: clipboard only (no browser available)
	if clipErr := s.ClipboardCmd(body); clipErr != nil {
		// Best-effort clipboard copy — ignore error
		_ = clipErr
	}
	return nil
}
