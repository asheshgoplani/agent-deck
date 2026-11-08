package session

import (
	"os"
	"path/filepath"
	"testing"
)

// Regression: a conductor env_file that only mentions TELEGRAM_STATE_DIR in a
// comment (the operator turned telegram off by commenting the line out) must
// not count as a telegram declaration. A plain substring match re-added
// plugin:telegram@… to the conductor on every spawn and re-armed the
// telegram scratch gate on the host, so commenting the line out never
// disabled anything.

var telegramEnvFileCases = []struct {
	name    string
	content string
	want    bool
}{
	{"export assignment", "export TELEGRAM_STATE_DIR=/home/u/.claude/channels/telegram-ops\n", true},
	{"bare assignment", "TELEGRAM_STATE_DIR=/x\n", true},
	{"indented export with tab", "\texport\tTELEGRAM_STATE_DIR=\"/x\"\n", true},
	{"assignment after other lines", "export A=1\n# note\nexport TELEGRAM_STATE_DIR=/x\n", true},
	{"commented export", "# export TELEGRAM_STATE_DIR=/home/u/.claude/channels/telegram-ops\n", false},
	{"commented without space", "#export TELEGRAM_STATE_DIR=/x\n", false},
	{"indented comment", "   # TELEGRAM_STATE_DIR=/x\n", false},
	{"prose comment", "# telegram disabled: TELEGRAM_STATE_DIR moved\nexport A=1\n", false},
	{"trailing comment on other var", "export A=1 # TELEGRAM_STATE_DIR=/x\n", false},
	{"different variable with same prefix", "export TELEGRAM_STATE_DIR_OLD=/x\n", false},
	{"no mention", "export RAILWAY_API_TOKEN=abc\n", false},
}

func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".envrc")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	return path
}

func TestEnvFileDeclaresTelegram_OnlyActiveAssignments(t *testing.T) {
	for _, tc := range telegramEnvFileCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := envFileDeclaresTelegram(writeEnvFile(t, tc.content)); got != tc.want {
				t.Fatalf("envFileDeclaresTelegram(%q) = %v; want %v", tc.content, got, tc.want)
			}
		})
	}
}

// Sibling copy of the same check: the host-level telegram detection used by
// the worker scratch gate.
func TestConfigDeclaresTelegram_OnlyActiveAssignments(t *testing.T) {
	for _, tc := range telegramEnvFileCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &UserConfig{Conductors: map[string]ConductorOverrides{
				"ops": {Claude: ConductorClaudeSettings{EnvFile: writeEnvFile(t, tc.content)}},
			}}
			if got := configDeclaresTelegram(cfg); got != tc.want {
				t.Fatalf("configDeclaresTelegram(env_file %q) = %v; want %v", tc.content, got, tc.want)
			}
		})
	}
}

// End to end on the spawn path: a conductor whose env_file has the telegram
// line commented out must not get telegram back on its command line.
func TestReconcileIgnoresCommentedTelegramLine(t *testing.T) {
	channelsTestEnv(t)
	withConductorEnvFile(t, "ops", "# export TELEGRAM_STATE_DIR=/home/u/.claude/channels/telegram-ops\n")
	inst := lostChannelsConductor("ops")
	inst.Channels = []string{"plugin:discord@claude-plugins-official"}

	if reconcileConductorTelegramChannel(inst) {
		t.Fatalf("reconcile re-added telegram from a commented line; Channels = %v", inst.Channels)
	}
	if got := channelValues(claudeArgvFromFlags(t, inst.buildClaudeExtraFlags(nil))); len(got) != 1 ||
		got[0] != "plugin:discord@claude-plugins-official" {
		t.Fatalf("--channels values = %q; want only discord", got)
	}
}
