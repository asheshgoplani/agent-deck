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
	d.SetSize(120, 50)
	d.nameInput.SetValue("legacy-session")
	d.pathInput.SetValue("~/project")
	d.commandInput.SetValue("gemini")
	d.updateToolOptions()
	for _, target := range []focusTarget{focusModel, focusReasoningEffort, focusConductor, focusRemoteMCPs, focusOptions} {
		if d.indexOf(target) >= 0 {
			t.Errorf("unsupported field remains focusable: %v", target)
		}
	}
	frame := strings.TrimRight(stripAnsi(d.View()), "\n") + "\n"
	notice := "Remote runs an older agent-deck; extra options are hidden until it is updated."
	if strings.Count(frame, notice) != 1 {
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
			d.multiRepoEnabled = true
			d.multiRepoPaths = []string{"~/first", "~/second"}
			d.worktreeEnabled = true
			d.branchInput.SetValue("feature")
			d.sandboxEnabled = true
			d.modelInput.SetValue("stale-controller-model")
			got, why := d.GetRemoteCreateOptions()
			want := session.RemoteAddOptions{Tool: command, Title: "legacy", Path: "~/first", AdditionalPaths: []string{"~/second"}, Group: d.GetSelectedGroup(), WorktreeBranch: "feature", Sandbox: true}
			if why != "" || !reflect.DeepEqual(got, want) {
				t.Fatalf("got %#v (%s), want %#v", got, why, want)
			}
		})
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
