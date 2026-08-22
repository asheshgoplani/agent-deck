package update

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLaunchdManagedJobsUseBootoutThenBootstrap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(dir, "com.agentdeck.conductor.plist")
	if err := os.WriteFile(plist, []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}
	origOS, origRun, origOutput := managedServicesGOOS, commandRun, commandOutput
	managedServicesGOOS = "darwin"
	var calls []string
	commandRun = func(name string, args ...string) error { calls = append(calls, name+" "+args[0]); return nil }
	commandOutput = func(string, ...string) ([]byte, error) { return nil, nil }
	t.Cleanup(func() { managedServicesGOOS, commandRun, commandOutput = origOS, origRun, origOutput })
	if _, err := RestartManagedServices(); err != nil {
		t.Fatal(err)
	}
	want := []string{"launchctl bootout", "launchctl bootstrap"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("launchd ordering = %v, want %v", calls, want)
	}
}

func TestLaunchdBootstrapFailureIsLoud(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "com.agentdeck.worker.plist"), []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}
	origOS, origRun, origOutput := managedServicesGOOS, commandRun, commandOutput
	managedServicesGOOS = "darwin"
	commandOutput = func(string, ...string) ([]byte, error) { return nil, nil }
	commandRun = func(_ string, args ...string) error {
		if args[0] == "bootstrap" {
			return errors.New("exit 78")
		}
		return nil
	}
	t.Cleanup(func() { managedServicesGOOS, commandRun, commandOutput = origOS, origRun, origOutput })
	_, err := RestartManagedServices()
	if err == nil || !strings.Contains(err.Error(), "EX_CONFIG") {
		t.Fatalf("failure was not loud/actionable: %v", err)
	}
}

func TestLaunchdBootoutFailureStopsBeforeBootstrap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "com.agentdeck.worker.plist"), []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}
	origOS, origRun, origOutput := managedServicesGOOS, commandRun, commandOutput
	managedServicesGOOS = "darwin"
	commandOutput = func(string, ...string) ([]byte, error) { return nil, nil }
	var calls []string
	commandRun = func(_ string, args ...string) error {
		calls = append(calls, args[0])
		if args[0] == "bootout" {
			return errors.New("operation not permitted")
		}
		return nil
	}
	t.Cleanup(func() { managedServicesGOOS, commandRun, commandOutput = origOS, origRun, origOutput })
	_, err := RestartManagedServices()
	if err == nil || !strings.Contains(err.Error(), "bootout failed") {
		t.Fatalf("bootout failure was not actionable: %v", err)
	}
	if want := []string{"bootout"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls after failed bootout = %v, want %v", calls, want)
	}
}
