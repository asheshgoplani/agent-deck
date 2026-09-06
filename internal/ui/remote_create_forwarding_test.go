package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The new-session dialog opened on a remote target (#1353) collects the same
// fields as for a local session, but the remote-create path used to read only
// tool/title/path/group and dropped the rest: account slot, model, Claude
// toggles, sandbox and worktree never reached the remote's `add`. These tests
// drive the real dialog and capture what the submit handler forwards.

type remoteCreateCapture struct {
	calls      int
	remoteName string
	opts       session.RemoteAddOptions
}

func (c *remoteCreateCapture) sink(remoteName string, opts session.RemoteAddOptions) tea.Cmd {
	c.calls++
	c.remoteName, c.opts = remoteName, opts
	return nil
}

// openRemoteDialogAndTypeName opens the dialog via `n` on a remote group,
// selects the tool and types a session name, leaving focus on the Name field.
func openRemoteDialogAndTypeName(t *testing.T, remoteName, tool, name string) (*Home, *remoteCreateCapture) {
	t.Helper()
	setXDGTestHome(t)
	home := NewHome()
	home.width = 100
	home.height = 30
	home.flatItems = []session.Item{remoteGroupItem(remoteName)}
	home.cursor = 0
	capture := &remoteCreateCapture{}
	home.remoteCreateSink = capture.sink

	h := pressN(t, home)
	if !h.newDialog.IsVisible() {
		t.Fatal("precondition: n on a remote group must open the dialog")
	}
	h.newDialog.SetDefaultTool(tool)
	if got := h.newDialog.GetSelectedCommand(); got != tool {
		t.Fatalf("precondition: selected tool = %q, want %q", got, tool)
	}
	for _, r := range name {
		h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return h, capture
}

func submitRemoteDialog(t *testing.T, h *Home) *Home {
	t.Helper()
	model, _ := h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	home := model.(*Home)
	if home.newDialog.IsVisible() {
		t.Fatalf("submit must close the dialog; dialog error = %q", home.newDialog.validationErr)
	}
	return home
}

// submitRemoteDialogExpectingError submits and asserts the dialog stayed open
// with an error naming the refused field, and that nothing was forwarded.
func submitRemoteDialogExpectingError(t *testing.T, h *Home, capture *remoteCreateCapture, want string) {
	t.Helper()
	model, _ := h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	home := model.(*Home)
	if !home.newDialog.IsVisible() {
		t.Fatal("a refused field must keep the dialog open so the user sees why")
	}
	if !strings.Contains(home.newDialog.validationErr, want) {
		t.Fatalf("dialog error = %q, want it to mention %q", home.newDialog.validationErr, want)
	}
	if capture.calls != 0 {
		t.Fatalf("remote create called %d times for a refused field, want 0", capture.calls)
	}
	if home.pendingRemoteName == "" {
		t.Fatal("the remote target must survive a refused submit so a corrected retry still goes to the remote")
	}
}

// An untouched dialog forwards only tool, title, path and group: the remote
// applies its own defaults, exactly as before this change.
func TestRemoteDialog_UntouchedOptions_ForwardOnlyBasics(t *testing.T) {
	h, capture := openRemoteDialogAndTypeName(t, "myserver", "claude", "plain-task")

	h = submitRemoteDialog(t, h)

	if capture.calls != 1 {
		t.Fatalf("remote create called %d times, want 1", capture.calls)
	}
	want := session.RemoteAddOptions{Tool: "claude", Title: "plain-task", Path: ".", Group: session.DefaultGroupPath}
	if capture.remoteName != "myserver" {
		t.Fatalf("remoteName = %q, want myserver", capture.remoteName)
	}
	if got := capture.opts; got.Tool != want.Tool || got.Title != want.Title || got.Path != want.Path || got.Group != want.Group ||
		got.Sandbox || got.Account != "" || got.Model != "" || len(got.ExtraArgs) != 0 || got.Yolo || got.WorktreeBranch != "" || got.ResumeSessionID != "" {
		t.Fatalf("opts = %+v, want only the basics %+v", got, want)
	}
	if len(h.instances) != 0 {
		t.Fatalf("local create must not run for remote targets; got %d instances", len(h.instances))
	}
}

// This machine's [claude] defaults (dangerous mode is on by default, plus any
// default_model or extra_args) must not leak into a remote create: the dialog
// starts from the server's defaults when the target is a remote.
func TestRemoteDialog_LocalConfigDefaults_NotForwarded(t *testing.T) {
	setXDGTestHome(t)
	home := NewHome()
	home.width = 100
	home.height = 30
	home.flatItems = []session.Item{remoteGroupItem("myserver")}
	home.cursor = 0
	capture := &remoteCreateCapture{}
	home.remoteCreateSink = capture.sink

	h := pressN(t, home)
	h.newDialog.SetDefaultTool("claude")
	if h.newDialog.claudeOptions.skipPermissions {
		t.Fatal("remote dialog must open with the skip-permissions toggle off (server default), not this machine's default")
	}
	if h.newDialog.GetLaunchModelID() != "" || len(h.newDialog.GetClaudeExtraArgs()) != 0 {
		t.Fatal("remote dialog must open without a local default model or extra args")
	}
}

// The account slot picked in the options panel is forwarded by name; the
// server resolves it against its own config.toml.
func TestRemoteDialog_AccountSlot_Forwarded(t *testing.T) {
	h, capture := openRemoteDialogAndTypeName(t, "myserver", "claude", "acct-task")
	h.newDialog.claudeOptions.SetAccounts([]string{"alice", "bob"})
	h.newDialog.claudeOptions.SetAccount("bob")

	submitRemoteDialog(t, h)

	if capture.opts.Account != "bob" {
		t.Fatalf("account = %q, want bob", capture.opts.Account)
	}
}

// Model, effort and the Claude toggles reach the remote as the same flags a
// local session would launch with.
func TestRemoteDialog_ClaudeOptions_Forwarded(t *testing.T) {
	h, capture := openRemoteDialogAndTypeName(t, "myserver", "claude", "opts-task")
	h.newDialog.modelInput.SetValue("opus")
	h.newDialog.reasoningEffort = "high"
	h.newDialog.claudeOptions.skipPermissions = true
	h.newDialog.claudeOptions.useChrome = true
	h.newDialog.claudeOptions.SetExtraArgs([]string{"--agent", "reviewer"})

	submitRemoteDialog(t, h)

	if capture.opts.Model != "opus" {
		t.Fatalf("model = %q, want opus", capture.opts.Model)
	}
	got := strings.Join(capture.opts.ExtraArgs, " ")
	for _, want := range []string{"--effort high", "--dangerously-skip-permissions", "--chrome", "--agent reviewer"} {
		if !strings.Contains(got, want) {
			t.Fatalf("extra args = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "--model") {
		t.Fatalf("extra args = %q: model must travel as the first-class --model flag, not as an extra arg", got)
	}
}

// Resume mode with an id becomes --resume-session on the server.
func TestRemoteDialog_ResumeSession_Forwarded(t *testing.T) {
	h, capture := openRemoteDialogAndTypeName(t, "myserver", "claude", "resume-task")
	h.newDialog.claudeOptions.SetFromOptions(&session.ClaudeOptions{SessionMode: "resume", ResumeSessionID: "abc-123"})

	submitRemoteDialog(t, h)

	if capture.opts.ResumeSessionID != "abc-123" {
		t.Fatalf("resume id = %q, want abc-123", capture.opts.ResumeSessionID)
	}
	if strings.Contains(strings.Join(capture.opts.ExtraArgs, " "), "--resume") {
		t.Fatalf("extra args = %v: a resume with an id must not also add --resume", capture.opts.ExtraArgs)
	}
}

// Sandbox and worktree checkboxes travel too; the worktree is created on the
// server's copy of the repository.
func TestRemoteDialog_SandboxAndWorktree_Forwarded(t *testing.T) {
	h, capture := openRemoteDialogAndTypeName(t, "myserver", "claude", "wt-task")
	h.newDialog.ToggleSandbox()
	h.newDialog.ToggleWorktree()
	if branch := strings.TrimSpace(h.newDialog.branchInput.Value()); branch == "" {
		t.Fatal("precondition: ToggleWorktree must auto-fill the branch from the name")
	}

	submitRemoteDialog(t, h)

	if !capture.opts.Sandbox {
		t.Fatal("sandbox checkbox was enabled in the dialog but not forwarded")
	}
	if !strings.HasSuffix(capture.opts.WorktreeBranch, "wt-task") {
		t.Fatalf("worktree branch = %q, want one derived from the session name", capture.opts.WorktreeBranch)
	}
}

// Codex and Gemini YOLO checkboxes map to the remote's --yolo flag.
func TestRemoteDialog_CodexYolo_Forwarded(t *testing.T) {
	h, capture := openRemoteDialogAndTypeName(t, "myserver", "codex", "yolo-task")
	h.newDialog.codexOptions.SetDefaults(true)

	submitRemoteDialog(t, h)

	if capture.opts.Tool != "codex" || !capture.opts.Yolo {
		t.Fatalf("opts = %+v, want codex with yolo", capture.opts)
	}
}

// Fields the remote `add` cannot express are refused with a visible message
// instead of being dropped.
func TestRemoteDialog_UnforwardableFields_Refused(t *testing.T) {
	t.Run("startup query", func(t *testing.T) {
		h, capture := openRemoteDialogAndTypeName(t, "myserver", "claude", "query-task")
		h.newDialog.claudeOptions.SetStartQuery("fix the build")
		submitRemoteDialogExpectingError(t, h, capture, "Startup query")
	})
	t.Run("multi-repo", func(t *testing.T) {
		h, capture := openRemoteDialogAndTypeName(t, "myserver", "claude", "multi-task")
		h.newDialog.ToggleMultiRepo()
		h.newDialog.multiRepoPaths = []string{"/srv/a", "/srv/b"}
		submitRemoteDialogExpectingError(t, h, capture, "cannot be created on a remote")
	})
	t.Run("codex reasoning effort", func(t *testing.T) {
		h, capture := openRemoteDialogAndTypeName(t, "myserver", "codex", "effort-task")
		h.newDialog.reasoningEffort = "high"
		submitRemoteDialogExpectingError(t, h, capture, "Reasoning effort")
	})
	t.Run("hermes yolo", func(t *testing.T) {
		h, capture := openRemoteDialogAndTypeName(t, "myserver", "hermes", "hermes-task")
		h.newDialog.hermesOptions.SetDefaults(true)
		submitRemoteDialogExpectingError(t, h, capture, "Hermes YOLO")
	})
}

// remoteClaudeExtraArgs is the exact translation of the panel's toggles.
func TestRemoteClaudeExtraArgs(t *testing.T) {
	cases := []struct {
		name string
		opts *session.ClaudeOptions
		want []string
	}{
		{name: "nil", opts: nil, want: nil},
		{name: "new session with nothing on", opts: &session.ClaudeOptions{SessionMode: "new"}, want: nil},
		{
			name: "model is stripped, the rest is emitted",
			opts: &session.ClaudeOptions{Model: "opus", Effort: "low", SkipPermissions: true, UseTeammateMode: true},
			want: []string{"--effort", "low", "--dangerously-skip-permissions", "--teammate-mode", "tmux"},
		},
		{name: "auto mode", opts: &session.ClaudeOptions{AutoMode: true}, want: []string{"--permission-mode", "auto"}},
		{name: "continue", opts: &session.ClaudeOptions{SessionMode: "continue"}, want: []string{"-c"}},
		{name: "bare resume asks the server's picker", opts: &session.ClaudeOptions{SessionMode: "resume"}, want: []string{"--resume"}},
		{name: "resume with id travels elsewhere", opts: &session.ClaudeOptions{SessionMode: "resume", ResumeSessionID: "x"}, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := remoteClaudeExtraArgs(tc.opts)
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("remoteClaudeExtraArgs(%+v) = %q, want %q", tc.opts, got, tc.want)
			}
		})
	}
}

// The two sandbox tests below keep the names from PR #2127 (Djeeteg007), which
// first forwarded the checkbox; they now run through RemoteAddOptions.
func TestRemoteDialog_SandboxCheckbox_ForwardedToRemoteCreate(t *testing.T) {
	h, capture := openRemoteDialogAndTypeName(t, "myserver", "claude", "sandboxed-task")
	if h.newDialog.IsSandboxEnabled() {
		t.Fatal("precondition: remote dialog must start with the sandbox checkbox off")
	}
	h.newDialog.ToggleSandbox()
	if !h.newDialog.IsSandboxEnabled() {
		t.Fatal("precondition: ToggleSandbox must enable the checkbox")
	}

	h = submitRemoteDialog(t, h)

	if capture.calls != 1 {
		t.Fatalf("remote create called %d times, want 1", capture.calls)
	}
	if capture.remoteName != "myserver" {
		t.Fatalf("remoteName = %q, want myserver", capture.remoteName)
	}
	if capture.opts.Title != "sandboxed-task" {
		t.Fatalf("title = %q, want sandboxed-task", capture.opts.Title)
	}
	if !capture.opts.Sandbox {
		t.Fatal("sandbox checkbox was enabled in the dialog but not forwarded to the remote create")
	}
	if len(h.instances) != 0 {
		t.Fatalf("local create must not run for remote targets; got %d instances", len(h.instances))
	}
}

func TestRemoteDialog_SandboxUnchecked_NotForwarded(t *testing.T) {
	h, capture := openRemoteDialogAndTypeName(t, "myserver", "claude", "plain-task")

	submitRemoteDialog(t, h)

	if capture.calls != 1 {
		t.Fatalf("remote create called %d times, want 1", capture.calls)
	}
	if capture.opts.Sandbox {
		t.Fatal("sandbox must stay off when the checkbox was not enabled")
	}
	if capture.opts.Title != "plain-task" {
		t.Fatalf("title = %q, want plain-task", capture.opts.Title)
	}
}
