package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestQueueOpenCodePrompt exercises the real CLI subprocess boundary with an
// offline launcher fixture, including admission receipts and ambiguous failures.
func TestQueueOpenCodePrompt(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		exit           string
		wantErr        bool
	}{
		{"accepted", `{"data":{"id":"msg_queued","sessionID":"ses_target","delivery":"queue"}}`, "0", false},
		{"wrong-session", `{"data":{"id":"msg_queued","sessionID":"ses_other","delivery":"queue"}}`, "0", true},
		{"steered", `{"data":{"id":"msg_queued","sessionID":"ses_target","delivery":"steer"}}`, "0", true},
		{"missing-receipt", `{"data":{}}`, "0", true},
		{"invalid-json", `invalid`, "0", true},
		{"cli-error", `private draft`, "1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateOpenCodeConfig(t, "opencode")
			dir := t.TempDir()
			launcher := filepath.Join(dir, "opencode2")
			argsFile := filepath.Join(dir, "args")
			t.Setenv("QUEUE_ARGS", argsFile)
			t.Setenv("QUEUE_RESPONSE", tc.response)
			t.Setenv("QUEUE_EXIT", tc.exit)
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$QUEUE_ARGS\"\nprintf '%s\\n' \"$QUEUE_RESPONSE\"\nexit \"$QUEUE_EXIT\"\n"
			if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			i := NewInstanceWithGroupAndTool("queue", dir, "", "opencode2")
			i.Command = launcher
			i.OpenCodeSessionID = "ses_target"
			text := "first\nsecond 'quoted' ; $(do not execute)"
			err := i.QueueOpenCodePrompt(context.Background(), text)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "private draft") {
				t.Fatal("error leaked draft")
			}
			data, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			args := strings.SplitN(strings.TrimSuffix(string(data), "\n"), "\n", 6)
			if len(args) != 6 || strings.Join(args[:5], " ") != "api session.prompt --param sessionID=ses_target --data" {
				t.Fatalf("args = %q", args)
			}
			var payload struct{ Text, Delivery string }
			if err := json.Unmarshal([]byte(args[5]), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Text != text || payload.Delivery != "queue" {
				t.Fatalf("payload = %+v", payload)
			}
		})
	}
}

// TestQueueOpenCodePromptRequiresIdentity rejects unknown recipients before exec.
func TestQueueOpenCodePromptRequiresIdentity(t *testing.T) {
	for _, id := range []string{"", "other", "ses_target\nother"} {
		i := &Instance{Tool: "opencode", Command: "/nonexistent/opencode2", OpenCodeSessionID: id}
		if err := i.QueueOpenCodePrompt(context.Background(), "message"); err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("identity %q: %v", id, err)
		}
	}
	for _, i := range []*Instance{nil, {Tool: "shell"}} {
		if err := i.QueueOpenCodePrompt(context.Background(), "message"); err == nil {
			t.Fatal("non-OpenCode recipient accepted")
		}
	}
}
