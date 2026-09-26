package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	deckterminal "github.com/asheshgoplani/agent-deck/internal/terminal"
)

func TestBuildRemoteAttachRequestCarriesTransport(t *testing.T) {
	withTempAgentDeckHome(t, `
[remotes.lab]
host = "alice@lab.example"
transport = "mosh"
mosh_server = "/opt/homebrew/bin/mosh-server"
`)
	req, ok := buildRemoteAttachRequest("lab", "id", "")
	if !ok || !req.Remote.UsesMosh() || req.Remote.MoshServer != "/opt/homebrew/bin/mosh-server" {
		t.Fatalf("request = %+v (ok=%v)", req.Remote, ok)
	}
	if command := deckterminal.BuildAttachCommand(req); !strings.Contains(command, "exec mosh ") {
		t.Fatalf("mosh remote rendered %q", command)
	}

	withTempAgentDeckHome(t, `
[remotes.lab]
host = "alice@lab.example"
transport = "mohs"
`)
	if req, ok := buildRemoteAttachRequest("lab", "id", ""); ok {
		t.Fatalf("mistyped transport resolved to %+v", req.Remote)
	}
}

// A mosh attach must be asked to quit, never killed: a killed mosh-client
// leaves mosh-server and its remote attach running. Close must still return
// at once, because the quit takes a network round trip.
func TestEmbeddedMoshCloseQuitsGracefully(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "signal")
	ready := filepath.Join(dir, "ready")
	fake := "#!/bin/sh\ntrap 'sleep 0.3; echo term > \"" + marker + "\"; exit 0' TERM\necho up > \"" + ready + "\"\nwhile :; do sleep 0.05; done\n"
	if err := os.WriteFile(filepath.Join(dir, "mosh"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	// The remote has mosh-server: the pre-attach probe succeeds.
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	term, err := startEmbeddedTerminalWithClipboard(context.Background(), deckterminal.AttachRequest{
		Name:   "id",
		Remote: &deckterminal.RemoteAttach{Host: "h", Transport: "mosh"},
	}, embeddedTerminalSize{Cols: 80, Rows: 24}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForTestFile(t, ready)

	start := time.Now()
	_ = term.Close()
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("Close blocked %v on the mosh quit", elapsed)
	}
	if got := waitForTestFile(t, marker); got != "term" {
		t.Fatalf("mosh saw %q, want SIGTERM", got)
	}
	select {
	case <-term.outputDone:
	case <-time.After(5 * time.Second):
		t.Fatal("teardown never finished after mosh exited")
	}
}

func waitForTestFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return strings.TrimSpace(string(data))
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s was never written", path)
	return ""
}
