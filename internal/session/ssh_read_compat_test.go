package session

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Run the actual single-channel capability gate against a legacy core.
// Old usage must never reach JSON stdout and no unsupported read may start.
func TestRunReadIOLegacyGate(t *testing.T) {
	for _, args := range [][]string{
		{"recall", "timeline", "id", "--json", "--tail", "100"},
		{"recall", "timeline", "id", "--json", "--before", "cursor", "--limit", "100"},
		{"recall", "follow", "id", "--after", "cursor", "--jsonl"},
		{"events", "follow", "--jsonl", "--since", "100"},
		{"session", "send-status", "send-id", "--json"},
	} {
		t.Run(args[0]+"-"+args[1], func(t *testing.T) {
			dir := t.TempDir()
			shim := "#!/bin/sh\nfor arg do command=$arg; done\nexec sh -c \"$command\"\n"
			if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(shim), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			marker := filepath.Join(dir, "read-started")
			core := filepath.Join(dir, "legacy core's binary")
			script := "#!/bin/sh\nfor arg do\n if [ \"$arg\" = --help ]; then\n  printf 'Usage: old core\\n'\n  printf 'flag provided but not defined: newer option\\n' >&2\n  exit 2\n fi\ndone\nprintf 'unexpected read' > " + shellQuote(marker) + "\n"
			if err := os.WriteFile(core, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			input, inputWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer inputWriter.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			runner := &SSHRunner{Host: "fixture", AgentDeckPath: core, Profile: "fixture-profile"}
			want := []byte("{\"error\":\"unsupported remote command\",\"remote\":\"fixture\",\"remote_version\":\"unknown\"}\n")
			var out, diagnostic bytes.Buffer
			err = runner.RunReadIO(ctx, input, &out, &diagnostic, want, args[1] == "follow", args...)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || !bytes.Equal(out.Bytes(), want) || diagnostic.Len() != 0 {
				t.Fatalf("legacy refusal err=%v stdout=%q stderr=%q", err, out.String(), diagnostic.String())
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unsupported read started: %v", err)
			}
		})
	}
}
