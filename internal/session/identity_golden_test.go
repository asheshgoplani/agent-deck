package session

import (
	"encoding/base64"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Golden regression tests for harness identity injection. They pin, byte for
// byte, what a session's identity block and the launch fragments that carry
// it look like with no config.toml, so later features can prove that, left
// switched off, they change nothing here.
//
// Regenerate (only for an intended, reviewed change) with:
//
//	go test ./internal/session/ -run TestIdentityGolden -update-identity-golden
//
// or AGENTDECK_UPDATE_IDENTITY_GOLDEN=1. In update mode each golden is also
// logged as one "IDENTITY_GOLDEN <name> <base64>" line, so the files can be
// recovered from the output when the test ran in a throwaway container
// (deck-test streams the source in and copies nothing back):
//
//	... -v | awk '$1=="IDENTITY_GOLDEN"{print $2, $3}' |
//	  while read n b; do printf '%s' "$b" | base64 -d > internal/session/testdata/identity_golden/"$n"; done
//
// Volatile parts are normalized before comparing: the local hostname becomes
// <HOST> and the temp HOME becomes <HOME>.

const identityGoldenUpdateEnv = "AGENTDECK_UPDATE_IDENTITY_GOLDEN"

var updateIdentityGolden = flag.Bool("update-identity-golden", false, "rewrite testdata/identity_golden from the current output")

func identityGoldenNormalize(t *testing.T, home, s string) string {
	t.Helper()
	if home != "" {
		s = strings.ReplaceAll(s, home, "<HOME>")
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		// Only the host line: a short hostname must not rewrite other text.
		// The printf %b payloads of SSH/sandbox launches encode the newline.
		s = strings.ReplaceAll(s, "- host: "+host+"\n", "- host: <HOST>\n")
		s = strings.ReplaceAll(s, "- host: "+host+`\n`, `- host: <HOST>\n`)
	}
	return s
}

func assertIdentityGolden(t *testing.T, home, name, got string) {
	t.Helper()
	got = identityGoldenNormalize(t, home, got)
	path := filepath.Join("testdata", "identity_golden", name)
	if *updateIdentityGolden || os.Getenv(identityGoldenUpdateEnv) == "1" {
		t.Logf("\nIDENTITY_GOLDEN %s %s", name, base64.StdEncoding.EncodeToString([]byte(got)))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (regenerate with -update-identity-golden)", path, err)
	}
	if want := string(data); got != want {
		t.Errorf("%s differs from golden %s (regenerate with -update-identity-golden only if the change is intended):\n%s",
			name, path, identityGoldenDiff(want, got))
	}
}

// identityGoldenDiff is a minimal line diff: common prefix and suffix are
// skipped, the differing middle is shown as -want / +got.
func identityGoldenDiff(want, got string) string {
	w := strings.Split(want, "\n")
	g := strings.Split(got, "\n")
	p := 0
	for p < len(w) && p < len(g) && w[p] == g[p] {
		p++
	}
	sw, sg := len(w), len(g)
	for sw > p && sg > p && w[sw-1] == g[sg-1] {
		sw--
		sg--
	}
	var b strings.Builder
	b.WriteString("--- golden\n+++ got\n")
	for _, l := range w[p:sw] {
		b.WriteString("-" + l + "\n")
	}
	for _, l := range g[p:sg] {
		b.WriteString("+" + l + "\n")
	}
	return b.String()
}

func TestIdentityGolden_Prompts(t *testing.T) {
	home := identityTestEnv(t)
	assertIdentityGolden(t, home, "prompt_full_claude.txt", identityTestInstance("claude").BuildIdentityPrompt())
	assertIdentityGolden(t, home, "prompt_full_codex.txt", identityTestInstance("codex").BuildIdentityPrompt())
	assertIdentityGolden(t, home, "prompt_primer_claude.txt", identityTestInstance("claude").BuildPrimerPrompt())
}

func TestIdentityGolden_LocalClaude(t *testing.T) {
	home := identityTestEnv(t)
	inst := identityTestInstance("claude")
	assertIdentityGolden(t, home, "local_claude_flag.txt", inst.claudeIdentityFlag())
	assertIdentityGolden(t, home, "local_claude_env_export.txt", inst.identityEnvExport())
	_, file, ok := inst.ensureIdentityFile()
	if !ok {
		t.Fatal("ensureIdentityFile returned !ok with default config")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	assertIdentityGolden(t, home, "local_claude_identity_file.md", string(data))
}

func TestIdentityGolden_LocalCodex(t *testing.T) {
	home := identityTestEnv(t)
	inst := identityTestInstance("codex")
	value, ok := inst.codexIdentityOverride(t.TempDir(), inst.ProjectPath)
	if !ok {
		t.Fatal("codexIdentityOverride returned !ok with default config")
	}
	assertIdentityGolden(t, home, "local_codex_override.txt", value)
}

func TestIdentityGolden_SSHClaude(t *testing.T) {
	home := identityTestEnv(t)
	inst := sshIdentityInstance("claude", "worker one")
	assertIdentityGolden(t, home, "ssh_claude_env_export.txt", inst.identityEnvExport())
	assertIdentityGolden(t, home, "ssh_claude_flag.txt", inst.claudeIdentityFlag())
}

func TestIdentityGolden_SSHCodex(t *testing.T) {
	home := identityTestEnv(t)
	inst := sshIdentityInstance("codex", "worker one")
	assertIdentityGolden(t, home, "ssh_codex_env_export.txt", inst.identityEnvExport())
	value, ok := inst.codexIdentityOverride(t.TempDir(), inst.ProjectPath)
	if !ok {
		t.Fatal("codexIdentityOverride returned !ok for ssh codex")
	}
	assertIdentityGolden(t, home, "ssh_codex_override.txt", value)
}

func TestIdentityGolden_SandboxClaude(t *testing.T) {
	home := identityTestEnv(t)
	inst := identityTestInstance("claude")
	inst.Command = "claude"
	inst.Sandbox = &SandboxConfig{Enabled: true, Image: "img"}
	assertIdentityGolden(t, home, "sandbox_claude_env_export.txt", inst.identityEnvExport())
	assertIdentityGolden(t, home, "sandbox_claude_flag.txt", inst.claudeIdentityFlag())
}

func TestIdentityGolden_SandboxCodex(t *testing.T) {
	home := identityTestEnv(t)
	inst := identityTestInstance("codex")
	inst.Command = "codex"
	inst.Sandbox = &SandboxConfig{Enabled: true, Image: "img"}
	assertIdentityGolden(t, home, "sandbox_codex_env_export.txt", inst.identityEnvExport())
	value, ok := inst.codexIdentityOverride(t.TempDir(), inst.ProjectPath)
	if !ok {
		t.Fatal("codexIdentityOverride returned !ok for sandboxed codex")
	}
	assertIdentityGolden(t, home, "sandbox_codex_override.txt", value)
}

// identityGoldenConfiguredDeveloperInstructions is a ~9 KB operator
// developer_instructions: large enough that the codex override alone is
// over the 8 KB inline budget, as in the #3215 review (R-2).
func identityGoldenConfiguredDeveloperInstructions() string {
	var b strings.Builder
	for k := 0; b.Len() < 9000; k++ {
		b.WriteString("Operator rule ")
		b.WriteString(strings.Repeat("x", k%7))
		b.WriteString(": keep the build green and say what you changed.\n")
	}
	return b.String()
}

// writeCodexDeveloperInstructions writes developer_instructions into
// <codexHome>/config.toml the way an operator would.
func writeCodexDeveloperInstructions(t *testing.T, codexHome, text string) {
	t.Helper()
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("developer_instructions = "+tomlBasicString(text)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A local codex session whose codex config already carries a large
// developer_instructions: the override and the full launch command, without
// instruction files, are pinned from main (#3215 review R-2).
func TestIdentityGolden_LocalCodexConfiguredDeveloperInstructions(t *testing.T) {
	home := identityTestEnv(t)
	t.Setenv("CODEX_HOME", "")
	codexHome := filepath.Join(home, ".codex")
	writeCodexDeveloperInstructions(t, codexHome, identityGoldenConfiguredDeveloperInstructions())
	inst := identityTestInstance("codex")
	value, ok := inst.codexIdentityOverride(codexHome, inst.ProjectPath)
	if !ok {
		t.Fatal("codexIdentityOverride returned !ok with a configured developer_instructions")
	}
	assertIdentityGolden(t, home, "local_codex_configured_dev_override.txt", value)
	assertIdentityGolden(t, home, "local_codex_configured_dev_command.txt", inst.buildCodexCommand("codex"))
}
