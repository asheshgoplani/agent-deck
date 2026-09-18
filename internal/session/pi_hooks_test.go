// pi_hooks_test.go covers the pi extension installer behind
// `agent-deck pi-hooks install|uninstall|status` (issue #2222).
package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPiExtensionsDir_HonorsPiConfigDirOverride(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "/custom/pi")
	if got, want := PiExtensionsDir(), filepath.Join("/custom/pi", "extensions"); got != want {
		t.Errorf("PiExtensionsDir() = %q, want %q", got, want)
	}

	t.Setenv("PI_CODING_AGENT_DIR", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := PiExtensionsDir(), filepath.Join(home, ".pi", "agent", "extensions"); got != want {
		t.Errorf("PiExtensionsDir() default = %q, want %q", got, want)
	}
}

// TestPiHookExtensionSource_IsWellFormed pins the contract the installer and
// `pi-hooks status` depend on: the shipped source must carry the version
// marker they parse, subscribe to exactly the four pi lifecycle events the
// status mapping knows, and no-op without AGENTDECK_INSTANCE_ID.
func TestPiHookExtensionSource_IsWellFormed(t *testing.T) {
	src := PiHookExtensionSource()

	if !strings.Contains(src, "AGENTDECK PI HOOK EXTENSION v1") {
		t.Error("extension source is missing the version marker the installer parses")
	}
	for _, event := range []string{"session_start", "turn_start", "turn_end", "session_shutdown"} {
		if !strings.Contains(src, `pi.on("`+event+`"`) {
			t.Errorf("extension source does not subscribe to %q", event)
		}
	}
	if !strings.Contains(src, "AGENTDECK_INSTANCE_ID") {
		t.Error("extension source must gate on AGENTDECK_INSTANCE_ID so it no-ops outside agent-deck")
	}
	if !strings.Contains(src, "agent-deck") || !strings.Contains(src, "hook-handler") {
		t.Error("extension source must emit through `agent-deck hook-handler`")
	}
}

func TestInstallPiHooks_InstallUpgradeUninstall(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "extensions")

	if state, _ := InspectPiHooks(dir); state != PiHooksNotInstalled {
		t.Fatalf("fresh dir state = %v, want PiHooksNotInstalled", state)
	}

	installed, err := InstallPiHooks(dir)
	if err != nil {
		t.Fatalf("InstallPiHooks: %v", err)
	}
	if !installed {
		t.Fatal("first install should report a write")
	}
	if !CheckPiHooksInstalled(dir) {
		t.Fatal("extension should read back as installed")
	}
	state, version := InspectPiHooks(dir)
	if state != PiHooksInstalled || version != PiHookExtensionVersion() {
		t.Fatalf("state/version = %v/%d, want PiHooksInstalled/%d", state, version, PiHookExtensionVersion())
	}

	// Idempotent: a second install is a no-op, not a rewrite.
	installed, err = InstallPiHooks(dir)
	if err != nil {
		t.Fatalf("second InstallPiHooks: %v", err)
	}
	if installed {
		t.Error("second install should report no write")
	}

	// A locally modified copy of OUR file is an upgrade, not a conflict.
	path := PiHookExtensionPath(dir)
	if err := os.WriteFile(path, []byte("// AGENTDECK PI HOOK EXTENSION v1\n// hand-edited\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if state, _ := InspectPiHooks(dir); state != PiHooksOutdated {
		t.Fatalf("modified copy state = %v, want PiHooksOutdated", state)
	}
	installed, err = InstallPiHooks(dir)
	if err != nil {
		t.Fatalf("upgrade InstallPiHooks: %v", err)
	}
	if !installed {
		t.Error("upgrading a drifted copy should report a write")
	}

	removed, err := RemovePiHooks(dir)
	if err != nil {
		t.Fatalf("RemovePiHooks: %v", err)
	}
	if !removed {
		t.Error("uninstall should report a removal")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("extension file still present after uninstall: %v", err)
	}

	removed, err = RemovePiHooks(dir)
	if err != nil {
		t.Fatalf("second RemovePiHooks: %v", err)
	}
	if removed {
		t.Error("uninstall with nothing installed should report no removal")
	}
}

// TestPiHooks_RefusesForeignFile: an agent-deck.ts somebody else wrote is
// never overwritten and never deleted.
func TestPiHooks_RefusesForeignFile(t *testing.T) {
	dir := t.TempDir()
	foreign := []byte("export default function () { /* not ours */ }\n")
	if err := os.WriteFile(PiHookExtensionPath(dir), foreign, 0644); err != nil {
		t.Fatal(err)
	}

	if state, _ := InspectPiHooks(dir); state != PiHooksForeign {
		t.Fatalf("state = %v, want PiHooksForeign", state)
	}
	if _, err := InstallPiHooks(dir); err == nil {
		t.Error("install over a foreign file should fail")
	}
	if _, err := RemovePiHooks(dir); err == nil {
		t.Error("uninstall of a foreign file should fail")
	}

	got, err := os.ReadFile(PiHookExtensionPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(foreign) {
		t.Error("foreign file was modified")
	}
}
