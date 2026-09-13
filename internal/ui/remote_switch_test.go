package ui

// Shift+P on a remote row. The Edit Session dialog opens for the remote
// session, its account pills come from the remote's own `accounts`, Enter
// asks the remote for `session switch-preview --json`, the same confirmation
// dialog a local switch gets is shown from that answer, and Switch runs the
// remote's own `session switch`. Nothing local is read for the switch: the
// fake runner below stands in for SSH and records every argument.
//
// Golden frames live under testdata/remote_switch/ (UPDATE_GOLDEN=1 to
// regenerate). They render with a throwaway HOME; no tmux server is needed
// because no session is started here.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type fakeRemoteSwitchRunner struct {
	calls    []string
	accounts map[string][]string
	preview  *session.RemoteSwitchPreview
	previews map[string]*session.RemoteSwitchPreview
	result   *session.RemoteSwitchResult
	err      error
}

func (f *fakeRemoteSwitchRunner) FetchAccountsForHarness(ctx context.Context, harness string) ([]string, error) {
	f.calls = append(f.calls, "accounts "+harness)
	if f.err != nil {
		return nil, f.err
	}
	return f.accounts[session.CanonicalSwitchHarnessForUI(harness)], nil
}

func (f *fakeRemoteSwitchRunner) SwitchPreview(ctx context.Context, sessionID, harness, account string) (*session.RemoteSwitchPreview, error) {
	f.calls = append(f.calls, strings.TrimSpace("switch-preview "+sessionID+" "+harness+" "+account))
	if f.err != nil {
		return nil, f.err
	}
	if p, ok := f.previews[harness+"/"+account]; ok {
		return p, nil
	}
	return f.preview, nil
}

func (f *fakeRemoteSwitchRunner) SwitchSession(ctx context.Context, sessionID, harness, account string, confirmContextLoss bool) (*session.RemoteSwitchResult, error) {
	call := strings.TrimSpace("switch " + sessionID + " " + harness + " " + account)
	if confirmContextLoss {
		call += " --confirm-context-loss"
	}
	f.calls = append(f.calls, call)
	return f.result, f.err
}

func armHomeWithRemoteRowForSwitch(t *testing.T, runner *fakeRemoteSwitchRunner) *Home {
	t.Helper()
	forceTrueColorProfile()
	withTempAgentDeckHome(t, `
[remotes.lab]
host = "alice@lab.example"
agent_deck_path = "/usr/local/bin/agent-deck"
`)
	previous := newRemoteSwitchRunner
	newRemoteSwitchRunner = func(remoteName string) (remoteSwitchRunner, error) {
		if remoteName != "lab" {
			return nil, errors.New("remote '" + remoteName + "' not found")
		}
		return runner, nil
	}
	t.Cleanup(func() { newRemoteSwitchRunner = previous })

	home := NewHome()
	home.width = 120
	home.height = 40
	home.initialLoading = false
	info := session.RemoteSessionInfo{ID: "remote-id-xyz", Title: "remote session", Group: "work", Tool: "claude", Account: "personal", Status: "idle", RemoteName: "lab"}
	home.remoteSessions = map[string][]session.RemoteSessionInfo{"lab": {info}}
	rowInfo := info
	home.flatItems = []session.Item{{Type: session.ItemTypeRemoteSession, RemoteName: "lab", RemoteSession: &rowInfo}}
	home.cursor = 0
	return home
}

// runCmd executes a tea.Cmd synchronously and feeds its message back.
func runCmd(t *testing.T, home *Home, cmd tea.Cmd) tea.Cmd {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a cmd")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var next []tea.Cmd
		for _, c := range batch {
			if c != nil {
				if n := runCmd(t, home, c); n != nil {
					next = append(next, n)
				}
			}
		}
		if len(next) == 0 {
			return nil
		}
		return tea.Batch(next...)
	}
	_, next := home.Update(msg)
	return next
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	got = strings.TrimRight(stripAnsi(got), "\n") + "\n"
	path := filepath.Join("testdata", "remote_switch", name+".txt")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (UPDATE_GOLDEN=1 to create)", path, err)
	}
	if string(want) != got {
		t.Fatalf("golden %s differs.\n--- want\n%s\n--- got\n%s", path, want, got)
	}
}

func selectEditPill(t *testing.T, d *EditSessionDialog, key, value string) {
	t.Helper()
	for i := range d.fields {
		if d.fields[i].key != key {
			continue
		}
		for j, opt := range d.fields[i].pillOptions {
			if opt == value {
				d.focusIndex = i
				d.fields[i].pillCursor = j
				if key == session.FieldTool {
					d.refreshTargetAccountPills(value)
				}
				return
			}
		}
		t.Fatalf("field %q has no option %q (have %v)", key, value, d.fields[i].pillOptions)
	}
	t.Fatalf("field %q not shown", key)
}

// Shift+P on a remote row opens the Edit Session dialog for that session and
// asks the remote for its account slots; local config is never consulted.
func TestRemoteSwitch_ShiftPOpensRemoteEditDialog(t *testing.T) {
	runner := &fakeRemoteSwitchRunner{accounts: map[string][]string{"claude": {"personal", "work"}, "codex": {"codexwork"}}}
	home := armHomeWithRemoteRowForSwitch(t, runner)

	_, cmd := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
	if !home.editSessionDialog.IsVisible() {
		t.Fatal("Shift+P on a remote row must open the Edit Session dialog")
	}
	if !home.editSessionDialog.IsRemote() || home.editSessionDialog.RemoteName() != "lab" || home.editSessionDialog.SessionID() != "remote-id-xyz" {
		t.Fatalf("dialog is not bound to the remote row: remote=%q id=%q", home.editSessionDialog.RemoteName(), home.editSessionDialog.SessionID())
	}
	assertGolden(t, "01-shift-p-loading-accounts", home.editSessionDialog.View())

	runCmd(t, home, cmd)
	if strings.Join(runner.calls, "|") != "accounts claude|accounts codex" {
		t.Fatalf("remote calls = %v; want the remote's account slots for both harness families", runner.calls)
	}
	if got := home.editSessionDialog.selectedPill(session.FieldAccount); got != "personal" {
		t.Fatalf("account row must show the remote session's current slot, got %q", got)
	}
	assertGolden(t, "02-remote-accounts-loaded", home.editSessionDialog.View())

	// The harness row re-targets the account pills from the remote's slots,
	// exactly as the local dialog does from local config.
	selectEditPill(t, home.editSessionDialog, session.FieldTool, "codex")
	if got := home.editSessionDialog.selectedPill(session.FieldAccount); got != "" {
		t.Fatalf("a slot missing on the target harness must fall back to default, got %q", got)
	}
	selectEditPill(t, home.editSessionDialog, session.FieldAccount, "codexwork")
	assertGolden(t, "03-codex-target-with-remote-slot", home.editSessionDialog.View())
}

// Enter with a new account asks the remote for a preview and shows the same
// Switch Account confirmation the local path shows, with the remote named.
func TestRemoteSwitch_EnterPreviewsThenConfirms(t *testing.T) {
	runner := &fakeRemoteSwitchRunner{
		accounts: map[string][]string{"claude": {"personal", "work"}},
		preview:  &session.RemoteSwitchPreview{SourceTool: "claude", SourceAccount: "personal", TargetHarness: "claude", TargetAccount: "work", TargetAccountStatus: "configured", Capability: "native-resume", Execution: "supported"},
		result:   &session.RemoteSwitchResult{Success: true, Status: "success", ID: "remote-id-xyz", OldTool: "claude", NewTool: "claude", OldAccount: "personal", NewAccount: "work", Continuity: "native-resume", DestinationReady: true, Restarted: true},
	}
	home := armHomeWithRemoteRowForSwitch(t, runner)
	_, cmd := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
	runCmd(t, home, cmd)
	selectEditPill(t, home.editSessionDialog, session.FieldAccount, "work")

	_, cmd = home.handleEditSessionDialogKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter with a pending switch must ask the remote for a preview")
	}
	if home.confirmDialog.IsVisible() {
		t.Fatal("no confirmation may be shown before the remote preview answers")
	}
	runCmd(t, home, cmd)
	if want := "switch-preview remote-id-xyz claude work"; runner.calls[len(runner.calls)-1] != want {
		t.Fatalf("calls = %v; want last %q", runner.calls, want)
	}
	if !home.confirmDialog.IsVisible() || home.confirmDialog.GetConfirmType() != ConfirmSwitchAccount || home.confirmDialog.GetRemoteName() != "lab" {
		t.Fatalf("expected the remote Switch Account confirmation, got visible=%v type=%v remote=%q", home.confirmDialog.IsVisible(), home.confirmDialog.GetConfirmType(), home.confirmDialog.GetRemoteName())
	}
	assertGolden(t, "04-switch-account-confirmation", home.confirmDialog.View())

	// Confirm: the remote runs its own switch; the row shows the in-flight
	// verb until the remote answers.
	cmd = home.confirmAction()
	if cmd == nil {
		t.Fatal("confirm must run the remote switch")
	}
	if home.remotePending["remote-id-xyz"] == "" {
		t.Fatal("the remote row must show the switch in flight")
	}
	if home.editSessionDialog.IsVisible() || home.confirmDialog.IsVisible() {
		t.Fatal("dialogs must close once the switch is running")
	}
	runCmd(t, home, cmd)
	if want := "switch remote-id-xyz claude work"; runner.calls[len(runner.calls)-1] != want {
		t.Fatalf("calls = %v; want last %q", runner.calls, want)
	}
	if home.remotePending["remote-id-xyz"] != "" {
		t.Fatal("pending verb must clear when the remote answers")
	}
	if !home.confirmDialog.IsVisible() || home.confirmDialog.GetConfirmType() != ConfirmNotice || !strings.Contains(home.confirmDialog.noticeTitle, "verified") {
		t.Fatalf("a ready native switch must be reported as verified: %q / %q", home.confirmDialog.noticeTitle, home.confirmDialog.noticeBody)
	}
	if !strings.Contains(home.confirmDialog.noticeBody, "lab") {
		t.Fatalf("the notice must name the remote: %q", home.confirmDialog.noticeBody)
	}
	// The cached row reflects the confirmed switch before the next fetch.
	if got := home.remoteSessions["lab"][0].Account; got != "work" {
		t.Fatalf("cached remote row account = %q, want work", got)
	}
}

// A refusal the remote reports (a slot that exists here but not there, a
// managed conductor on the remote) is shown verbatim and nothing runs.
func TestRemoteSwitch_RemoteRefusalIsReported(t *testing.T) {
	runner := &fakeRemoteSwitchRunner{
		accounts: map[string][]string{"claude": {"personal", "work"}},
		preview: &session.RemoteSwitchPreview{Refusal: &session.RemoteSwitchRefusal{Code: "unknown-account",
			Message: `account "work" has no [profiles.work.claude].config_dir in config.toml (configured accounts: personal)`}},
	}
	home := armHomeWithRemoteRowForSwitch(t, runner)
	_, cmd := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
	runCmd(t, home, cmd)
	selectEditPill(t, home.editSessionDialog, session.FieldAccount, "work")
	_, cmd = home.handleEditSessionDialogKey(tea.KeyMsg{Type: tea.KeyEnter})
	runCmd(t, home, cmd)

	if !home.confirmDialog.IsVisible() || home.confirmDialog.GetConfirmType() != ConfirmNotice {
		t.Fatalf("a refusal must be shown as a notice, got type %v", home.confirmDialog.GetConfirmType())
	}
	if !strings.Contains(home.confirmDialog.noticeBody, "configured accounts: personal") || !strings.Contains(home.confirmDialog.noticeBody, "lab") {
		t.Fatalf("notice must carry the remote's refusal and name the remote: %q", home.confirmDialog.noticeBody)
	}
	assertGolden(t, "05-remote-refusal-notice", home.confirmDialog.View())
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "switch ") {
			t.Fatalf("a refused preview must never run the switch: %v", runner.calls)
		}
	}
	if !home.editSessionDialog.IsVisible() {
		t.Fatal("the edit dialog stays open under the refusal so the user can pick another slot")
	}
}

// A cross-harness target shows the transfer disclosure from the remote's
// preview (its exclusions and warnings, e.g. a harness missing on that
// host), and Transfer forwards --confirm-context-loss.
func TestRemoteSwitch_CrossHarnessTransfer(t *testing.T) {
	runner := &fakeRemoteSwitchRunner{
		accounts: map[string][]string{"claude": {"personal"}},
		preview: &session.RemoteSwitchPreview{SourceTool: "claude", TargetHarness: "codex", Capability: "transcript-tail", Execution: "planned",
			Exclusions: []string{"native session state (target is fresh; no native resume)", "context beyond the export budget"},
			Warnings:   []string{`target harness "codex" was not found on this host's PATH; the fresh target cannot start here until it is installed`}},
		result: &session.RemoteSwitchResult{Success: false, Status: "pending", Pending: true, TargetID: "fresh-1", MissingContract: "target-native identity"},
	}
	home := armHomeWithRemoteRowForSwitch(t, runner)
	_, cmd := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
	runCmd(t, home, cmd)
	selectEditPill(t, home.editSessionDialog, session.FieldTool, "codex")
	_, cmd = home.handleEditSessionDialogKey(tea.KeyMsg{Type: tea.KeyEnter})
	runCmd(t, home, cmd)

	if home.confirmDialog.GetConfirmType() != ConfirmCrossHarnessTransfer || home.confirmDialog.GetRemoteName() != "lab" {
		t.Fatalf("expected the remote transfer confirmation, got %v", home.confirmDialog.GetConfirmType())
	}
	if home.editSessionDialog.IsVisible() {
		t.Fatal("the edit dialog closes behind the transfer confirmation, as locally")
	}
	view := stripAnsi(home.confirmDialog.View())
	if !strings.Contains(view, "PATH") {
		t.Fatalf("the remote's warning must be part of the disclosure:\n%s", view)
	}
	assertGolden(t, "06-cross-harness-transfer-confirmation", home.confirmDialog.View())

	cmd = home.confirmAction()
	runCmd(t, home, cmd)
	if want := "switch remote-id-xyz codex --confirm-context-loss"; runner.calls[len(runner.calls)-1] != want {
		t.Fatalf("calls = %v; want last %q", runner.calls, want)
	}
	if home.confirmDialog.GetConfirmType() != ConfirmNotice || !strings.Contains(home.confirmDialog.noticeTitle, "pending") {
		t.Fatalf("a pending transfer must be reported as pending: %q", home.confirmDialog.noticeTitle)
	}
	if !strings.Contains(home.confirmDialog.noticeBody, "fresh-1") {
		t.Fatalf("the notice must name the new target: %q", home.confirmDialog.noticeBody)
	}
}

// A switch that fails on the remote after committing is reported with the
// remote's message and its recovery flag, never as success.
func TestRemoteSwitch_FailureIsReportedWithRecoveryFlag(t *testing.T) {
	runner := &fakeRemoteSwitchRunner{
		accounts: map[string][]string{"claude": {"personal", "work"}},
		preview:  &session.RemoteSwitchPreview{SourceTool: "claude", TargetHarness: "claude", TargetAccount: "work", Capability: "native-resume", Execution: "supported"},
	}
	home := armHomeWithRemoteRowForSwitch(t, runner)
	_, cmd := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
	runCmd(t, home, cmd)
	selectEditPill(t, home.editSessionDialog, session.FieldAccount, "work")
	_, cmd = home.handleEditSessionDialogKey(tea.KeyMsg{Type: tea.KeyEnter})
	runCmd(t, home, cmd)
	runner.result = &session.RemoteSwitchResult{Status: "failed", RecoveryRequired: true, Error: "persist native switch: journal write failed"}
	runner.err = errors.New("remote switch failed: persist native switch: journal write failed")
	cmd = home.confirmAction()
	runCmd(t, home, cmd)
	if home.confirmDialog.GetConfirmType() != ConfirmNotice || !strings.Contains(home.confirmDialog.noticeTitle, "failed") {
		t.Fatalf("failure must be a failed notice: %q", home.confirmDialog.noticeTitle)
	}
	body := home.confirmDialog.noticeBody
	if !strings.Contains(body, "journal write failed") || !strings.Contains(body, "recovery_required=true") || !strings.Contains(body, "lab") {
		t.Fatalf("notice must carry the remote message, recovery flag and remote name: %q", body)
	}
	if got := home.remoteSessions["lab"][0].Account; got != "personal" {
		t.Fatalf("a failed switch must not patch the cached row, got account %q", got)
	}
}

// The remote row changing under an open confirmation cancels it, mirroring
// the local modal-identity guard.
func TestRemoteSwitch_SourceChangedUnderConfirmationCancels(t *testing.T) {
	runner := &fakeRemoteSwitchRunner{
		accounts: map[string][]string{"claude": {"personal", "work"}},
		preview:  &session.RemoteSwitchPreview{SourceTool: "claude", TargetHarness: "claude", TargetAccount: "work", Capability: "native-resume", Execution: "supported"},
	}
	home := armHomeWithRemoteRowForSwitch(t, runner)
	_, cmd := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
	runCmd(t, home, cmd)
	selectEditPill(t, home.editSessionDialog, session.FieldAccount, "work")
	_, cmd = home.handleEditSessionDialogKey(tea.KeyMsg{Type: tea.KeyEnter})
	runCmd(t, home, cmd)
	home.remoteSessions["lab"][0].Account = "someone-else"
	if cmd := home.confirmAction(); cmd != nil {
		t.Fatal("a changed source must not run the switch")
	}
	if home.confirmDialog.GetConfirmType() != ConfirmNotice || !strings.Contains(home.confirmDialog.noticeTitle, "cancelled") {
		t.Fatalf("expected a cancelled notice, got %q", home.confirmDialog.noticeTitle)
	}
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "switch ") {
			t.Fatalf("switch ran despite the changed source: %v", runner.calls)
		}
	}
}

// A title edit on a remote row still goes through the remote's rename, and
// mixing it with a switch is refused the way the local dialog refuses it.
func TestRemoteSwitch_TitleEditAndMixedEdits(t *testing.T) {
	runner := &fakeRemoteSwitchRunner{accounts: map[string][]string{"claude": {"personal", "work"}}}
	home := armHomeWithRemoteRowForSwitch(t, runner)
	_, cmd := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
	runCmd(t, home, cmd)
	home.editSessionDialog.fields[0].input.SetValue("renamed session")
	selectEditPill(t, home.editSessionDialog, session.FieldAccount, "work")
	if _, cmd := home.handleEditSessionDialogKey(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Fatal("a title edit combined with a switch must be refused before anything runs")
	}
	if home.editSessionDialog.validationErr == "" {
		t.Fatal("the dialog must explain that the switch is saved separately")
	}

	selectEditPill(t, home.editSessionDialog, session.FieldAccount, "personal")
	_, cmd = home.handleEditSessionDialogKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("a title-only edit must run the remote rename")
	}
	if home.editSessionDialog.IsVisible() {
		t.Fatal("the dialog closes on a plain rename")
	}
	if got := home.remoteSessions["lab"][0].Title; got != "renamed session" {
		t.Fatalf("optimistic title = %q", got)
	}
}
