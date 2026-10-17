package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestConductorRecoveryWarningsThroughRegistry(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "claude")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nprintf '\\033[2J\\033[H❯ operator draft'\nwhile IFS= read -r line; do :; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	inst := session.NewInstanceWithTool("recovery-warning", dir, "claude")
	inst.Command, inst.IsConductor = command, true
	profile := fmt.Sprintf("_recovery_%d", time.Now().UnixNano())
	seedStore(t, profile, nil, inst)
	t.Cleanup(func() {
		for _, saved := range loadStore(t, profile) {
			if tm := saved.GetTmuxSession(); tm != nil {
				_ = tm.Kill()
			}
		}
	})
	registry := testRegistry(t, Deps{})
	check := func(result *Result) {
		t.Helper()
		if result.Err != nil {
			t.Fatal(result.Err)
		}
		if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "not sent") {
			t.Errorf("registry lost recovery warning: %#v", result.Warnings)
		}
		raw, err := json.Marshal(result.Out)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"warning":`) {
			t.Errorf("typed result lost recovery warning: %s", raw)
		}
		saved := findByTitle(loadStore(t, profile), inst.Title)
		if saved == nil || saved.LastStartedAt.IsZero() {
			t.Fatal("successful spawn state was not persisted")
		}
	}
	check(registry.Run(context.Background(), IDSessionStart, SessionStartIn{Profile: profile, Session: inst.ID, NoWait: true}))
	check(registry.Run(context.Background(), IDSessionRestart, SessionRestartIn{Profile: profile, Session: inst.ID, Force: true}))
	check(registry.Run(context.Background(), IDSessionRestart, SessionRestartIn{Profile: profile, All: true}))
}
