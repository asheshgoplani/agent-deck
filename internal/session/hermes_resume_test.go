package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withFakeHermes puts a fake `hermes` executable (running the given /bin/sh
// body) at the front of PATH for the test, so captureHermesSessionID invokes it
// instead of a real hermes. HOME is already isolated by the package TestMain, so
// GetToolCommand("hermes") resolves to the bare name and PATH lookup wins.
func withFakeHermes(t *testing.T, shBody string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hermes"), []byte("#!/bin/sh\n"+shBody+"\n"), 0o755); err != nil {
		t.Fatalf("write fake hermes: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestCaptureHermesSessionID(t *testing.T) {
	t.Run("normal output returns the id", func(t *testing.T) {
		withFakeHermes(t, `printf 'Title Workspace LastActive ID\n--- --- --- ---\nFoo compass 1m 20260720_145826_0e92e7\n'`)
		if got := captureHermesSessionID("/some/dir"); got != "20260720_145826_0e92e7" {
			t.Errorf("capture = %q, want 20260720_145826_0e92e7", got)
		}
	})

	t.Run("non-zero exit returns empty (fresh restart, no block)", func(t *testing.T) {
		withFakeHermes(t, `exit 1`)
		if got := captureHermesSessionID(""); got != "" {
			t.Errorf("capture on hermes error = %q, want empty", got)
		}
	})

	t.Run("hang is bounded by the timeout", func(t *testing.T) {
		orig := hermesSessionsListTimeout
		hermesSessionsListTimeout = 200 * time.Millisecond
		t.Cleanup(func() { hermesSessionsListTimeout = orig })
		withFakeHermes(t, `sleep 10`)

		start := time.Now()
		got := captureHermesSessionID("")
		elapsed := time.Since(start)
		if got != "" {
			t.Errorf("capture on hang = %q, want empty", got)
		}
		if elapsed > 3*time.Second {
			t.Errorf("capture took %v — timeout did not bound the wedged binary", elapsed)
		}
	})
}

// Hermes mints a new session ID each launch and never exports it (env/hook), so
// agent-deck captures it from `hermes sessions list` and resumes with --resume.
// These tests cover the two pure pieces: parsing the list output and building
// the resume command.

func TestParseHermesSessionsLatestID(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{
			name: "typical list — first data row's ID (last column)",
			out: "Title                        Workspace          Last Active   ID\n" +
				"──────────────────────────────────────────────────────────────\n" +
				"Slack Triage Summary         compass            16m ago       20260720_143254_a3db50\n" +
				"—                            slackbot           20m ago       20260720_142828_287a92\n",
			want: "20260720_143254_a3db50",
		},
		{
			name: "row with dashes for empty title/workspace",
			out: "Title  Workspace  Last Active  ID\n" +
				"----------------------------------\n" +
				"—      —          yesterday    20260718_230951_14294a78\n",
			want: "20260718_230951_14294a78",
		},
		{"empty output", "", ""},
		{"header + separator only", "Title Workspace Last Active ID\n──────────────\n", ""},
		{"garbage last column ignored", "Title Workspace Last Active ID\n──────\nsome free text without an id\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseHermesSessionsLatestID(tc.out); got != tc.want {
				t.Errorf("parseHermesSessionsLatestID = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildHermesCommand_ResumeWhenSessionIDKnown(t *testing.T) {
	// Package TestMain isolates HOME, so LoadUserConfig sees a clean config.
	withID := &Instance{Tool: "hermes", Command: "hermes", HermesSessionID: "20260720_143254_a3db50"}
	cmd := withID.buildHermesCommand("hermes")
	if !strings.Contains(cmd, "--resume 20260720_143254_a3db50") {
		t.Errorf("resume command = %q, want it to contain `--resume 20260720_143254_a3db50`", cmd)
	}

	fresh := (&Instance{Tool: "hermes", Command: "hermes"}).buildHermesCommand("hermes")
	if strings.Contains(fresh, "--resume") {
		t.Errorf("fresh command = %q, must NOT contain --resume when no session id is known", fresh)
	}
}

func TestCanRestart_Hermes_ResumableWhileAlive(t *testing.T) {
	// The reported bug: a live hermes session couldn't be restarted (silent
	// no-op) because hermes had no resume support declared. Hermes does support
	// resume, so it must be restartable regardless of liveness.
	i := &Instance{Tool: "hermes", Status: StatusIdle}
	if !i.CanRestart() {
		t.Errorf("hermes CanRestart() = false, want true (hermes supports --resume)")
	}
	if !i.CanRestartFresh() {
		t.Errorf("hermes CanRestartFresh() = false, want true")
	}
}
