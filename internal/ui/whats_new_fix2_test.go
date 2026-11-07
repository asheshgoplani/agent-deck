package ui

import (
	"errors"
	"strings"
	"testing"
)

// ASK-205 re-review: the subprocess runs as the person who pressed y (no
// agent session ids), and a dry run the CLI refuses disables posting.

func TestWhatsNewDialogCLIEnvDropsSessionIDs(t *testing.T) {
	t.Setenv("AGENTDECK_INSTANCE_ID", "sess-1")
	t.Setenv("AGENT_DECK_SESSION_ID", "sess-1")
	t.Setenv("TMUX", "/tmp/tmux-1000/default,123,0")
	t.Setenv("TMUX_PANE", "%3")
	t.Setenv("CLAUDE_CONFIG_DIR", "/work/claude-profile")
	env := "\n" + strings.Join(whatsNewCLIEnv(), "\n") + "\n"
	for _, k := range []string{"AGENTDECK_INSTANCE_ID", "AGENT_DECK_SESSION_ID", "TMUX", "TMUX_PANE"} {
		if strings.Contains(env, "\n"+k+"=") {
			t.Errorf("a person pressed y: the subprocess env must not carry %s", k)
		}
	}
	if !strings.Contains(env, "\nCLAUDE_CONFIG_DIR=/work/claude-profile\n") {
		t.Error("the subprocess env lost CLAUDE_CONFIG_DIR")
	}
}

func TestWhatsNewDialogDryRunRefusalDisablesPosting(t *testing.T) {
	for name, tc := range map[string]struct{ out, code, msg string }{
		"answered": {`{"ok":false,"error_code":"invalid_input","message":"you already answered #4242 (yes on v1.16.37); add --again to post another answer"}`,
			"invalid_input", "you already answered #4242"},
		"consent": {`{"ok":false,"error_code":"consent_required","message":"agent sessions may not post on GitHub from this machine"}`,
			"consent_required", "agent sessions may not post"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newWhatsNewFakes(t, "1.16.36", fixedReport())
			f.out = []string{tc.out}
			f.runErr = errors.New("exit status 1")
			h := newWhatsNewTestHome(t, f, "1.16.37")
			startUp(t, h, f)
			toPreview(t, h)
			v := flatView(h)
			for _, want := range []string{"cannot be posted", tc.code, tc.msg} {
				if !strings.Contains(v, want) {
					t.Errorf("refused dry run: view lacks %q:\n%s", want, v)
				}
			}
			if strings.Contains(v, "will be posted PUBLICLY") || strings.Contains(v, "y: post") {
				t.Fatalf("a refused answer must not be offered for posting:\n%s", v)
			}
			pressWN(h, reportTestKey("y"))
			if len(f.runs) != 1 {
				t.Fatalf("posted after a refused dry run: %q", f.runs)
			}
		})
	}
}
