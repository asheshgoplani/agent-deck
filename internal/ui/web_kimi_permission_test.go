package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestWebMutatorCreateSessionKimiPermissionOverride(t *testing.T) {
	skipIfNoTmuxBinaryUI(t)
	for _, tc := range []struct {
		name           string
		override       string
		wantStoredMode string
		wantArgs       string
	}{
		{
			name:           "always ask overrides configured never ask",
			override:       session.KimiPermissionAlwaysAsk,
			wantStoredMode: session.KimiPermissionAlwaysAsk,
		},
		{
			name:     "omitted override preserves configured never ask",
			wantArgs: "--auto",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := session.SaveUserConfig(&session.UserConfig{
				Kimi: session.KimiSettings{PermissionMode: session.KimiPermissionNeverAsk},
			}); err != nil {
				t.Fatalf("SaveUserConfig: %v", err)
			}
			session.ClearUserConfigCache()
			t.Cleanup(session.ClearUserConfigCache)

			bin := t.TempDir()
			fake := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$KIMI_ARGS_FILE\"\nexec sleep 30\n"
			if err := os.WriteFile(filepath.Join(bin, "kimi"), []byte(fake), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			argsFile := filepath.Join(t.TempDir(), "args")
			t.Setenv("KIMI_ARGS_FILE", argsFile)

			h, storage := newHeadlessHomeForTest(t, fmt.Sprintf("_test_web_kimi_permission_%d", time.Now().UnixNano()))
			id, err := NewWebMutator(h).CreateSession("web-kimi", "kimi", t.TempDir(), "", "", "", tc.override)
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			instances, _, err := storage.LoadWithGroups()
			if err != nil {
				t.Fatal(err)
			}
			var created *session.Instance
			for _, inst := range instances {
				if inst.ID == id {
					created = inst
					break
				}
			}
			if created == nil {
				t.Fatalf("created session %s missing from storage", id)
			}
			t.Cleanup(func() { _ = created.Kill() })

			opts := created.GetKimiOptions()
			gotStoredMode := ""
			if opts != nil {
				gotStoredMode = opts.PermissionMode
			}
			if gotStoredMode != tc.wantStoredMode {
				t.Fatalf("stored Kimi permission mode = %q, want %q", gotStoredMode, tc.wantStoredMode)
			}

			deadline := time.Now().Add(5 * time.Second)
			var args []byte
			for {
				args, err = os.ReadFile(argsFile)
				if err == nil {
					break
				}
				if !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if time.Now().After(deadline) {
					t.Fatalf("fake Kimi did not record its arguments: %v", err)
				}
				time.Sleep(20 * time.Millisecond)
			}
			if got := strings.Join(strings.Fields(string(args)), " "); got != tc.wantArgs {
				t.Fatalf("fake Kimi args = %q, want %q", got, tc.wantArgs)
			}
		})
	}
}
