package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortableContextProjection_ExportsOnlyConversationText(t *testing.T) {
	payload := []byte(strings.Join([]string{
		`{"type":"system","message":{"role":"system","content":"global instruction must not transfer"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"current user task: preserve this fact"},{"type":"image","source":{"data":"attachment"}}]},"account":{"email":"do-not-transfer"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"private"},{"type":"text","text":"assistant response"}],"bridge":{"token":"do-not-transfer"}}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"tool state"}]}}`,
	}, "\n"))
	got := portableContextProjection("claude", payload)
	for _, want := range []string{"current user task: preserve this fact", "assistant response"} {
		if !strings.Contains(got, want) {
			t.Fatalf("projection missing %q: %s", want, got)
		}
	}
	for _, forbidden := range []string{"global instruction", "attachment", "do-not-transfer", "private", "tool state"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("projection leaked %q: %s", forbidden, got)
		}
	}
}

func TestPortableContextProjection_PiMessageSchemaExportsOnlyNestedConversationText(t *testing.T) {
	payload := []byte(strings.Join([]string{
		`{"type":"session","version":3,"id":"pi-native","cwd":"/workspace/repo"}`,
		`{"type":"message","id":"u1","parentId":null,"message":{"role":"user","content":[{"type":"text","text":"Pi user context"}]}}`,
		`{"type":"message","id":"a1","parentId":"u1","message":{"role":"assistant","content":[{"type":"thinking","thinking":"private"},{"type":"text","text":"Pi assistant context"}]}}`,
		`{"type":"message","id":"tool1","parentId":"a1","message":{"role":"user","content":[{"type":"toolResult","content":"private tool output"}]}}`,
		`{"type":"message","id":"tool2","parentId":"tool1","message":{"role":"assistant","content":[{"type":"toolCall","name":"read"}]}}`,
	}, "\n"))
	got := portableContextProjection("pi", payload)
	for _, want := range []string{"Pi user context", "Pi assistant context"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Pi projection missing %q: %s", want, got)
		}
	}
	for _, forbidden := range []string{"private", "tool output", "toolCall", "pi-native"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("Pi projection leaked %q: %s", forbidden, got)
		}
	}
}

func TestCrossHarnessPayloadStage_BoundsCommandForLargeLiteralPayloads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	for _, tc := range []struct{ source, target string }{
		{"claude", "codex"}, {"claude", "pi"}, {"codex", "claude"},
		{"codex", "pi"}, {"pi", "claude"}, {"pi", "codex"},
	} {
		t.Run(tc.source+"-to-"+tc.target, func(t *testing.T) {
			for _, size := range []int{32 * 1024, 128 * 1024} {
				export := syntheticTransferExport(tc.source)
				export.Manifest.Source.SessionID = fmt.Sprintf("source-session-%d", size)
				export.Manifest.Source.Account = "exact-source-account"
				plan, err := BuildFreshTargetLaunchPlan(export, FreshTargetLaunchOptions{TargetHarness: tc.target})
				if err != nil {
					t.Fatal(err)
				}
				if plan.Source.Account != export.Manifest.Source.Account {
					t.Fatalf("source account changed: got %q want %q", plan.Source.Account, export.Manifest.Source.Account)
				}
				literal := "quotes ' \"; newline\nbackticks `$(not-executed)` " + strings.Repeat("x", size)
				plan.Prompt = []byte(literal)
				if err := stageCrossHarnessPayload(plan); err != nil {
					t.Fatal(err)
				}
				if got, err := os.ReadFile(plan.PayloadPath); err != nil || string(got) != literal {
					t.Fatalf("staged payload changed literal bytes: err=%v", err)
				}
				target := &Instance{ID: plan.Target.InstanceID, Tool: plan.Target.Tool, Account: plan.Target.Account, ProjectPath: plan.Target.ProjectPath, Title: plan.Target.Title, GroupPath: plan.Target.GroupPath}
				command, embedded, err := crossHarnessPlanCommand(target, plan, "ignored")
				if err != nil || !embedded {
					t.Fatalf("file-backed command error=%v embedded=%t", err, embedded)
				}
				if len(command) > 4096 || strings.Contains(command, literal) || strings.Contains(command, "not-executed") {
					t.Fatalf("tmux command was not bounded/redacted: length=%d", len(command))
				}
				if !strings.Contains(command, plan.PayloadPath) || !strings.Contains(command, `payload=$(cat "$1")`) {
					t.Fatalf("command does not read the staged payload literally: %s", command)
				}
			}
		})
	}
}

func TestCrossHarnessPayloadStage_RejectsTamperAndSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	plan, err := BuildFreshTargetLaunchPlan(syntheticTransferExport("claude"), FreshTargetLaunchOptions{TargetHarness: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if err := stageCrossHarnessPayload(plan); err != nil {
		t.Fatal(err)
	}
	target := &Instance{ID: plan.Target.InstanceID, Tool: plan.Target.Tool, Account: plan.Target.Account, ProjectPath: plan.Target.ProjectPath, Title: plan.Target.Title, GroupPath: plan.Target.GroupPath}
	if err := os.WriteFile(plan.PayloadPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := crossHarnessPlanCommand(target, plan, ""); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("tampered payload error = %v, want hash refusal", err)
	}
	if err := os.Remove(plan.PayloadPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "outside"), plan.PayloadPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := crossHarnessPlanCommand(target, plan, ""); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("symlink payload error = %v, want unsafe-path refusal", err)
	}
}
