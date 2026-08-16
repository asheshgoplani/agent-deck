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
	// A key that IS present but unusable. Captured by exporting a
	// DEEPSEEK_API_KEY containing a space and a newline.
	dshInvalidCredLine = `dsh: INVALID_CREDENTIAL: llm-deepseek: the API key resolved from DEEPSEEK_API_KEY contains characters no HTTP header can carry; set DEEPSEEK_API_KEY to the raw key alone (the web Models page writes it)`
	// Codes that are NOT credential failures. A restart or a re-run can fix
	// these, so they must stay outside the auth hold.
	dshQuotaLine   = `dsh: QUOTA: llm-deepseek: provider quota exhausted`
	dshContextLine = `dsh: CONTEXT_WINDOW_EXCEEDED: llm-deepseek: the request exceeds the model context window`
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

// TestDefaultRawPatterns_DeepSeek_HelpIsNotAPrompt pins the review finding that
// "Usage: dsh" described a state that never occurs: it is the HELP screen (dsh
// prints it on --help and exits 0), while a real usage failure prints
// `error: a task is required, …` or `error: unknown option '…'`.
func TestDefaultRawPatterns_DeepSeek_HelpIsNotAPrompt(t *testing.T) {
	raw := DefaultRawPatterns("deepseek")
	if raw == nil {
		t.Fatal("no deepseek preset")
	}
	for _, p := range raw.PromptPatterns {
		if containsFold(p, "usage") {
			t.Errorf("prompt pattern %q matches the help screen, not a waiting state", p)
		}
	}
	resolved, err := CompilePatterns(raw)
	if err != nil {
		t.Fatalf("CompilePatterns: %v", err)
	}
	for _, usage := range []string{
		"Usage: dsh --profile headless [options] [task...]",
		`error: a task is required, for example: dsh --profile headless "run the tests"`,
		"error: unknown option '--bogus'",
	} {
		for _, re := range resolved.PromptRegexps {
			if re.MatchString(usage) {
				t.Errorf("prompt regex %q matched a non-waiting line %q", re, usage)
			}
		}
		for _, s := range resolved.PromptStrings {
			if containsFold(usage, s) {
				t.Errorf("prompt string %q matched a non-waiting line %q", s, usage)
			}
		}
	}
}

// TestPromptDetector_DeepSeek pins the dedicated detector arm. Without it
// deepseek fell through to the generic shell detector, which looks for "$ ",
// "# " and "% " — glyphs a dsh pane never prints.
func TestPromptDetector_DeepSeek(t *testing.T) {
	d := NewPromptDetector("deepseek")

	if !d.HasPrompt(dshWebReadyBanner) {
		t.Error("a served-and-idle dsh web pane is not detected as waiting")
	}
	// Busy wins over the banner: the server can still be mid-turn.
	if d.HasPrompt(dshWebReadyBanner + "\nworking... (esc to interrupt)") {
		t.Error("a busy pane was reported as waiting")
	}
	// A headless run's answer line is not a waiting state.
	if d.HasPrompt("answered: run the tests") {
		t.Error("a completed one-shot was reported as waiting")
	}
	// The arm ADDS to the previous behaviour rather than replacing it: an
	// installed interactive profile has a prompt glyph agent-deck cannot know,
	// so anything the banner does not explain still falls through to the generic
	// heuristic. Answering a hard "not ready" there would deny the
	// startup-window fast path forever — strictly worse than having no arm.
	if !d.HasPrompt("user@host:~/proj$ ") {
		t.Error("the generic fallback was lost; an installed profile would never be seen as ready")
	}
	// ...but busy still wins over that fallback.
	if d.HasPrompt("user@host:~/proj$ working (esc to interrupt)") {
		t.Error("busy did not win over the generic fallback")
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
	// Both CREDENTIAL codes hold the session. The adversarial review on #1942
	// caught that only one of them was covered: an earlier version required a
	// prose fragment AND the code on one line, which meant a reworded message
	// would have silently disabled the hold, and INVALID_CREDENTIAL — a real,
	// reachable failure for a key that is present but malformed — was missed
	// entirely.
	for _, line := range []string{dshMissingCredLine, dshInvalidCredLine} {
		if !IsAuthFailureContent("deepseek", line) {
			t.Errorf("credential failure not detected:\n%s", line)
		}
	}

	// A restart cannot fix a missing key, but it CAN fix these — they must stay
	// outside the auth hold.
	nonCredential := []string{
		"dsh web: http://127.0.0.1:3080",
		"dsh: boot failure: ECONNRESET",
		"Error: socket connection closed",
		// Real dsh error codes that are NOT credential problems: a quota trip
		// or a context overflow is worth re-running, not parking.
		dshQuotaLine,
		dshContextLine,
		// The code alone, outside dsh's own `dsh: CODE: ` rendering: an agent
		// discussing this failure must not put its own session on hold.
		"the run failed with MISSING_CREDENTIAL, let me check the key",
		"see INVALID_CREDENTIAL in the docs",
		// Indented or quoted by a conductor relaying a child's pane — the
		// anchor requires the line to START with dsh's own prefix.
		"  > dsh: MISSING_CREDENTIAL: llm-deepseek: no API key for provider route",
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

	// The rendering is structural — `dsh: <CODE>: <message>` — so a message
	// reworded upstream still holds the session. This is the property the
	// prose-fragment version did not have.
	reworded := "dsh: MISSING_CREDENTIAL: llm-deepseek: completely different wording here"
	if !IsAuthFailureContent("deepseek", reworded) {
		t.Error("a reworded credential message stopped matching; detection must key on the code, not the prose")
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
