package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoOperandHelpPreservesExistingFiles(t *testing.T) {
	var commands [][]string
	for _, family := range []string{"hooks", "codex-hooks", "cursor-hooks", "gemini-hooks", "hermes-hooks"} {
		for _, action := range []string{"install", "uninstall", "status"} {
			for _, help := range []string{"help", "--help", "-h"} {
				commands = append(commands, []string{family, action, help})
			}
		}
	}
	for _, help := range []string{"help", "--help", "-h"} {
		commands = append(commands, []string{"costs", "sync", help})
	}
	for _, args := range commands {
		t.Run(strings.Join(args, "/"), func(t *testing.T) {
			home := t.TempDir()
			for _, dir := range []string{".claude", ".codex", ".cursor", ".gemini", ".hermes"} {
				target := filepath.Join(home, dir)
				if err := os.MkdirAll(target, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "settings.json"), []byte("{\"owner_marker\":true}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshotTree(t, home)
			out, err := runIssue2025Helper(t, home, args)
			if err != nil {
				t.Errorf("help failed: %v: %s", err, out)
			}
			if !strings.Contains(string(out), "Usage: agent-deck") {
				t.Errorf("no usage: %s", out)
			}
			if diff := mapKeyDiff(before, snapshotTree(t, home)); diff != "" {
				t.Errorf("help changed files: %s", diff)
			}
		})
	}
}

func TestCostsSyncRejectsUnexpectedOperands(t *testing.T) {
	home := t.TempDir()
	before := snapshotTree(t, home)
	out, err := runIssue2025Helper(t, home, []string{"costs", "sync", "unexpected"})
	if err == nil || !strings.Contains(string(out), "Usage: agent-deck costs sync") {
		t.Errorf("unexpected operand accepted: %v: %s", err, out)
	}
	if diff := mapKeyDiff(before, snapshotTree(t, home)); diff != "" {
		t.Errorf("invalid request changed files: %s", diff)
	}
}
