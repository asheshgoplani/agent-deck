package tmux

import "testing"

// #2817: new-session -e (Session.LaunchEnv) exists from tmux 3.2 on. An
// older or unknown tmux must report no support so the session layer keeps
// its fallback instead of handing tmux a flag it rejects.
func TestTmuxSupportsLaunchEnv_Issue2817(t *testing.T) {
	for ver, want := range map[string]bool{
		"":       false,
		"2.9a":   false,
		"3.0a":   false,
		"3.1c":   false,
		"3.2":    true,
		"3.2a":   true,
		"3.4":    true,
		"4.0":    true,
		"master": true,
	} {
		if got := tmuxSupportsLaunchEnv(ver); got != want {
			t.Errorf("tmuxSupportsLaunchEnv(%q) = %v, want %v", ver, got, want)
		}
	}
}

// #2817 review: tmux reads an argv token that ends in ';' as a command
// separator, so a title like "build; deploy;" in `-e AGENTDECK_TITLE=...`
// broke new-session. A trailing ';' must be escaped so tmux gives back the
// exact value; nothing else is touched.
func TestLaunchEnvToken_TrailingSemicolon_Issue2817(t *testing.T) {
	for in, want := range map[string]string{
		"T=plain":          "T=plain",
		"T=a;b":            "T=a;b",
		"T=build; deploy;": `T=build; deploy\;`,
		`T=x\;`:            "T=x\\\\;",
		`T=a\b`:            `T=a\b`,
		"T=;":              `T=\;`,
	} {
		if got := launchEnvToken(in); got != want {
			t.Errorf("launchEnvToken(%q) = %q, want %q", in, got, want)
		}
	}
}
