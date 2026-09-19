package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

func TestRemoteLegacyDialog_Golden(t *testing.T) {
	forceTrueColorProfile()
	h, capture := newRemoteHome(t, remoteGroupItem("old-host"), "")
	h = pressN(t, h)
	binDir := t.TempDir()
	script := `#!/bin/sh
case "$*" in
 *--capabilities*)
  printf 'flag provided but not defined: -capabilities\nUsage: agent-deck add\n  -account string\n  -mcp string\n' >&2
  exit 2
  ;;
 *"'add'"*) printf '{"id":"legacy-created","title":"legacy-session"}\n' ;;
 *"'session' 'start'"*) exit 0 ;;
 *) printf 'unexpected fake SSH invocation\n' >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "ssh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	runner := session.NewSSHRunner("", session.RemoteConfig{Host: "fake-old-host"})
	catalog, err := runner.FetchCreationCatalog(context.Background())
	if err != nil {
		t.Fatalf("old remote catalog: %v", err)
	}
	h.newDialog.SetRemoteCreationCatalog(catalog)
	d := h.newDialog
	d.ToggleMultiRepo()
	if d.multiRepoEnabled {
		t.Fatal("legacy dialog enables unsupported multi-repo mode")
	}
	d.SetSize(120, 50)
	d.nameInput.SetValue("legacy-session")
	d.pathInput.SetValue("~/project")
	d.commandInput.SetValue("gemini")
	d.updateToolOptions()
	for _, target := range []focusTarget{focusModel, focusReasoningEffort, focusConductor, focusRemoteMCPs, focusOptions, focusMultiRepo} {
		if d.indexOf(target) >= 0 {
			t.Errorf("unsupported field remains focusable: %v", target)
		}
	}
	frame := strings.TrimRight(stripAnsi(d.View()), "\n") + "\n"
	// The notice is long enough to wrap inside the box at this width, so
	// compare against the box-drawing-stripped, whitespace-collapsed frame
	// rather than the raw multi-line rendering (mirrors the flattening in
	// TestRemoteUpdatedMsg_FailureOpensNoticeWithFullMessage).
	flat := strings.Join(strings.Fields(strings.NewReplacer("│", " ", "╭", " ", "╮", " ", "╰", " ", "╯", " ", "─", " ").Replace(frame)), " ")
	if strings.Count(flat, legacyRemoteCreationNotice) != 1 {
		t.Fatalf("expected single legacy notice:\n%s", frame)
	}
	path := filepath.Join("testdata", "newdialog_flow", "06-legacy-remote.txt")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, []byte(frame), 0644); err != nil {
			t.Fatal(err)
		}
	} else {
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(want) != frame {
			t.Fatalf("legacy golden differs\nwant:\n%s\ngot:\n%s", want, frame)
		}
	}
	h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	want := session.RemoteAddOptions{Tool: "gemini", Title: "legacy-session", Path: "~/project", Group: d.GetSelectedGroup()}
	if capture.calls != 1 || !reflect.DeepEqual(capture.opts, want) {
		t.Fatalf("legacy creation = %#v (%d calls), want %#v", capture.opts, capture.calls, want)
	}
	id, err := runner.CreateSessionWithOptions(context.Background(), capture.opts)
	if err != nil || id != "legacy-created" {
		t.Fatalf("legacy remote add = %q, %v", id, err)
	}
}

func TestRemoteLegacyDialog_Options(t *testing.T) {
	for _, command := range []string{"", "claude", "gemini", "codex", "hermes", "custom --flag"} {
		t.Run(command, func(t *testing.T) {
			h, _ := newRemoteHome(t, remoteGroupItem("old-host"), "")
			h = pressN(t, h)
			d := h.newDialog
			d.SetRemoteCreationCatalog(session.LegacyRemoteCreationCatalog())
			d.nameInput.SetValue("legacy")
			d.commandInput.SetValue(command)
			d.pathInput.SetValue("~/first")
			d.worktreeEnabled = true
			d.branchInput.SetValue("feature")
			d.sandboxEnabled = true
			d.modelInput.SetValue("stale-controller-model")
			got, why := d.GetRemoteCreateOptions()
			want := session.RemoteAddOptions{Tool: command, Title: "legacy", Path: "~/first", Group: d.GetSelectedGroup(), WorktreeBranch: "feature", Sandbox: true}
			if why != "" || !reflect.DeepEqual(got, want) {
				t.Fatalf("got %#v (%s), want %#v", got, why, want)
			}
		})
	}
}

// TestRemoteLegacyDialog_OffersBuiltinTools is walk defect #6's regression
// test: a remote with no capability catalog at all (#2275's Legacy
// fallback) used to collapse the Command picker to shell-only, hiding
// claude/codex/pi/etc even though the remote's bare -c/--cmd flag runs any
// of them. It must instead offer the same built-in list the local dialog
// does, with the tool-specific option panels still hidden (kind stays
// unverified for anything but shell).
func TestRemoteLegacyDialog_OffersBuiltinTools(t *testing.T) {
	h, _ := newRemoteHome(t, remoteGroupItem("old-host"), "")
	h = pressN(t, h)
	d := h.newDialog
	d.SetRemoteCreationCatalog(session.LegacyRemoteCreationCatalog())

	want := buildPresetCommands()
	if !reflect.DeepEqual(d.presetCommands, want) {
		t.Fatalf("presetCommands = %#v, want the local built-in list %#v", d.presetCommands, want)
	}
	for _, tool := range []string{"claude", "codex", "gemini", "pi", "hermes"} {
		found := false
		for _, cmd := range d.presetCommands {
			if cmd == tool {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q missing from legacy dialog's command picker: %v", tool, d.presetCommands)
		}
	}

	// Selecting a builtin still leaves its kind unverified (Legacy's Tools
	// stays [{Name: ""}]), so the tool-specific options panel never opens —
	// this is what correctly keeps model/account/MCP rows hidden.
	d.SetDefaultTool("claude")
	if d.toolKind(d.GetSelectedCommand()) != "" {
		t.Errorf("toolKind(%q) = %q, want empty (unverified) on a legacy remote", d.GetSelectedCommand(), d.toolKind(d.GetSelectedCommand()))
	}
	if d.toolOptions != nil {
		t.Error("tool options panel must stay hidden on a legacy remote even when a known builtin is picked")
	}
}

func TestRemoteLegacyDialog_MultiRepoRefused(t *testing.T) {
	h, _ := newRemoteHome(t, remoteGroupItem("old-host"), "")
	h = pressN(t, h)
	d := h.newDialog
	d.multiRepoEnabled = true
	d.multiRepoPaths = []string{"~/first", "~/second"}
	d.SetRemoteCreationCatalog(session.LegacyRemoteCreationCatalog())
	opts, why := d.GetRemoteCreateOptions()
	if why != "unsupported remote creation field --additional-path; update the remote" {
		t.Fatalf("requested multi-repo must be refused: %q", why)
	}
	if !d.multiRepoEnabled || !reflect.DeepEqual(opts.AdditionalPaths, []string{"~/second"}) {
		t.Fatalf("requested paths silently cleared: %#v", opts)
	}
	if d.indexOf(focusMultiRepo) >= 0 || strings.Contains(stripAnsi(d.View()), "Multi-repo mode") {
		t.Fatal("legacy dialog exposes unsupported multi-repo control")
	}
}

func TestRemoteCreationCatalogError_SingleLine(t *testing.T) {
	h, _ := newRemoteHome(t, remoteGroupItem("old-host"), "")
	h = pressN(t, h)
	h.Update(remoteCreationCatalogFetchedMsg{remoteName: "old-host", gen: h.remoteAccountsGen, err: errors.New("ssh failed: exit status 255\nprivate remote diagnostic\nUsage: agent-deck add")})
	if strings.ContainsAny(h.newDialog.validationErr, "\r\n") || strings.Contains(h.newDialog.validationErr, "private") || strings.Contains(h.newDialog.validationErr, "Usage") {
		t.Fatalf("remote stderr leaked: %q", h.newDialog.validationErr)
	}
	if h.newDialog.validationErr == "" {
		t.Fatal("missing refusal")
	}
	if _, why := h.newDialog.GetRemoteCreateOptions(); why == "" {
		t.Fatal("failed remote catalog must refuse creation")
	}
}
