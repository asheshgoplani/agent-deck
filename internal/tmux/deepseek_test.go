package tmux

import "testing"

// DeepSeek Harness (`dsh`): tmux-layer detection tests.
//
// The pane text asserted here is verbatim output from @deepseek-ai/dsh
// 0.1.0-rc.6, captured in a sandboxed HOME. It is not paraphrased: a detection
// pattern is only worth what the real binary prints.

const (
	dshWebReadyBanner  = "dsh web: http://127.0.0.1:39949"
	dshLauncherHelp    = "dsh: boot a DeepSeek Harness profile — an ordered stack of plugin-bundle patch\nlayers under your own overrides."
	dshHeadlessUsage   = "Usage: dsh --profile headless [options] [task...]"
	dshMissingCredLine = `dsh: MISSING_CREDENTIAL: llm-deepseek: no API key for provider route "deepseek-official"; store DEEPSEEK_API_KEY through the credentials service (the web Models page writes it), or export DEEPSEEK_API_KEY in the launching environment`
)

func TestDetectToolFromCommand_DeepSeek(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{"bare dsh", "dsh", "deepseek"},
		{"dsh with launcher flags", "dsh --profile web --port 8080", "deepseek"},
		{"absolute path", "/usr/local/bin/dsh", "deepseek"},
		{"npm bin path", "/home/u/.npm-global/bin/dsh --profile headless", "deepseek"},
		{"npx one-liner", "npx @deepseek-ai/dsh web", "deepseek"},
		// agent-deck's own launch string puts env assignments first, so the
		// program is not fields[0].
		{"env-prefixed launch", "DSH_HOME=/home/u/.dsh AGENTDECK_TOOL=deepseek dsh --profile web", "deepseek"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectToolFromCommand(tt.command); got != tt.want {
				t.Fatalf("detectToolFromCommand(%q) = %q, want %q", tt.command, got, tt.want)
			}
		})
	}
}

func TestDetectToolFromCommand_DeepSeek_Negative(t *testing.T) {
	// "dsh" is three very common letters. Substring matching would claim every
	// one of these; the basename/token arms must not.
	tests := []string{
		"dshell",
		"/usr/bin/fdsh-tool",
		"npm run build:dshboard",
		"grep dsh README.md",
		"cat dsh.log",
		"echo dsh",
	}
	for _, cmd := range tests {
		t.Run(cmd, func(t *testing.T) {
			if got := detectToolFromCommand(cmd); got == "deepseek" {
				t.Fatalf("detectToolFromCommand(%q) = %q, should NOT match deepseek", cmd, got)
			}
		})
	}
}

func TestDetectToolFromContent_DeepSeek(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"web ready banner", dshWebReadyBanner},
		{"launcher help", dshLauncherHelp},
		{"headless usage", dshHeadlessUsage},
		{"credential error", dshMissingCredLine},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectToolFromContent(tt.content); got != "deepseek" {
				t.Fatalf("detectToolFromContent(%q) = %q, want deepseek", tt.content, got)
			}
		})
	}
}

func TestDetectToolFromContent_DeepSeek_DoesNotStealModelMentions(t *testing.T) {
	// A codex pane that happens to be running a DeepSeek model must stay codex.
	// The tool-detection order puts codex first, but the deepseek patterns must
	// also not match a bare model name on their own.
	content := "codex\nmodel: deepseek-v4-pro\n"
	if got := detectToolFromContent(content); got == "deepseek" {
		t.Fatalf("detectToolFromContent claimed a codex pane mentioning a DeepSeek model")
	}

	// And a pane that only names the model, with no dsh output at all, must not
	// be claimed either.
	if got := detectToolFromContent("using deepseek-v4-pro for this task"); got == "deepseek" {
		t.Fatalf("detectToolFromContent claimed a pane that only names a DeepSeek model")
	}
}

func TestDefaultRawPatterns_DeepSeek(t *testing.T) {
	raw := DefaultRawPatterns("deepseek")
	if raw == nil {
		t.Fatal("DefaultRawPatterns(\"deepseek\") = nil, want a preset")
	}

	resolved, err := CompilePatterns(raw)
	if err != nil {
		t.Fatalf("CompilePatterns: %v", err)
	}

	// The web ready banner is the idle/waiting signal: the server is up and
	// doing nothing until a browser talks to it.
	matched := false
	for _, re := range resolved.PromptRegexps {
		if re.MatchString(dshWebReadyBanner) {
			matched = true
		}
	}
	if !matched {
		t.Errorf("no prompt pattern matches the web ready banner %q", dshWebReadyBanner)
	}

	// The banner must NOT read as busy, or a served-and-idle pane would show a
	// spinner forever (busy is checked before prompt in the detector).
	for _, s := range resolved.BusyStrings {
		if containsFold(dshWebReadyBanner, s) {
			t.Errorf("busy pattern %q matches the idle web ready banner", s)
		}
	}
	for _, re := range resolved.BusyRegexps {
		if re.MatchString(dshWebReadyBanner) {
			t.Errorf("busy regex %q matches the idle web ready banner", re)
		}
	}
}

func TestIsAuthFailureContent_DeepSeek(t *testing.T) {
	if !IsAuthFailureContent("deepseek", dshMissingCredLine) {
		t.Error("dsh's MISSING_CREDENTIAL line is not detected as a credential failure")
	}

	// A restart cannot fix a missing key, but it CAN fix these — they must stay
	// outside the auth hold.
	nonCredential := []string{
		"dsh web: http://127.0.0.1:3080",
		"dsh: boot failure: ECONNRESET",
		"Error: socket connection closed",
		// The code alone, without dsh's own provider-route phrase: an agent
		// discussing this failure must not put its own session on hold.
		"the run failed with MISSING_CREDENTIAL, let me check the key",
	}
	for _, content := range nonCredential {
		if IsAuthFailureContent("deepseek", content) {
			t.Errorf("IsAuthFailureContent(deepseek, %q) = true, want false", content)
		}
	}

	// Tool-scoped: the same line under another tool is not this tool's verdict.
	if IsAuthFailureContent("codex", dshMissingCredLine) {
		t.Error("dsh's credential line was attributed to codex")
	}

	// Found in a realistic pane tail, not just as the whole content.
	pane := "$ dsh --profile headless \"say hi\"\n" + dshMissingCredLine + "\n$ "
	if !IsAuthFailureContent("deepseek", pane) {
		t.Error("credential failure not detected in a pane tail")
	}

	// Scrolled far out of the tail window, it must no longer count as live.
	scrolled := dshMissingCredLine + "\n"
	for i := 0; i < 40; i++ {
		scrolled += "ordinary output line\n"
	}
	if IsAuthFailureContent("deepseek", scrolled) {
		t.Error("a credential failure scrolled out of the tail window is still reported as live")
	}
}

// containsFold is a local case-insensitive Contains for the busy-pattern check
// (busy strings are matched case-insensitively by the detector).
func containsFold(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		indexFold(haystack, needle) >= 0
}

func indexFold(haystack, needle string) int {
	h, n := []rune(lowerASCII(haystack)), []rune(lowerASCII(needle))
	for i := 0; i+len(n) <= len(h); i++ {
		match := true
		for j := range n {
			if h[i+j] != n[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
