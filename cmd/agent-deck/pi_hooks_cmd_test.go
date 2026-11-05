// pi_hooks_cmd_test.go covers the pi (pi-coding-agent) hook integration added
// for issue #2222: the event mapping `agent-deck hook-handler` applies to pi's
// lifecycle event names, and the `agent-deck pi-hooks` CLI wiring.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/comms"
	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestMapEventToStatus_PiEvents pins pi's native event vocabulary onto the
// shared normalized event map. The extension emits pi's own names, not
// Claude's, so the mapping (not the extension) is where the translation lives.
func TestMapEventToStatus_PiEvents(t *testing.T) {
	tests := []struct {
		event  string
		expect string
	}{
		{"turn_start", "running"},
		{"turn_end", "waiting"},
		{"session_start", "waiting"},
		{"session_shutdown", "dead"},
		// The normalizer folds separators and case, so these are the same
		// events however a future pi version spells them.
		{"turnStart", "running"},
		{"turn-end", "waiting"},
		{"sessionShutdown", "dead"},
	}
	for _, tt := range tests {
		t.Run(tt.event, func(t *testing.T) {
			if got := mapEventToStatus(tt.event); got != tt.expect {
				t.Errorf("mapEventToStatus(%q) = %q, want %q", tt.event, got, tt.expect)
			}
		})
	}
}

func setupPiHooksTest(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "pi")
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	return filepath.Join(dir, "extensions")
}

func TestHandlePiHooks_InstallStatusUninstall(t *testing.T) {
	extDir := setupPiHooksTest(t)

	out := captureStdout(t, func() { handlePiHooks([]string{"status"}) })
	if !strings.Contains(out, "Status: NOT INSTALLED") {
		t.Errorf("status before install = %q, want NOT INSTALLED", out)
	}

	out = captureStdout(t, func() { handlePiHooks([]string{"install"}) })
	if !strings.Contains(out, "pi hooks installed successfully.") {
		t.Errorf("install output = %q", out)
	}
	if !session.CheckPiHooksInstalled(extDir) {
		t.Fatal("extension not installed under PI_CODING_AGENT_DIR")
	}
	if _, err := os.Stat(session.PiHookExtensionPath(extDir)); err != nil {
		t.Fatalf("extension file missing: %v", err)
	}

	out = captureStdout(t, func() { handlePiHooks([]string{"status"}) })
	if !strings.Contains(out, "Status: INSTALLED") {
		t.Errorf("status after install = %q, want INSTALLED", out)
	}

	out = captureStdout(t, func() { handlePiHooks([]string{"install"}) })
	if !strings.Contains(out, "pi hooks are already installed.") {
		t.Errorf("second install output = %q", out)
	}

	out = captureStdout(t, func() { handlePiHooks([]string{"uninstall"}) })
	if !strings.Contains(out, "pi hooks removed successfully.") {
		t.Errorf("uninstall output = %q", out)
	}

	out = captureStdout(t, func() { handlePiHooks([]string{"uninstall"}) })
	if !strings.Contains(out, "No agent-deck pi hooks found to remove.") {
		t.Errorf("second uninstall output = %q", out)
	}
}

func TestHandlePiHooks_StatusReportsDrift(t *testing.T) {
	extDir := setupPiHooksTest(t)
	if _, err := session.InstallPiHooks(extDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(session.PiHookExtensionPath(extDir),
		[]byte("// AGENTDECK PI HOOK EXTENSION v0\n"), 0644); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "pi"), []byte("#!/bin/sh\necho 1.3.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")

	out := captureStdout(t, func() { handlePiHooks([]string{"status"}) })
	if !strings.Contains(out, "Status: OUTDATED") {
		t.Errorf("status with drift = %q, want OUTDATED", out)
	}
	// An outdated extension cannot serve the socket, whatever pi is.
	if want := "Socket: off (extension is v0, current v6)\n"; !strings.Contains(out, want) {
		t.Errorf("status with drift lacks %q:\n%s", want, out)
	}
}

// TestHandlePiHooks_HelpIsReadOnly: a help request anywhere in the argument
// list prints usage and touches nothing (#1993).
func TestHandlePiHooks_HelpIsReadOnly(t *testing.T) {
	extDir := setupPiHooksTest(t)

	for _, args := range [][]string{
		{"help"}, {"--help"}, {"-h"},
		{"install", "--help"}, {"uninstall", "-h"}, {"status", "help"},
	} {
		out := captureStdout(t, func() { handlePiHooks(args) })
		if !strings.Contains(out, "Usage: agent-deck pi-hooks <command>") {
			t.Errorf("pi-hooks %v printed no usage: %q", args, out)
		}
		if _, err := os.Stat(session.PiHookExtensionPath(extDir)); !os.IsNotExist(err) {
			t.Fatalf("pi-hooks %v wrote the extension file", args)
		}
	}
}

// TestPiHooksStatus_PrintsPiVersionAndSocketReason pins the two lines status
// gains in extension v5 (design A2, R4): the pi version as pi itself prints it
// and whether the wake socket is on, with the reason when it is off.
func TestPiHooksStatus_PrintsPiVersionAndSocketReason(t *testing.T) {
	short, err := os.MkdirTemp("/tmp", "ps")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })

	fakePi := func(t *testing.T, body string) string {
		t.Helper()
		bin := t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, "pi"), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+":/usr/bin:/bin")
		return bin
	}
	status := func(t *testing.T, install bool) string {
		t.Helper()
		setupPiHooksTest(t)
		t.Setenv("XDG_DATA_HOME", short)
		if install {
			captureStdout(t, func() { handlePiHooks([]string{"install"}) })
		}
		return captureStdout(t, func() { handlePiHooks([]string{"status"}) })
	}
	hasLine := func(out, line string) bool {
		for _, l := range strings.Split(out, "\n") {
			if l == line {
				return true
			}
		}
		return false
	}

	t.Run("on", func(t *testing.T) {
		bin := fakePi(t, "echo 1.3.0")
		out := status(t, true)
		if want := "Pi: 1.3.0 (via " + filepath.Join(bin, "pi") + " --version)"; !hasLine(out, want) {
			t.Errorf("status lacks the line %q:\n%s", want, out)
		}
		if !hasLine(out, "Socket: on") {
			t.Errorf("status lacks %q:\n%s", "Socket: on", out)
		}
	})

	t.Run("below the floor", func(t *testing.T) {
		bin := fakePi(t, "echo 1.1.0")
		out := status(t, true)
		if want := "Pi: 1.1.0 (via " + filepath.Join(bin, "pi") + " --version)"; !hasLine(out, want) {
			t.Errorf("status lacks the line %q:\n%s", want, out)
		}
		if want := "Socket: off (pi 1.1.0 is below 1.2.0)"; !hasLine(out, want) {
			t.Errorf("status lacks the line %q:\n%s", want, out)
		}
	})

	t.Run("probe failed", func(t *testing.T) {
		fakePi(t, "echo boom >&2; exit 3")
		out := status(t, true)
		found := false
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, "Socket: off (pi --version failed: ") && strings.HasSuffix(l, ")") {
				found = true
			}
		}
		if !found {
			t.Errorf("status lacks a %q line:\n%s", "Socket: off (pi --version failed: <err>)", out)
		}
	})

	t.Run("path too long", func(t *testing.T) {
		fakePi(t, "echo 1.3.0")
		long := filepath.Join(short, strings.Repeat("d", 60))
		setupPiHooksTest(t)
		t.Setenv("XDG_DATA_HOME", long)
		captureStdout(t, func() { handlePiHooks([]string{"install"}) })
		out := captureStdout(t, func() { handlePiHooks([]string{"status"}) })
		runtimeDir, err := agentpaths.ProfileRuntimeDir(os.Getenv("AGENTDECK_PROFILE"))
		if err != nil {
			t.Fatal(err)
		}
		n := len(filepath.Join(runtimeDir, "pi", "0123456789ab.sock"))
		if want := fmt.Sprintf("Socket: off (path %d bytes, limit 103)", n); !hasLine(out, want) {
			t.Errorf("status lacks the line %q:\n%s", want, out)
		}
	})

	t.Run("not installed", func(t *testing.T) {
		fakePi(t, "echo 1.3.0")
		out := status(t, false)
		if want := "Socket: off (extension not installed)"; !hasLine(out, want) {
			t.Errorf("status lacks the line %q:\n%s", want, out)
		}
	})
	// Precedence when more than one cause applies (design A2): extension not
	// installed, then pi below the floor or unreadable, then path too long.
	t.Run("not installed beats below the floor", func(t *testing.T) {
		fakePi(t, "echo 1.1.0")
		out := status(t, false)
		if want := "Socket: off (extension not installed)"; !hasLine(out, want) {
			t.Errorf("status lacks the line %q:\n%s", want, out)
		}
		if strings.Contains(out, "below 1.2.0") {
			t.Errorf("status names the version floor although the extension is not installed:\n%s", out)
		}
	})

	t.Run("below the floor beats path too long", func(t *testing.T) {
		fakePi(t, "echo 1.1.0")
		setupPiHooksTest(t)
		t.Setenv("XDG_DATA_HOME", filepath.Join(short, strings.Repeat("d", 60)))
		captureStdout(t, func() { handlePiHooks([]string{"install"}) })
		out := captureStdout(t, func() { handlePiHooks([]string{"status"}) })
		if want := "Socket: off (pi 1.1.0 is below 1.2.0)"; !hasLine(out, want) {
			t.Errorf("status lacks the line %q:\n%s", want, out)
		}
		if strings.Contains(out, "limit 103") {
			t.Errorf("status names the path limit although pi is below the floor:\n%s", out)
		}
	})

	// The profile is resolved the standard CLI way: -p wins over AGENTDECK_PROFILE.
	// The path length names the profile that was used. (Pinned on the
	// subcommand-level -p; the global `agent-deck -p X pi-hooks status` form is
	// consumed by main before dispatch, so main must hand handlePiHooks the
	// profile too, which a direct call cannot show.)
	t.Run("the -p flag wins over AGENTDECK_PROFILE", func(t *testing.T) {
		fakePi(t, "echo 1.3.0")
		setupPiHooksTest(t)
		t.Setenv("XDG_DATA_HOME", filepath.Join(short, strings.Repeat("d", 60)))
		t.Setenv("AGENTDECK_PROFILE", "p")
		captureStdout(t, func() { handlePiHooks([]string{"install"}) })
		const flagged = "a-considerably-longer-profile-name"
		out := captureStdout(t, func() { handlePiHooks([]string{"status", "-p", flagged}) })
		runtimeDir, err := agentpaths.ProfileRuntimeDir(flagged)
		if err != nil {
			t.Fatal(err)
		}
		n := len(filepath.Join(runtimeDir, "pi", "0123456789ab.sock"))
		if want := fmt.Sprintf("Socket: off (path %d bytes, limit 103)", n); !hasLine(out, want) {
			t.Errorf("status lacks the line %q (the -p profile's path):\n%s", want, out)
		}
	})
}

// TestPiHooksStatus_GlobalProfileFlagReachesTheHandler runs the real binary:
// `agent-deck -p X pi-hooks status` is consumed by main's global flag parsing
// before dispatch, and the profile must still reach the status probe. There is
// no in-process seam (dispatch is inline in main), so this is the lowest
// testable level. The socket path length names the profile that was used.
func TestPiHooksStatus_GlobalProfileFlagReachesTheHandler(t *testing.T) {
	bin := agentDeckBinaryAs(t, "agent-deck")
	short, err := os.MkdirTemp("/tmp", "ps")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	data := filepath.Join(short, strings.Repeat("d", 60))
	t.Setenv("XDG_DATA_HOME", data) // so agentpaths here computes what the child will

	piBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(piBin, "pi"), []byte("#!/bin/sh\necho 1.3.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	const flagged = "a-considerably-longer-profile-name"
	run := func(args ...string) string {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(),
			"HOME="+t.TempDir(), "XDG_DATA_HOME="+data, "AGENTDECK_PROFILE=p",
			"PI_CODING_AGENT_DIR="+filepath.Join(short, "pi"),
			"PATH="+piBin+":/usr/bin:/bin")
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	run("-p", flagged, "pi-hooks", "install")
	out := run("-p", flagged, "pi-hooks", "status")

	runtimeDir, err := agentpaths.ProfileRuntimeDir(flagged)
	if err != nil {
		t.Fatal(err)
	}
	n := len(filepath.Join(runtimeDir, "pi", "0123456789ab.sock"))
	if want := fmt.Sprintf("Socket: off (path %d bytes, limit 103)", n); !strings.Contains(out, want+"\n") {
		t.Errorf("`agent-deck -p %s pi-hooks status` lacks %q (the -p profile's path):\n%s", flagged, want, out)
	}
}

// main's extractor (main.go:1601-1608) accepts -p=value and --profile=value, so
// pi-hooks status takes them as the profile too, over AGENTDECK_PROFILE.
func TestPiHooksStatus_EqualsFormsAreAProfile(t *testing.T) {
	short, err := os.MkdirTemp("/tmp", "ps")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "pi"), []byte("#!/bin/sh\necho 1.3.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	setupPiHooksTest(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(short, strings.Repeat("d", 60)))
	t.Setenv("AGENTDECK_PROFILE", "p")
	captureStdout(t, func() { handlePiHooks([]string{"install"}) })

	const flagged = "a-considerably-longer-profile-name"
	runtimeDir, err := agentpaths.ProfileRuntimeDir(flagged)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("Socket: off (path %d bytes, limit 103)\n", len(filepath.Join(runtimeDir, "pi", "0123456789ab.sock")))
	for _, form := range []string{"-p=" + flagged, "--profile=" + flagged} {
		out := captureStdout(t, func() { handlePiHooks([]string{"status", form}) })
		if !strings.Contains(out, want) {
			t.Errorf("status %s lacks %q (the flagged profile's path, not AGENTDECK_PROFILE's):\n%s", form, want, out)
		}
	}
}

// Max ask: a permanent refusal stops socket wakes for a parent's records, and
// that is invisible unless status says so. pi-hooks status names the parent's
// reason: "socket wakes stopped: <reason>". A parent whose wakes work shows no
// such line.
func TestPiHooksStatus_SurfacesWhenAPermanentRefusalStoppedSocketWakes(t *testing.T) {
	stoppedLine := func(out string) string {
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(strings.ToLower(l), "socket wakes stopped") {
				return l
			}
		}
		return ""
	}
	t.Run("stopped", func(t *testing.T) {
		r, _ := enrolPiParent(t, "shipped the build")
		setupPiHooksTest(t)
		// What the send leg leaves after a permanent refusal: the records' wake
		// tries consumed, and a failed socket wake record naming the reason.
		if _, err := r.Do(piParentID, func(p comms.Pass) ([]events.Cursor, error) {
			var cs []events.Cursor
			for _, e := range p.Pending {
				cs = append(cs, e.Cursor)
			}
			p.File.NoteWakeFailed(cs)
			p.File.NoteWakeFailed(cs)
			return nil, nil
		}); err != nil {
			t.Fatal(err)
		}
		l, err := comms.Open("default")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		if _, _, err := l.Commit(comms.Record{Kind: comms.KindWake, From: "agent-deck", To: []string{piParentID}, Via: "socket",
			State: comms.StateFailed, Err: "bad_request", Trigger: "ledger", Text: "wake", TRecord: time.Now().UnixMilli()}); err != nil {
			t.Fatal(err)
		}
		out := captureStdout(t, func() { handlePiHooks([]string{"status"}) })
		line := stoppedLine(out)
		if line == "" || !strings.Contains(line, "bad_request") {
			t.Errorf("status does not say socket wakes stopped, with the reason:\n%s", out)
		}
	})
	t.Run("working", func(t *testing.T) {
		_, _ = enrolPiParent(t, "shipped the build")
		setupPiHooksTest(t)
		out := captureStdout(t, func() { handlePiHooks([]string{"status"}) })
		if line := stoppedLine(out); line != "" {
			t.Errorf("a parent whose wakes work shows %q", line)
		}
	})
}

// `pi-hooks status` probes pi on the launch's PATH, not this process's: a pi
// that only the spawn-path prelude's directories hold (~/.local/bin, which a
// non-login shell lacks) is the pi a launch runs, so status must report it
// instead of "not found". Wired through handlePiHooks so a status that goes
// back to the bare launch env fails here.
func TestPiHooksStatus_ProbesPiOnTheSpawnPathPrelude(t *testing.T) {
	short, err := os.MkdirTemp("/tmp", "ps")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })

	home := t.TempDir()
	t.Setenv("HOME", home)
	prelude := filepath.Join(home, ".local", "bin") // a spawn-path prelude dir
	if err := os.MkdirAll(prelude, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prelude, "pi"), []byte("#!/bin/sh\necho 1.3.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// This process's PATH has no pi, so only the prelude can resolve it.
	t.Setenv("PATH", t.TempDir()+":/usr/bin:/bin")
	if _, err := exec.LookPath("pi"); err == nil {
		t.Fatal("control: pi must not be on this process's PATH")
	}

	setupPiHooksTest(t)
	t.Setenv("XDG_DATA_HOME", short)
	captureStdout(t, func() { handlePiHooks([]string{"install"}) })
	out := captureStdout(t, func() { handlePiHooks([]string{"status"}) })

	want := "Pi: 1.3.0 (via " + filepath.Join(prelude, "pi") + " --version)"
	found := false
	for _, l := range strings.Split(out, "\n") {
		if l == want {
			found = true
		}
	}
	if !found {
		t.Errorf("status lacks the line %q (pi is only on the spawn-path prelude):\n%s", want, out)
	}
	if strings.Contains(out, "not found") {
		t.Errorf("status reports pi not found although the launch PATH has it:\n%s", out)
	}
}
