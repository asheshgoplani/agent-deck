package main

import (
	"bytes"
	"errors"
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
	type invocation struct {
		name string
		args []string
	}
	var invocations []invocation
	originalRunCommand := desktopNotificationsRunCommand
	desktopNotificationsRunCommand = func(name string, args ...string) ([]byte, error) {
		invocations = append(invocations, invocation{name: name, args: slices.Clone(args)})
		return nil, nil
	}
	t.Cleanup(func() { desktopNotificationsRunCommand = originalRunCommand })
	originalOS := desktopNotificationsOS
	desktopNotificationsOS = "darwin"
	t.Cleanup(func() { desktopNotificationsOS = originalOS })
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
		"<key>LSMultipleInstancesProhibited</key><true/>",
		"<key>NSPrincipalClass</key><string>NSApplication</string>",
	} {
		if !strings.Contains(string(plist), want) {
			t.Fatalf("Info.plist missing %q:\n%s", want, plist)
		}
	}
	if !slices.Contains(env, "AGENT_DECK_DESKTOP_BUNDLED=1") || !slices.Contains(env, "AGENT_DECK_DESKTOP_ROUTER="+router) {
		t.Fatalf("bundle relaunch env does not preserve router: %v", env)
	}
	bundle := filepath.Dir(filepath.Dir(filepath.Dir(executable)))
	wantInvocations := []invocation{
		{name: "codesign", args: []string{"--force", "--sign", "-", "--identifier", "com.agent-deck.desktop-notifications", bundle}},
		{name: "codesign", args: []string{"--verify", "--strict", bundle}},
		{name: "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister", args: []string{"-f", bundle}},
	}
	if !slices.EqualFunc(invocations, wantInvocations, func(got, want invocation) bool {
		return got.name == want.name && slices.Equal(got.args, want.args)
	}) {
		t.Fatalf("packaging commands = %#v, want %#v", invocations, wantInvocations)
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

func TestDesktopNotificationPackagingSkipsMacOSToolsOutsideDarwin(t *testing.T) {
	originalOS := desktopNotificationsOS
	desktopNotificationsOS = "linux"
	t.Cleanup(func() { desktopNotificationsOS = originalOS })
	originalRunCommand := desktopNotificationsRunCommand
	desktopNotificationsRunCommand = func(name string, args ...string) ([]byte, error) {
		t.Fatalf("unexpected command outside Darwin: %s %v", name, args)
		return nil, nil
	}
	t.Cleanup(func() { desktopNotificationsRunCommand = originalRunCommand })

	if err := signDesktopNotificationBundle("helper.app"); err != nil {
		t.Fatal(err)
	}
	if err := registerDesktopNotificationBundle("helper.app"); err != nil {
		t.Fatal(err)
	}
}

func TestRunDesktopNotificationHelperUsesNativeServeSeam(t *testing.T) {
	raw, err := os.ReadFile("desktop_notifications_cmd.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if !strings.Contains(source, "var desktopNotificationsNativeServe = desktopnotify.NativeServe") {
		t.Fatal("desktop notification helper has no injectable NativeServe seam")
	}
	if !strings.Contains(source, "desktopNotificationsNativeServe(helper.Serve)") {
		t.Fatal("runDesktopNotificationHelper does not route Helper.Serve through the NativeServe seam")
	}
}

func TestSignDesktopNotificationBundleRequiresStrictVerification(t *testing.T) {
	originalOS := desktopNotificationsOS
	desktopNotificationsOS = "darwin"
	t.Cleanup(func() { desktopNotificationsOS = originalOS })
	originalRunCommand := desktopNotificationsRunCommand
	call := 0
	desktopNotificationsRunCommand = func(_ string, _ ...string) ([]byte, error) {
		call++
		if call == 2 {
			return []byte("Info.plist is not bound"), errors.New("invalid signature")
		}
		return nil, nil
	}
	t.Cleanup(func() { desktopNotificationsRunCommand = originalRunCommand })

	err := signDesktopNotificationBundle("helper.app")
	if err == nil || !strings.Contains(err.Error(), "verify helper bundle signature") || !strings.Contains(err.Error(), "Info.plist is not bound") {
		t.Fatalf("signature readiness error = %v", err)
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
