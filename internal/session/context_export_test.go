package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContextExportClaudeRetainsExactSeededContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := filepath.Join(home, ".claude-export")
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	project := filepath.Join(home, "project")
	sid := "11111111-2222-3333-4444-555555555555"
	path := filepath.Join(config, "projects", ConvertToClaudeDirName(project), sid+".jsonl")
	seed := []byte(`{"sessionId":"11111111-2222-3333-4444-555555555555","type":"user","message":{"role":"user","content":"seeded context"}}` + "\n")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, seed, 0600); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{Tool: "claude", ProjectPath: project, ClaudeSessionID: sid}

	export, err := ExportClaudeContext(inst, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(export.Payload, seed) || !bytes.Contains(export.Payload, []byte("seeded context")) {
		t.Fatalf("native exact context was not retained: %q", export.Payload)
	}
	if export.Manifest.Source.SessionID != sid || export.Manifest.Continuity != "native" {
		t.Fatalf("wrong source manifest: %+v", export.Manifest)
	}
	transferred, err := MarkContextTransferred(export, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if transferred.Manifest.Continuity != "transferred" || transferred.Manifest.TargetTool != "codex" || len(transferred.Manifest.LossDisclosure) == 0 {
		t.Fatalf("cross-harness label missing: %+v", transferred.Manifest)
	}
	if export.Manifest.Continuity != "native" {
		t.Fatal("marking a transfer mutated the native source manifest")
	}
	hash := sha256.Sum256(seed)
	if export.Manifest.Artifact.SourceSHA256 != hex.EncodeToString(hash[:]) || export.Manifest.Artifact.Truncated {
		t.Fatalf("source hash/truncation mismatch: %+v", export.Manifest.Artifact)
	}
}

func TestContextExportRefusesClaudeWrongIdentityAndNewestFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := filepath.Join(home, ".claude-export")
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	project := filepath.Join(home, "project")
	wanted := "11111111-2222-3333-4444-555555555555"
	other := "22222222-3333-4444-5555-666666666666"
	dir := filepath.Join(config, "projects", ConvertToClaudeDirName(project))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, other+".jsonl"), []byte(`{"sessionId":"`+other+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := ExportClaudeContext(&Instance{Tool: "claude", ProjectPath: project, ClaudeSessionID: wanted}, 0)
	if err == nil {
		t.Fatal("export selected a newest or neighbouring transcript")
	}
}

func TestContextExportClaudeUsesOnlyItsBoundAccountForDuplicatedNativeID(t *testing.T) {
	home := withTempAgentDeckHome(t, `
[profiles.personal.claude]
config_dir = "~/.claude-personal"
[profiles.work.claude]
config_dir = "~/.claude-work"
`)
	project := filepath.Join(home, "project")
	const sid = "11111111-2222-3333-4444-555555555555"
	personal := filepath.Join(home, ".claude-personal", "projects", ConvertToClaudeDirName(project), sid+".jsonl")
	work := filepath.Join(home, ".claude-work", "projects", ConvertToClaudeDirName(project), sid+".jsonl")
	for path, payload := range map[string]string{
		personal: `{"sessionId":"` + sid + `","message":"personal source"}` + "\n",
		work:     `{"sessionId":"` + sid + `","message":"work copy"}` + "\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	inst := &Instance{Tool: "claude", Account: "personal", ProjectPath: project, ClaudeSessionID: sid}
	export, err := ExportClaudeContext(inst, 0)
	if err != nil {
		t.Fatal(err)
	}
	if export.Manifest.Artifact.Path != personal || !bytes.Contains(export.Payload, []byte("personal source")) {
		t.Fatalf("export used another account's duplicate: %#v %q", export.Manifest.Artifact, export.Payload)
	}
	if err := os.Remove(personal); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportClaudeContext(inst, 0); err == nil || !strings.Contains(err.Error(), "no exact context artifact") {
		t.Fatalf("missing personal source borrowed work's duplicate: %v", err)
	}
}

func TestContextExportClaudeRefusesProjectFallbackAndSymlinkSource(t *testing.T) {
	home := withTempAgentDeckHome(t, `
[profiles.personal.claude]
config_dir = "~/.claude-personal"
`)
	project := filepath.Join(home, "display-project")
	workingDir := filepath.Join(home, "actual-launch-cwd")
	const sid = "11111111-2222-3333-4444-555555555555"
	config := filepath.Join(home, ".claude-personal")
	projectArtifact := filepath.Join(config, "projects", ConvertToClaudeDirName(project), sid+".jsonl")
	if err := os.MkdirAll(filepath.Dir(projectArtifact), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectArtifact, []byte(fmt.Sprintf("{\"sessionId\":%q}\n", sid)), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{Tool: "claude", Account: "personal", ProjectPath: project, MultiRepoEnabled: true, MultiRepoTempDir: workingDir, ClaudeSessionID: sid}
	if _, err := ExportClaudeContext(inst, 0); err == nil {
		t.Fatal("raw ProjectPath artifact was accepted despite a distinct effective working directory")
	}
	canonical := filepath.Join(config, "projects", ConvertToClaudeDirName(workingDir), sid+".jsonl")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(projectArtifact, canonical); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportClaudeContext(inst, 0); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked canonical source was accepted: %v", err)
	}
}

func TestContextExportRejectsMalformedPrefixBeforeLaterForeignIdentity(t *testing.T) {
	wanted := "11111111-2222-3333-4444-555555555555"
	foreign := "22222222-3333-4444-5555-666666666666"
	data := []byte(`{"sessionId":` + "\n" + `{"sessionId":"` + foreign + `"}` + "\n")
	if err := validateJSONLIdentity(data, wanted); err == nil {
		t.Fatal("malformed non-tail JSONL prefix hid a later foreign session identity")
	}
}

func TestContextExportOnlyToleratesAnIncompleteFinalJSONLLine(t *testing.T) {
	wanted := "11111111-2222-3333-4444-555555555555"
	data := []byte(`{"sessionId":"` + wanted + `"}` + "\n" + `{"sessionId":`)
	if err := validateJSONLIdentity(data, wanted); err != nil {
		t.Fatalf("unterminated final append should be tolerated: %v", err)
	}
	if err := validateJSONLIdentity(append(data, '\n'), wanted); err == nil {
		t.Fatal("terminated malformed final JSONL record was accepted")
	}
}

func TestContextExportPiRetainsSeededContextAndRefusesWrongID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".pi", "agent-deck", "pi-instance")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	seed := []byte(`{"type":"session","version":3,"id":"pi-exact"}` + "\n" + `{"type":"message","id":"m1","parentId":null,"message":{"role":"user","content":"seeded Pi context"}}` + "\n")
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, seed, 0600); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{ID: "pi-instance", Tool: "pi", ProjectPath: filepath.Join(home, "project")}
	export, err := ExportPiContext(inst, "pi-exact", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(export.Payload, []byte("seeded Pi context")) || export.Manifest.Source.SessionID != "pi-exact" {
		t.Fatalf("Pi context or identity lost: %+v %q", export.Manifest, export.Payload)
	}
	if _, err := ExportPiContext(inst, "wrong-id", 0); err == nil {
		t.Fatal("Pi adapter accepted a wrong session identity")
	}
}

func TestContextExportPiResolvesTimestampedFileAndKeepsPersistedExactSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "project")
	dir := filepath.Join(home, ".pi", "agent-deck", "pi-timestamped")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dir, "2026-01-02T03-04-05-006Z_01999999-9999-7999-8999-999999999901.jsonl")
	if err := os.WriteFile(first, []byte(`{"type":"session","version":3,"id":"pi-first","cwd":"`+project+`"}`+"\n"+`{"type":"message","id":"u1","parentId":null,"message":{"role":"user","content":"first exact source"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{ID: "pi-timestamped", Tool: "pi", ProjectPath: project}
	export, err := ExportContext(inst, ContextExportOptions{})
	if err != nil || export.Manifest.Artifact.Path != first || export.Manifest.Source.SessionID != "pi-first" {
		t.Fatalf("timestamped Pi export = %#v, %v", export, err)
	}
	inst.PiSessionID, inst.PiSessionPath = "pi-first", first
	second := filepath.Join(dir, "2026-01-02T03-05-05-006Z_01999999-9999-7999-8999-999999999902.jsonl")
	if err := os.WriteFile(second, []byte(`{"type":"session","version":3,"id":"pi-second","cwd":"`+project+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	export, err = ExportContext(inst, ContextExportOptions{})
	if err != nil || export.Manifest.Artifact.Path != first || !bytes.Contains(export.Payload, []byte("first exact source")) {
		t.Fatalf("persisted Pi source changed after second file: %#v, %v", export, err)
	}
}

func TestContextExportPiRefusesAmbiguousForeignMalformedAndSymlinkArtifacts(t *testing.T) {
	for _, tc := range []struct {
		name string
		seed func(t *testing.T, dir, project string)
	}{
		{"ambiguous", func(t *testing.T, dir, project string) {
			for _, name := range []string{"one.jsonl", "two.jsonl"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"type":"session","id":"`+name+`","cwd":"`+project+`"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"foreign cwd", func(t *testing.T, dir, _ string) {
			if err := os.WriteFile(filepath.Join(dir, "foreign.jsonl"), []byte(`{"type":"session","id":"foreign","cwd":"/other"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"malformed header", func(t *testing.T, dir, _ string) {
			if err := os.WriteFile(filepath.Join(dir, "bad.jsonl"), []byte(`{"type":"session","id":`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink", func(t *testing.T, dir, project string) {
			outside := filepath.Join(filepath.Dir(dir), "outside.jsonl")
			if err := os.WriteFile(outside, []byte(`{"type":"session","id":"outside","cwd":"`+project+`"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(dir, "linked.jsonl")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			project := filepath.Join(home, "project")
			dir := filepath.Join(home, ".pi", "agent-deck", "pi-refuse")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			tc.seed(t, dir, project)
			if _, err := ExportContext(&Instance{ID: "pi-refuse", Tool: "pi", ProjectPath: project}, ContextExportOptions{}); err == nil {
				t.Fatal("unsafe Pi artifact set was accepted")
			}
		})
	}
}

func TestContextExportCodexExactIDAndTruncationDisclosure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexHome := filepath.Join(home, ".codex-export")
	t.Setenv("CODEX_HOME", codexHome)
	sid := "33333333-4444-5555-6666-777777777777"
	path := filepath.Join(codexHome, "sessions", "2026", "09", "10", "rollout-20260910-"+sid+".jsonl")
	seed := []byte(`{"type":"session_meta","payload":{"id":"` + sid + `"}}` + "\n" + `{"type":"event","text":"seeded Codex context"}` + "\n")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, seed, 0600); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{Tool: "codex", ProjectPath: filepath.Join(home, "project"), CodexSessionID: sid}
	export, err := ExportCodexContext(inst, 32)
	if err != nil {
		t.Fatal(err)
	}
	if !export.Manifest.Artifact.Truncated || len(export.Payload) > 32 || len(export.Manifest.LossDisclosure) == 0 {
		t.Fatalf("truncation was not disclosed: %+v payload=%d", export.Manifest, len(export.Payload))
	}
}
