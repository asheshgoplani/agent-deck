package tmux

import "testing"

func TestDetectToolFromCommand_Omp(t *testing.T) {
	cases := []string{
		"omp",
		"omp --profile wsl-pilot",
		`session_dir=${HOME}/.omp/agent-deck/abc; mkdir -p "$session_dir" && AGENTDECK_PROFILE=default omp --profile wsl-pilot --continue --session-dir "$session_dir"`,
	}
	for _, cmd := range cases {
		if got := detectToolFromCommand(cmd); got != "omp" {
			t.Errorf("detectToolFromCommand(%q) = %q, want omp", cmd, got)
		}
	}
}

func TestDetectToolFromCommand_Omp_Negative(t *testing.T) {
	// "wsl-pilot" must not trip the pi arm; unrelated commands must not become omp.
	cases := map[string]string{
		"stomp-server --port 8080": "",
		"docker compose up":        "",
	}
	for cmd, want := range cases {
		if got := detectToolFromCommand(cmd); got != want {
			t.Errorf("detectToolFromCommand(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestDetectToolFromContent_Omp(t *testing.T) {
	// omp welcome banner must detect as omp, not codex, even when provider
	// text like "openai" or "codex" appears elsewhere on the screen.
	content := `╭─── omp v17.0.1 ─────────────────────────╮
│      Welcome back!    │ Tips            │
│     Claude Opus 4.8   │ # for prompts   │
│        anthropic      │                 │
Failed: codex_apps: HTTP 401 token_expired; openai provider ready`
	if got := detectToolFromContent(content); got != "omp" {
		t.Errorf("detectToolFromContent(omp banner) = %q, want omp", got)
	}
}
