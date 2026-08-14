package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/asheshgoplani/agent-deck/internal/childenv"
	"github.com/asheshgoplani/agent-deck/internal/desktopnotify"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

var desktopNotificationsOS = runtime.GOOS
var desktopNotificationsNativePresentationAvailable = desktopnotify.NativePresentationAvailable

func handleDesktopNotifications(args []string) {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Println("Usage: agent-deck desktop-notifications <doctor|helper>")
		fmt.Println()
		fmt.Println("Manage macOS actionable desktop notifications.")
		return
	}
	switch args[0] {
	case "doctor":
		desktopNotificationsDoctor()
	case "helper":
		runDesktopNotificationHelper()
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown desktop-notifications command: %s\n", args[0])
		os.Exit(1)
	}
}

func desktopNotificationsDoctor() {
	if desktopNotificationsOS != "darwin" {
		fmt.Println("desktop notifications: unavailable (macOS only)")
		return
	}
	settings := session.GetDesktopNotificationsSettings()
	socket, socketErr := session.DesktopNotificationSocketPath()
	binary, binaryErr := os.Executable()
	fmt.Printf("desktop notifications: %s\n", map[bool]string{true: "enabled", false: "disabled"}[settings.Enabled])
	if !settings.Enabled {
		fmt.Println("remediation: add [desktop_notifications] enabled = true to config.toml")
	}
	if !desktopNotificationsNativePresentationAvailable() {
		fmt.Println("native presentation: unavailable (this Agent Deck build requires cgo on macOS)")
		fmt.Println("remediation: install or rebuild Agent Deck for macOS with cgo enabled")
		return
	}
	if socketErr != nil {
		fmt.Printf("helper endpoint: unavailable (%v)\n", socketErr)
	} else if desktopnotify.EndpointHealthy(socket) {
		fmt.Printf("helper endpoint: healthy (%s)\n", socket)
	} else {
		fmt.Printf("helper endpoint: unavailable (%s)\n", socket)
		fmt.Println("remediation: run agent-deck desktop-notifications helper from your logged-in macOS session")
	}
	if binaryErr != nil {
		fmt.Printf("action routing: unavailable (%v)\n", binaryErr)
	} else if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() {
		fmt.Printf("action routing: unavailable (%v)\n", err)
	} else {
		fmt.Printf("action routing: ready (%s session focus <id> --attach)\n", binary)
	}
	if _, err := exec.LookPath("terminal-notifier"); err == nil {
		fmt.Println("coexistence: terminal-notifier is installed; disable its user-managed scripts after verifying this feature to avoid duplicate banners")
	}
}

func runDesktopNotificationHelper() {
	if err := desktopNotificationHelperPrerequisite(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if os.Getenv("AGENT_DECK_DESKTOP_BUNDLED") != "1" {
		if err := relaunchDesktopNotificationHelperBundled(); err != nil {
			fmt.Fprintf(os.Stderr, "desktop notification helper: %v\n", err)
			os.Exit(1)
		}
		return
	}
	socket, err := session.DesktopNotificationSocketPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "desktop notification helper: %v\n", err)
		os.Exit(1)
	}
	state, err := session.DesktopNotificationStatePath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "desktop notification helper: %v\n", err)
		os.Exit(1)
	}
	listener, err := desktopnotify.Listen(socket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "desktop notification helper: %v\n", err)
		os.Exit(1)
	}
	defer listener.Close()
	store, err := desktopnotify.OpenStore(state)
	if err != nil {
		fmt.Fprintf(os.Stderr, "desktop notification helper: %v\n", err)
		os.Exit(1)
	}
	if err := (desktopnotify.Helper{Listener: listener, Store: store, Present: desktopnotify.NativePresent}).Serve(); err != nil {
		fmt.Fprintf(os.Stderr, "desktop notification helper: %v\n", err)
		os.Exit(1)
	}
}

func desktopNotificationHelperPrerequisite() error {
	if desktopNotificationsOS != "darwin" {
		return errors.New("desktop notification helper is supported on macOS only")
	}
	if !desktopNotificationsNativePresentationAvailable() {
		return errors.New("desktop notification helper requires a macOS build with cgo enabled; install or rebuild Agent Deck with cgo enabled")
	}
	return nil
}

// relaunchDesktopNotificationHelperBundled gives UserNotifications a genuine
// NSBundle. Apple rejects notification-center requests from a bare Go binary
// ("bundleProxyForCurrentProcess is nil"). The copied helper is a managed,
// per-user background app; AGENT_DECK_DESKTOP_ROUTER preserves the original
// installed Agent Deck path for notification click actions.
func relaunchDesktopNotificationHelperBundled() error {
	router, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve Agent Deck executable: %w", err)
	}
	dataDir, err := session.GetAgentDeckDir()
	if err != nil {
		return fmt.Errorf("resolve helper directory: %w", err)
	}
	bundle := filepath.Join(dataDir, "desktop-notifications", "Agent Deck Notifications.app")
	contents := filepath.Join(bundle, "Contents")
	macOSDir := filepath.Join(contents, "MacOS")
	if err := os.MkdirAll(macOSDir, 0o700); err != nil {
		return fmt.Errorf("create helper bundle: %w", err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleDevelopmentRegion</key><string>en</string>
<key>CFBundleExecutable</key><string>agent-deck-notifications</string>
<key>CFBundleIdentifier</key><string>com.agent-deck.desktop-notifications</string>
<key>CFBundleName</key><string>Agent Deck Notifications</string>
<key>LSBackgroundOnly</key><true/>
</dict></plist>
`
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0o600); err != nil {
		return fmt.Errorf("write helper bundle metadata: %w", err)
	}
	bundledExecutable := filepath.Join(macOSDir, "agent-deck-notifications")
	if err := copyDesktopHelper(router, bundledExecutable); err != nil {
		return err
	}
	env := append(childenv.ForLaunch(""), "AGENT_DECK_DESKTOP_BUNDLED=1", "AGENT_DECK_DESKTOP_ROUTER="+router)
	return syscall.Exec(bundledExecutable, []string{bundledExecutable, "desktop-notifications", "helper"}, env)
}

func copyDesktopHelper(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open Agent Deck executable: %w", err)
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".agent-deck-notifications-*")
	if err != nil {
		return fmt.Errorf("create helper executable: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("copy helper executable: %w", err)
	}
	if err := tmp.Chmod(0o700); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("make helper executable: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close helper executable: %w", err)
	}
	if err := os.Rename(tmpName, destination); err != nil {
		return fmt.Errorf("install helper executable: %w", err)
	}
	return nil
}
