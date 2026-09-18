package ui

import (
	"errors"
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

var errUnavailable = errors.New("ssh: connect failed")

type remoteCreateCapture struct {
	calls      int
	remoteName string
	opts       session.RemoteAddOptions
	// accountsFetchedFor records the remotes whose account slots the dialog
	// asked for when it opened; the real fetch goes over SSH.
	accountsFetchedFor []string
	// mcpsFetchedFor is the same for the remote's MCP names.
	mcpsFetchedFor []string
}

func (c *remoteCreateCapture) sink(remoteName string, opts session.RemoteAddOptions) tea.Cmd {
	c.calls++
	c.remoteName, c.opts = remoteName, opts
	return nil
}

func (c *remoteCreateCapture) accountsFetcher(remoteName string) tea.Cmd {
	c.accountsFetchedFor = append(c.accountsFetchedFor, remoteName)
	return nil
}

func (c *remoteCreateCapture) mcpsFetcher(remoteName string) tea.Cmd {
	c.mcpsFetchedFor = append(c.mcpsFetchedFor, remoteName)
	return func() tea.Msg { return nil }
}

// newRemoteHome builds a Home whose cursor sits on the given remote item, with
// this machine's config.toml set to configTOML (empty for none) and the three
// SSH paths the dialog can reach (create, account fetch, MCP fetch) replaced
// by captures.
func newRemoteHome(t *testing.T, item session.Item, configTOML string) (*Home, *remoteCreateCapture) {
	t.Helper()
	home := setXDGTestHome(t)
	if configTOML != "" {
		writeXDGTestConfig(t, home, configTOML)
	}
	h := NewHome()
	h.width = 100
	h.height = 30
	h.flatItems = []session.Item{item}
	h.cursor = 0
	capture := &remoteCreateCapture{}
	h.remoteCreateSink = capture.sink
	h.remoteCreationCatalogFetcher = func(name string) tea.Cmd {
		capture.accountsFetcher(name)
		capture.mcpsFetcher(name)
		return func() tea.Msg { return nil }
	}
	return h, capture
}

// openRemoteDialogOn opens the dialog via `n` on the given remote item,
// selects the tool and types a session name, leaving focus on the Name field.
func openRemoteDialogOn(t *testing.T, item session.Item, configTOML, tool, name string) (*Home, *remoteCreateCapture) {
	t.Helper()
	home, capture := newRemoteHome(t, item, configTOML)

	h := pressN(t, home)
	if !h.newDialog.IsVisible() {
		t.Fatal("precondition: n on a remote item must open the dialog")
	}
	h.newDialog.SetRemoteCreationCatalog(remoteDialogTestCatalog())
	h.newDialog.SetDefaultTool(tool)
	if got := h.newDialog.GetSelectedCommand(); got != tool {
		t.Fatalf("precondition: selected tool = %q, want %q", got, tool)
	}
	for _, r := range name {
		h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return h, capture
}

// openRemoteDialogAndTypeName is openRemoteDialogOn for a remote group with
// no local config.toml.
func openRemoteDialogAndTypeName(t *testing.T, remoteName, tool, name string) (*Home, *remoteCreateCapture) {
	t.Helper()
	return openRemoteDialogOn(t, remoteGroupItem(remoteName), "", tool, name)
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

// An untouched dialog forwards only tool, title and path: opened from the
// remote's own root row the group is empty, so no -g flag reaches the
// remote's add command and the session lands at the remote's true top level
// instead of a local-only bucket.
func TestRemoteDialog_UntouchedOptions_ForwardOnlyBasics(t *testing.T) {
	h, capture := openRemoteDialogAndTypeName(t, "myserver", "claude", "plain-task")

	h = submitRemoteDialog(t, h)

	if capture.calls != 1 {
		t.Fatalf("remote create called %d times, want 1", capture.calls)
	}
	want := session.RemoteAddOptions{Tool: "claude", Title: "plain-task", Path: ".", Group: ""}
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
	home, _ := newRemoteHome(t, remoteGroupItem("myserver"), "")

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
	if got := strings.Join(capture.opts.ExtraArgs, " "); got != "--agent reviewer" {
		t.Fatalf("extra args = %q", got)
	}
	if capture.opts.ClaudeOptions == nil || !capture.opts.ClaudeOptions.SkipPermissions || !capture.opts.ClaudeOptions.UseChrome || capture.opts.ReasoningEffort != "high" {
		t.Fatalf("Claude choices lost: %+v", capture.opts)
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
func TestRemoteDialog_CreationParityFields_Forwarded(t *testing.T) {
	t.Run("startup query", func(t *testing.T) {
		h, capture := openRemoteDialogAndTypeName(t, "myserver", "claude", "query-task")
		h.newDialog.claudeOptions.SetStartQuery("fix the build")
		submitRemoteDialog(t, h)
		if capture.opts.StartQuery != "fix the build" {
			t.Fatalf("query lost: %+v", capture.opts)
		}
	})
	t.Run("multi-repo", func(t *testing.T) {
		h, capture := openRemoteDialogAndTypeName(t, "myserver", "claude", "multi-task")
		h.newDialog.ToggleMultiRepo()
		h.newDialog.multiRepoPaths = []string{"/srv/a", "/srv/b"}
		submitRemoteDialog(t, h)
		if capture.opts.Path != "/srv/a" || strings.Join(capture.opts.AdditionalPaths, ",") != "/srv/b" {
			t.Fatalf("paths lost: %+v", capture.opts)
		}
	})
	t.Run("codex reasoning effort", func(t *testing.T) {
		h, capture := openRemoteDialogAndTypeName(t, "myserver", "codex", "effort-task")
		h.newDialog.reasoningEffort = "high"
		submitRemoteDialog(t, h)
		if capture.opts.ReasoningEffort == "" {
			t.Fatalf("effort lost: %+v", capture.opts)
		}
	})
	t.Run("hermes yolo", func(t *testing.T) {
		h, capture := openRemoteDialogAndTypeName(t, "myserver", "hermes", "hermes-task")
		h.newDialog.hermesOptions.SetDefaults(true)
		submitRemoteDialog(t, h)
		if !capture.opts.Yolo {
			t.Fatalf("yolo lost: %+v", capture.opts)
		}
	})
}

// The dialog opened from a remote session item (not just the remote group
// header) keeps that session's group and path and forwards them after
// ResetRemoteDefaults has run.
func TestRemoteDialog_RemoteSessionItem_ForwardsGroupAndPath(t *testing.T) {
	rs := session.RemoteSessionInfo{ID: "remote-123", Title: "remote-session", RemoteName: "myserver", Group: "work", Path: "/srv/repo"}
	item := session.Item{Type: session.ItemTypeRemoteSession, RemoteSession: &rs, RemoteName: "myserver"}
	h, capture := openRemoteDialogOn(t, item, "", "claude", "sibling-task")

	submitRemoteDialog(t, h)

	if capture.calls != 1 || capture.remoteName != "myserver" {
		t.Fatalf("remote create called %d times for %q, want once for myserver", capture.calls, capture.remoteName)
	}
	if capture.opts.Group != "work" || capture.opts.Path != "/srv/repo" {
		t.Fatalf("opts = %+v, want the remote session's group work and path /srv/repo", capture.opts)
	}
	if capture.opts.Title != "sibling-task" || capture.opts.Tool != "claude" {
		t.Fatalf("opts = %+v, want title sibling-task with tool claude", capture.opts)
	}
}

// The worktree branch prefix is applied exactly once, by the remote. An
// auto-filled branch must not carry this machine's [worktree].branch_prefix,
// or the server (which applies its own) would create remote/local/name.
func TestRemoteDialog_WorktreeBranch_PrefixAppliedOnceByRemote(t *testing.T) {
	const controllerConfig = "[worktree]\nbranch_prefix = \"ctl/\"\n"
	remotePrefix := "srv/"
	remote := session.WorktreeSettings{BranchPrefix: &remotePrefix}

	t.Run("auto-filled branch travels as the bare slug", func(t *testing.T) {
		h, capture := openRemoteDialogOn(t, remoteGroupItem("myserver"), controllerConfig, "claude", "wt-task")
		h.newDialog.ToggleWorktree()
		if got := h.newDialog.branchInput.Value(); got != "wt-task" {
			t.Fatalf("auto-filled branch = %q, want the bare slug wt-task (no controller prefix)", got)
		}

		submitRemoteDialog(t, h)

		if capture.opts.WorktreeBranch != "wt-task" {
			t.Fatalf("forwarded branch = %q, want wt-task", capture.opts.WorktreeBranch)
		}
		if got := remote.ApplyBranchPrefix(capture.opts.WorktreeBranch); got != "srv/wt-task" {
			t.Fatalf("remote add -w %q creates %q, want srv/wt-task (remote prefix once, never ctl/)", capture.opts.WorktreeBranch, got)
		}
	})

	t.Run("typed branch is forwarded as entered", func(t *testing.T) {
		h, capture := openRemoteDialogOn(t, remoteGroupItem("myserver"), controllerConfig, "claude", "wt-task")
		h.newDialog.ToggleWorktree()
		h.newDialog.branchInput.SetValue("srv/hotfix-urgent")

		submitRemoteDialog(t, h)

		if capture.opts.WorktreeBranch != "srv/hotfix-urgent" {
			t.Fatalf("forwarded branch = %q, want the typed srv/hotfix-urgent", capture.opts.WorktreeBranch)
		}
		if got := remote.ApplyBranchPrefix(capture.opts.WorktreeBranch); got != "srv/hotfix-urgent" {
			t.Fatalf("remote add -w %q creates %q, want the typed name unchanged", capture.opts.WorktreeBranch, got)
		}
	})

	t.Run("local dialog keeps the controller prefix", func(t *testing.T) {
		h, _ := openRemoteDialogOn(t, remoteGroupItem("myserver"), controllerConfig, "claude", "wt-task")
		h.newDialog.Hide()
		h.newDialog.ShowInGroup(session.DefaultGroupPath, session.DefaultGroupName, t.TempDir(), nil, "")
		if h.newDialog.branchPrefix != "ctl/" {
			t.Fatalf("local dialog prefix = %q after a remote open, want ctl/", h.newDialog.branchPrefix)
		}
	})
}

// A Codex-compatible custom tool shows the same effort selector as codex, so
// a selected effort is refused with the same message rather than dropped.
func TestRemoteDialog_CodexCompatibleCustomTool_EffortForwarded(t *testing.T) {
	h, capture := openRemoteDialogAndTypeName(t, "myserver", "codex", "custom-effort")
	h.newDialog.presetCommands = append(h.newDialog.presetCommands, "mycodex")
	h.newDialog.remoteCatalog.Tools = append(h.newDialog.remoteCatalog.Tools, session.RemoteCreationTool{Name: "mycodex", Kind: "codex"})
	h.newDialog.SetDefaultTool("mycodex")
	h.newDialog.reasoningEffort = "high"
	h.newDialog.codexOptions.SetDefaults(true)
	submitRemoteDialog(t, h)
	if capture.opts.Tool != "mycodex" || capture.opts.ReasoningEffort != "high" || !capture.opts.Yolo {
		t.Fatalf("options lost: %+v", capture.opts)
	}
}

// The account row lists the target remote's slots, not this machine's: a
// slot configured only locally is never offered (the server would reject
// it), and one configured only on the server can be picked.
func TestRemoteDialog_AccountSlots_ComeFromRemote(t *testing.T) {
	const localConfig = "[profiles.local-only.claude]\nconfig_dir = \"~/.claude-local\"\n"

	t.Run("local-only slot is not offered and the remote is asked", func(t *testing.T) {
		h, capture := openRemoteDialogOn(t, remoteGroupItem("myserver"), localConfig, "claude", "acct-task")
		if cfg, _ := session.LoadUserConfig(); strings.Join(session.ConfiguredAccountNames(cfg), ",") != "local-only" {
			t.Fatal("precondition: this machine's config must define the local-only slot")
		}
		if h.newDialog.claudeOptions.hasAccountRow() {
			t.Fatalf("remote dialog offers %v, want no local slots", h.newDialog.claudeOptions.accounts)
		}
		if strings.Join(capture.accountsFetchedFor, ",") != "myserver" {
			t.Fatalf("account slots fetched for %v, want myserver once", capture.accountsFetchedFor)
		}
		h.newDialog.claudeOptions.SetAccount("local-only")
		submitRemoteDialog(t, h)
		if capture.opts.Account != "" {
			t.Fatalf("account = %q, want none: a local-only slot must never travel", capture.opts.Account)
		}
	})

	t.Run("remote-only slot is offered and forwarded", func(t *testing.T) {
		h, capture := openRemoteDialogOn(t, remoteGroupItem("myserver"), localConfig, "claude", "acct-task")
		model, _ := h.Update(remoteAccountsFetchedMsg{remoteName: "myserver", accounts: []string{"srv-alice", "srv-bob"}, gen: h.remoteAccountsGen}.catalogMessage())
		h = model.(*Home)
		if !h.newDialog.claudeOptions.hasAccountRow() {
			t.Fatal("the remote's slots must populate the account row")
		}
		if got := strings.Join(h.newDialog.claudeOptions.accounts, ","); got != "srv-alice,srv-bob" {
			t.Fatalf("offered slots = %q, want exactly the remote's", got)
		}
		h.newDialog.claudeOptions.SetAccount("srv-bob")

		submitRemoteDialog(t, h)

		if capture.opts.Account != "srv-bob" {
			t.Fatalf("account = %q, want srv-bob", capture.opts.Account)
		}
	})

	t.Run("answers for another remote, a failed fetch or a closed dialog are dropped", func(t *testing.T) {
		h, _ := openRemoteDialogOn(t, remoteGroupItem("myserver"), localConfig, "claude", "acct-task")
		for _, msg := range []remoteAccountsFetchedMsg{
			{remoteName: "otherserver", accounts: []string{"stale"}, gen: h.remoteAccountsGen},
			{remoteName: "myserver", accounts: []string{"stale"}, err: errUnavailable, gen: h.remoteAccountsGen},
		} {
			model, _ := h.Update(msg.catalogMessage())
			h = model.(*Home)
			if h.newDialog.claudeOptions.hasAccountRow() {
				t.Fatalf("msg %+v must not populate the account row", msg)
			}
		}
		h.newDialog.Hide()
		model, _ := h.Update(remoteAccountsFetchedMsg{remoteName: "myserver", accounts: []string{"late"}, gen: h.remoteAccountsGen}.catalogMessage())
		h = model.(*Home)
		if h.newDialog.claudeOptions.hasAccountRow() {
			t.Fatal("a late answer for a closed dialog must be dropped")
		}
	})

	t.Run("a late answer for an earlier opening of the same remote never replaces the current list", func(t *testing.T) {
		h, capture := openRemoteDialogOn(t, remoteGroupItem("myserver"), localConfig, "claude", "acct-task")
		firstGen := h.remoteAccountsGen
		if firstGen == 0 {
			t.Fatal("precondition: opening the remote dialog must number the account fetch")
		}
		// Close and reopen on the same remote while fetch A is still pending.
		h.newDialog.Hide()
		h.pendingRemoteName = ""
		model, _ := h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
		h = model.(*Home)
		// The stub fetcher returns no command, so only the numbering and the
		// recorded request prove that a fresh fetch was asked for.
		if h.remoteAccountsGen != firstGen+1 {
			t.Fatalf("reopening must request a new numbered fetch (gen %d -> %d)", firstGen, h.remoteAccountsGen)
		}
		if strings.Join(capture.accountsFetchedFor, ",") != "myserver,myserver" {
			t.Fatalf("fetches requested for %v, want myserver twice", capture.accountsFetchedFor)
		}
		h.newDialog.SetRemoteCreationCatalog(remoteDialogTestCatalog())
		h.newDialog.SetDefaultTool("claude")
		for _, r := range "acct-task" {
			h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
		// Fetch B (this opening) answers first and the user picks a slot.
		model, _ = h.Update(remoteAccountsFetchedMsg{remoteName: "myserver", accounts: []string{"srv-alice", "srv-bob"}, gen: h.remoteAccountsGen}.catalogMessage())
		h = model.(*Home)
		h.newDialog.claudeOptions.SetAccount("srv-bob")
		// Fetch A (the earlier opening) answers late with a different list in
		// which the same cursor position would name another account.
		model, _ = h.Update(remoteAccountsFetchedMsg{remoteName: "myserver", accounts: []string{"srv-zed", "srv-alice", "srv-bob"}, gen: firstGen}.catalogMessage())
		h = model.(*Home)
		if got := strings.Join(h.newDialog.claudeOptions.accounts, ","); got != "srv-alice,srv-bob" {
			t.Fatalf("offered slots = %q after the late answer, want the current opening's list unchanged", got)
		}
		if got := h.newDialog.GetClaudeAccount(); got != "srv-bob" {
			t.Fatalf("selected account = %q after the late answer, want srv-bob", got)
		}
		submitRemoteDialog(t, h)
		if capture.opts.Account != "srv-bob" {
			t.Fatalf("forwarded account = %q, want srv-bob", capture.opts.Account)
		}
	})
}

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

// n on one of the remote's OWN group headers (Level > 0) offers the new
// session in that group, the way n on a local group header does. Before this
// the dialog forced the default group for every remote header, so a session
// created from "remotes/box/work" landed in the remote's my-sessions.
func TestRemoteDialog_RemoteGroupHeader_ForwardsThatGroup(t *testing.T) {
	item := session.Item{Type: session.ItemTypeRemoteGroup, RemoteName: "myserver", Path: "remotes/myserver/work/api", Level: 2}
	h, capture := openRemoteDialogOn(t, item, "", "claude", "grouped-task")

	submitRemoteDialog(t, h)

	if capture.calls != 1 || capture.remoteName != "myserver" {
		t.Fatalf("remote create called %d times for %q, want once for myserver", capture.calls, capture.remoteName)
	}
	if capture.opts.Group != "work/api" {
		t.Fatalf("opts = %+v, want the header's own remote group work/api", capture.opts)
	}
}

// The Level-0 "remotes/<host>" header is a local UI bucket, not a remote
// group: the dialog forwards no group at all, landing the session at the
// remote's true top level. Forwarding the local "my-sessions" default here
// used to file it into an unrelated, empty subgroup instead.
func TestRemoteDialog_RemoteHostHeader_KeepsDefaultGroup(t *testing.T) {
	item := session.Item{Type: session.ItemTypeRemoteGroup, RemoteName: "myserver", Path: "remotes/myserver", Level: 0}
	h, capture := openRemoteDialogOn(t, item, "", "claude", "root-task")

	submitRemoteDialog(t, h)

	if capture.calls != 1 || capture.opts.Group != "" {
		t.Fatalf("opts = %+v (calls=%d), want an empty group (remote root) for the host header", capture.opts, capture.calls)
	}
}

func remoteDialogTestCatalog() *session.RemoteCreationCatalog {
	fields := []session.RemoteCreationField{}
	for _, name := range []string{"json", "quick", "sandbox", "yolo", "create-dir", "no-wait", "no-parent", "skip-permissions", "auto-mode", "chrome", "teammate-mode", "continue", "resume"} {
		fields = append(fields, session.RemoteCreationField{Name: name})
	}
	for _, name := range []string{"t", "g", "c", "account", "model", "mcp", "resume-session", "extra-arg", "w", "startup-query", "additional-path", "effort", "parent"} {
		fields = append(fields, session.RemoteCreationField{Name: name, TakesValue: true})
	}
	catalog := &session.RemoteCreationCatalog{Version: 1, DefaultTool: "", Commands: map[string][]session.RemoteCreationField{"add": fields, "launch": fields}}
	for _, name := range []string{"", "claude", "codex", "gemini", "hermes", "opencode"} {
		catalog.Tools = append(catalog.Tools, session.RemoteCreationTool{Name: name, Kind: name})
	}
	return catalog
}

func TestRemoteDialog_CatalogOwnsToolsModelsAndParents(t *testing.T) {
	h, capture := openRemoteDialogOn(t, remoteGroupItem("myserver"), "default_tool = \"claude\"\n[tools.controller-only]\ncommand = \"true\"\n", "claude", "remote-owner")
	catalog := remoteDialogTestCatalog()
	catalog.Tools = []session.RemoteCreationTool{{Name: "remote-codex", Kind: "codex", Models: []string{"remote-model"}}}
	catalog.DefaultTool = "remote-codex"
	catalog.Conductors = []session.RemoteCreationConductor{{ID: "remote-parent", Title: "Remote parent"}}
	h.newDialog.SetRemoteCreationCatalog(catalog)
	if strings.Join(h.newDialog.presetCommands, ",") != "remote-codex" || strings.Join(h.newDialog.modelSuggestions, ",") != "remote-model" || len(h.newDialog.inheritedSettings) != 0 {
		t.Fatal("controller catalog leaked")
	}
	h.newDialog.conductorCursor = 1
	h.newDialog.multiRepoEnabled = true
	h.newDialog.multiRepoPaths = []string{"~/repo", "$PROJECT/other"}
	submitRemoteDialog(t, h)
	if capture.opts.ParentID != "remote-parent" || capture.opts.Path != "~/repo" || capture.opts.AdditionalPaths[0] != "$PROJECT/other" {
		t.Fatalf("owner values lost: %+v", capture.opts)
	}
}

func TestRemoteDialog_MissingCatalogRefusesBeforeCreate(t *testing.T) {
	h, capture := openRemoteDialogAndTypeName(t, "myserver", "claude", "blocked")
	if !strings.Contains(h.newDialog.View(), "remote: myserver") {
		t.Fatal("remote host identity not visible")
	}
	h.newDialog.remoteCatalog = nil
	submitRemoteDialogExpectingError(t, h, capture, "capabilities")
}

func TestRemoteDialog_UnsupportedCatalogFieldRefusesBeforeCreate(t *testing.T) {
	h, capture := openRemoteDialogAndTypeName(t, "myserver", "codex", "blocked")
	h.newDialog.reasoningEffort = "high"
	for command, fields := range h.newDialog.remoteCatalog.Commands {
		var supported []session.RemoteCreationField
		for _, field := range fields {
			if field.Name != "effort" {
				supported = append(supported, field)
			}
		}
		h.newDialog.remoteCatalog.Commands[command] = supported
	}
	submitRemoteDialogExpectingError(t, h, capture, "effort")
}

// Fixture adapters keep the account and MCP scenarios focused while exercising
// the single production catalog response.
type remoteAccountsFetchedMsg struct {
	remoteName string
	accounts   []string
	err        error
	gen        uint64
}

func (m remoteAccountsFetchedMsg) catalogMessage() remoteCreationCatalogFetchedMsg {
	c := remoteDialogTestCatalog()
	c.DefaultTool = "claude"
	c.Accounts = m.accounts
	return remoteCreationCatalogFetchedMsg{remoteName: m.remoteName, catalog: c, err: m.err, gen: m.gen}
}

type remoteMCPsFetchedMsg struct {
	remoteName string
	mcps       []string
	err        error
	gen        uint64
}

func (m remoteMCPsFetchedMsg) catalogMessage() remoteCreationCatalogFetchedMsg {
	c := remoteDialogTestCatalog()
	c.DefaultTool = "claude"
	c.MCPs = m.mcps
	return remoteCreationCatalogFetchedMsg{remoteName: m.remoteName, catalog: c, err: m.err, gen: m.gen}
}

func TestRemoteDialog_RemoteDefaultsCanBeDisabled(t *testing.T) {
	h, capture := openRemoteDialogAndTypeName(t, "myserver", "claude", "choices")
	c := remoteDialogTestCatalog()
	c.DefaultTool = "claude"
	c.Defaults = map[string]bool{"skip_permissions": true, "codex_yolo": true}
	h.newDialog.SetRemoteCreationCatalog(c)
	if !h.newDialog.claudeOptions.skipPermissions {
		t.Fatal("remote default missing")
	}
	h.newDialog.claudeOptions.skipPermissions = false
	submitRemoteDialog(t, h)
	if capture.opts.ClaudeOptions == nil || capture.opts.ClaudeOptions.SkipPermissions {
		t.Fatal("explicit false lost")
	}
	h, capture = openRemoteDialogAndTypeName(t, "myserver", "codex", "choices")
	h.newDialog.codexOptions.SetDefaults(false)
	submitRemoteDialog(t, h)
	if capture.opts.YoloOverride == nil || *capture.opts.YoloOverride {
		t.Fatal("explicit yolo false lost")
	}
}

func TestRemoteDialog_CustomShellCommandUsesRemoteText(t *testing.T) {
	h, capture := openRemoteDialogAndTypeName(t, "myserver", "", "custom-shell")
	if !h.newDialog.customCommandSelected() || !strings.Contains(h.newDialog.View(), "Custom:") {
		t.Fatal("remote shell lacks custom command input")
	}
	h.newDialog.focusIndex = h.newDialog.indexOf(focusCommand)
	h.newDialog.updateFocus()
	for _, r := range "remote-script --verbose" {
		h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	submitRemoteDialog(t, h)
	if capture.opts.Tool != "remote-script --verbose" {
		t.Fatalf("command lost: %+v", capture.opts)
	}
}

func TestRemoteDialog_LocalRefreshCannotReplaceRemoteTools(t *testing.T) {
	h, _ := openRemoteDialogAndTypeName(t, "myserver", "claude", "owner")
	h.newDialog.remoteCatalog.Tools = []session.RemoteCreationTool{{Name: "remote-only", Kind: "codex"}}
	h.newDialog.SetRemoteCreationCatalog(h.newDialog.remoteCatalog)
	h.newDialog.RefreshPresetCommands()
	if strings.Join(h.newDialog.presetCommands, ",") != "remote-only" {
		t.Fatal("controller refresh replaced remote catalog")
	}
	h.newDialog.Hide()
	h.newDialog.SetDefaultTool("claude")
	h.newDialog.ShowInGroup("local", "local", ".", nil, "")
	if h.newDialog.GetSelectedCommand() != "claude" {
		t.Fatal("local tool selection lost after closing remote dialog")
	}
}

func TestRemoteDialog_PathTabDoesNotConsultController(t *testing.T) {
	h, _ := openRemoteDialogAndTypeName(t, "myserver", "claude", "path")
	d := h.newDialog
	d.pathInput.SetValue("~/nonexistent-on-controller/remote")
	d.focusIndex = d.indexOf(focusPath)
	d.updateFocus()
	d.Update(tea.KeyMsg{Type: tea.KeyTab})
	if d.currentTarget() == focusPath || d.pathInput.Value() != "~/nonexistent-on-controller/remote" {
		t.Fatal("remote path navigation consulted or expanded controller path")
	}
}

func TestRemoteDialog_EmptyCatalogToolNavigation(t *testing.T) {
	for _, state := range []string{"pending", "failed", "empty"} {
		for _, key := range []tea.KeyType{tea.KeyLeft, tea.KeyRight} {
			t.Run(state+"/"+tea.KeyMsg{Type: key}.String(), func(t *testing.T) {
				defer func() {
					if recovered := recover(); recovered != nil {
						t.Errorf("empty catalog navigation panicked: %v", recovered)
					}
				}()
				h, capture := newRemoteHome(t, remoteGroupItem("myserver"), "")
				h = pressN(t, h)
				if state == "failed" {
					model, _ := h.Update(remoteCreationCatalogFetchedMsg{remoteName: "myserver", gen: h.remoteAccountsGen, err: errUnavailable})
					h = model.(*Home)
				}
				if state == "empty" {
					c := remoteDialogTestCatalog()
					c.Tools = nil
					h.newDialog.SetRemoteCreationCatalog(c)
				}
				d := h.newDialog
				d.nameInput.SetValue("blocked")
				d.focusIndex = d.indexOf(focusCommand)
				d.updateFocus()
				if d.customCommandSelected() {
					t.Fatal("empty catalog must not enable a custom command")
				}
				h.handleNewDialogKey(tea.KeyMsg{Type: key})
				if d.commandCursor != 0 {
					t.Fatalf("empty catalog cursor = %d", d.commandCursor)
				}
				if _, _, command := d.GetRemoteValues(); command != "" {
					t.Fatalf("empty catalog command = %q", command)
				}
				if capture.calls != 0 {
					t.Fatal("navigation created a session")
				}
				if !d.IsVisible() {
					t.Fatal("navigation closed dialog")
				}
				submitRemoteDialogExpectingError(t, h, capture, "capabilities")
			})
		}
	}
}

func TestRemoteDialog_ExplicitShellOverridesCatalogDefault(t *testing.T) {
	h, capture := openRemoteDialogAndTypeName(t, "myserver", "claude", "plain-shell")
	catalog := remoteDialogTestCatalog()
	catalog.DefaultTool = "codex"
	h.newDialog.SetRemoteCreationCatalog(catalog)
	if h.newDialog.GetSelectedCommand() != "codex" {
		t.Fatal("remote default not selected")
	}
	h.newDialog.SetDefaultTool("")
	// Empty command is the add handler's plain shell contract. It does not use
	// the catalog default; a custom command, when typed, is forwarded instead.
	submitRemoteDialog(t, h)
	if capture.calls != 1 || capture.opts.Tool != "" || capture.opts.ClaudeOptions != nil || capture.opts.YoloOverride != nil {
		t.Fatalf("shell selection lost: %+v", capture.opts)
	}
}
