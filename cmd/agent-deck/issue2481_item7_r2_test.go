package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIssue2481R2_UpdateShutdownRotatesLaunchdLog(t *testing.T) {
	_, _, dataDir, _ := setupTask6XDGEnv(t)
	path := filepath.Join(dataDir, "agent-deck", "logs", "auto-update.log")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate((8 << 20) + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	shutdown := initUpdateCommandLogging()
	shutdown()
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("update shutdown did not rotate stdout log: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("active log must reopen on next scheduled run: %v", err)
	}
}
