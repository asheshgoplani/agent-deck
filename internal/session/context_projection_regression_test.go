package session

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportPortableContextProjectsBeforeReadableBudget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := filepath.Join(home, "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	project := filepath.Join(home, "project")
	const sid = "11111111-2222-3333-4444-555555555555"
	path := filepath.Join(config, "projects", ConvertToClaudeDirName(project), sid+".jsonl")
	conversation := strings.Join([]string{
		`{"sessionId":"` + sid + `","type":"user","message":{"role":"user","content":"hey how is it going"}}`,
		`{"sessionId":"` + sid + `","type":"assistant","message":{"role":"assistant","content":"I am well"}}`,
		`{"sessionId":"` + sid + `","type":"file-history-snapshot","metadata":{"private":"` + strings.Repeat("metadata-", 20000) + `"}}`,
	}, "\n") + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(conversation), 0o600); err != nil {
		t.Fatal(err)
	}
	export, err := ExportPortableContext(&Instance{Tool: "claude", ProjectPath: project, ClaudeSessionID: sid}, ContextExportOptions{MaxBytes: 256})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(export.Payload, []byte("hey how is it going")) || !bytes.Contains(export.Payload, []byte("I am well")) {
		t.Fatalf("readable conversation was lost behind metadata: %q", export.Payload)
	}
	if bytes.Contains(export.Payload, []byte("metadata-")) || export.Manifest.Artifact.Truncated {
		t.Fatalf("raw metadata leaked or metadata incorrectly consumed the readable budget: %+v %q", export.Manifest.Artifact, export.Payload)
	}
}

func TestExportContextRefusesNonemptyUnknownSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unknown.jsonl")
	const sid = "source-id"
	if err := os.WriteFile(path, []byte(`{"type":"session_meta","payload":{"id":"`+sid+`"}}`+"\n"+`{"type":"opaque_private_state","blob":"nonempty"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Native/raw export preserves a valid exact artifact even when it carries
	// only metadata. Portable export has the stricter cross-harness contract.
	if _, err := exportArtifact(path, ContextSourceIdentity{Tool: "codex", SessionID: sid}, "codex-rollout-jsonl", "native", 100, sid); err != nil {
		t.Fatalf("valid metadata-only native artifact was refused: %v", err)
	}
	_, err := exportPortableArtifact(path, ContextSourceIdentity{Tool: "codex", SessionID: sid}, "codex-rollout-jsonl", "native", 100, sid)
	if err == nil || !strings.Contains(err.Error(), "no readable user or assistant text") {
		t.Fatalf("nonempty unknown schema was accepted: %v", err)
	}
}

func TestReadableProjectionCodexClaudePiShapesAndTruncationDisclosure(t *testing.T) {
	for _, tc := range []struct {
		name, source, record, want string
	}{
		{"codex-to-claude", "codex", `{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"Codex greeting"}]}}`, "Codex greeting"},
		{"claude-to-pi", "claude", `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Claude response"}]}}`, "Claude response"},
		{"pi-to-codex", "pi", `{"type":"message","message":{"role":"user","content":[{"type":"text","text":"Pi greeting"}]}}`, "Pi greeting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projection, ok := projectPortableContext(tc.source, []byte(tc.record+"\n"))
			if !ok || !strings.Contains(projection, tc.want) {
				t.Fatalf("projection = %q, %v; want %q", projection, ok, tc.want)
			}
		})
	}
	payload, truncated := boundedReadableContextPayload([]byte("[USER]\n"+strings.Repeat("meaningful text ", 30)), 100)
	if !truncated || !bytes.Contains(payload, []byte("truncated")) || len(payload) > 100 {
		t.Fatalf("readable truncation disclosure = %q truncated=%v", payload, truncated)
	}
}
