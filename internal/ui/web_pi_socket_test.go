package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The web surface (WebMutator.CreateSession, behind POST /api/sessions) starts
// a pi through the same launch as the TUI and CLI. With pi at the version floor
// and the extension installed, the stored row must say the launch exported the
// wake socket and launched pi: that pair is what enrols a listed pi parent as a
// ledger consumer, so a web-created pi that recorded neither would be woken by
// typing into its pane. Below the floor it exports nothing and records false.
func TestWebMutatorCreatePiSessionRecordsTheSocketLaunch(t *testing.T) {
	skipIfNoTmuxBinaryUI(t)
	for _, tc := range []struct {
		version  string
		exported bool
	}{{"1.3.0", true}, {"1.1.0", false}} {
		t.Run(tc.version, func(t *testing.T) {
			short, err := os.MkdirTemp("/tmp", "pw")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(short) })
			t.Setenv("XDG_DATA_HOME", short) // the socket path must fit sun_path
			t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
			session.ClearUserConfigCache()
			t.Cleanup(session.ClearUserConfigCache)
			if _, err := session.InstallPiHooks(session.PiExtensionsDir()); err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			fake := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo " + tc.version + "; exit 0; fi\nexec sleep 30\n"
			if err := os.WriteFile(filepath.Join(bin, "pi"), []byte(fake), 0o755); err != nil {
				t.Fatal(err)
			}

			h, storage := newHeadlessHomeForTest(t, fmt.Sprintf("_test_web_pi_socket_%d", time.Now().UnixNano()))
			id, err := NewWebMutator(h).CreateSession("web-pi", "pi", t.TempDir(), "", "", "")
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			instances, _, err := storage.LoadWithGroups()
			if err != nil {
				t.Fatal(err)
			}
			for _, inst := range instances {
				if inst.ID != id {
					continue
				}
				t.Cleanup(func() { _ = inst.Kill() })
				if inst.LaunchedTool != "pi" {
					t.Errorf("web-created pi holds LaunchedTool = %q, want \"pi\"", inst.LaunchedTool)
				}
				if inst.PiSocketExported != tc.exported {
					t.Errorf("web-created pi %s holds PiSocketExported = %v, want %v", tc.version, inst.PiSocketExported, tc.exported)
				}
				return
			}
			t.Fatalf("created session %s missing from storage", id)
		})
	}
}
