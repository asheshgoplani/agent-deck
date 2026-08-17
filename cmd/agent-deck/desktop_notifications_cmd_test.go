package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCopyDesktopHelperCreatesPrivateExecutable(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "agent-deck")
	if err := os.WriteFile(source, []byte("helper bytes"), 0o700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "bundle", "agent-deck-notifications")
	if err := os.Mkdir(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyDesktopHelper(source, destination); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "helper bytes" {
		t.Fatalf("copied bytes = %q", got)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %o, want 0700", info.Mode().Perm())
	}
}

func TestDesktopNotificationsDispatchAndDoctorRemediation(t *testing.T) {
	originalOS := desktopNotificationsOS
	desktopNotificationsOS = "darwin"
	t.Cleanup(func() { desktopNotificationsOS = originalOS })
	originalNativePresentationAvailable := desktopNotificationsNativePresentationAvailable
	desktopNotificationsNativePresentationAvailable = func() bool { return true }
	t.Cleanup(func() { desktopNotificationsNativePresentationAvailable = originalNativePresentationAvailable })
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(configRoot, "agent-deck"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "agent-deck", "config.toml"), []byte("[desktop_notifications]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out := captureDesktopNotificationsOutput(t, func() { handleDesktopNotifications([]string{"doctor"}) })
	for _, want := range []string{"desktop notifications: disabled", "remediation: add [desktop_notifications] enabled = true", "helper endpoint:", "action routing: ready"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out)
		}
	}
	help := captureDesktopNotificationsOutput(t, func() { handleDesktopNotifications(nil) })
	if !strings.Contains(help, "Usage: agent-deck desktop-notifications <doctor|helper>") {
		t.Fatalf("help output: %s", help)
	}
}

func TestDesktopNotificationsDoctorRejectsNonExecutableRouter(t *testing.T) {
	originalOS := desktopNotificationsOS
	desktopNotificationsOS = "darwin"
	t.Cleanup(func() { desktopNotificationsOS = originalOS })
	originalNative := desktopNotificationsNativePresentationAvailable
	desktopNotificationsNativePresentationAvailable = func() bool { return true }
	t.Cleanup(func() { desktopNotificationsNativePresentationAvailable = originalNative })
	router := filepath.Join(t.TempDir(), "agent-deck")
	if err := os.WriteFile(router, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalExecutable := desktopNotificationsExecutable
	desktopNotificationsExecutable = func() (string, error) { return router, nil }
	t.Cleanup(func() { desktopNotificationsExecutable = originalExecutable })
	out := captureDesktopNotificationsOutput(t, desktopNotificationsDoctor)
	if !strings.Contains(out, "action routing: unavailable") || strings.Contains(out, "action routing: ready") {
		t.Fatalf("doctor accepted non-executable router:\n%s", out)
	}
}

func TestPrepareDesktopNotificationBundlePreservesMetadataAndRouter(t *testing.T) {
	dir := t.TempDir()
	router := filepath.Join(dir, "agent-deck")
	if err := os.WriteFile(router, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	executable, env, err := prepareDesktopNotificationBundle(router, dir)
	if err != nil {
		t.Fatal(err)
	}
	plist, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(executable)), "Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<key>CFBundleExecutable</key><string>" + filepath.Base(executable) + "</string>",
		"<key>CFBundleIdentifier</key><string>com.agent-deck.desktop-notifications</string>",
	} {
		if !strings.Contains(string(plist), want) {
			t.Fatalf("Info.plist missing %q:\n%s", want, plist)
		}
	}
	if !slices.Contains(env, "AGENT_DECK_DESKTOP_BUNDLED=1") || !slices.Contains(env, "AGENT_DECK_DESKTOP_ROUTER="+router) {
		t.Fatalf("bundle relaunch env does not preserve router: %v", env)
	}
}

func TestDesktopNotificationsRejectHelperOutsideMacOS(t *testing.T) {
	originalOS := desktopNotificationsOS
	desktopNotificationsOS = "linux"
	t.Cleanup(func() { desktopNotificationsOS = originalOS })
	if err := desktopNotificationHelperPrerequisite(); err == nil || !strings.Contains(err.Error(), "macOS only") {
		t.Fatalf("non-macOS helper prerequisite = %v, want macOS-only error", err)
	}
}

func captureDesktopNotificationsOutput(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = original }()
	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	return out.String()
}
