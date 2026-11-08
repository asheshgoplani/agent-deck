package session

import (
	"os/exec"
	"strings"
	"testing"
)

// Regression suite: a session with several channels must hand Claude one
// --channels value per channel.
//
// Claude Code declares the flag as `--channels <servers...>` (variadic,
// "space-separated server names") and splits nothing on commas. A single
// comma-joined value such as
//
//	--channels plugin:discord@claude-plugins-official,plugin:telegram@claude-plugins-official
//
// is therefore ONE channel entry that names no installed plugin, and the MCP
// log reports "Channel notifications skipped: you asked for
// plugin:discord@...,plugin:telegram@... but the installed plugin is
// discord@...". Inbound delivery dies for every channel of the session.
// Claude itself re-emits the flag as one `--channels <entry>` pair per
// channel when it relaunches, which is the form agent-deck now builds.

// claudeArgvFromFlags runs the built flag string through bash (the same
// `bash -c` wrapper the pane uses) and returns the argv words Claude would
// receive.
func claudeArgvFromFlags(t *testing.T, flags string) []string {
	t.Helper()
	out, err := exec.Command("bash", "-c", `printf '%s\0' `+flags).Output()
	if err != nil {
		t.Fatalf("bash could not word-split the flags %q: %v", flags, err)
	}
	words := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	return words
}

// channelValues collects every value Claude's parser would file under
// --channels: the words after each --channels up to the next flag.
func channelValues(argv []string) []string {
	var vals []string
	for k := 0; k < len(argv); k++ {
		if v, ok := strings.CutPrefix(argv[k], "--channels="); ok {
			vals = append(vals, v)
			continue
		}
		if argv[k] != "--channels" {
			continue
		}
		for k+1 < len(argv) && !strings.HasPrefix(argv[k+1], "-") {
			k++
			vals = append(vals, argv[k])
		}
	}
	return vals
}

func assertOneValuePerChannel(t *testing.T, path string, argv, want []string) {
	t.Helper()
	got := channelValues(argv)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s: Claude receives --channels values %q; want one value per channel %q\nargv: %q",
			path, got, want, argv)
	}
	for _, v := range got {
		if strings.Contains(v, ",") {
			t.Fatalf("%s: --channels value %q is comma-joined; Claude reads it as one unknown channel", path, v)
		}
	}
}

var twoChannels = []string{
	"plugin:telegram@claude-plugins-official",
	"plugin:discord@claude-plugins-official",
}

func TestChannelsFlags_TwoChannelsAreSeparateArgvValues(t *testing.T) {
	channelsTestEnv(t)
	inst := NewInstanceWithTool("ch-multi-argv", t.TempDir(), "claude")
	inst.Channels = append([]string(nil), twoChannels...)

	argv := claudeArgvFromFlags(t, inst.buildClaudeExtraFlags(&ClaudeOptions{}))
	assertOneValuePerChannel(t, "extra flags", argv, twoChannels)
}

// The start and the restart/resume builders must both carry the per-channel
// form (they share buildClaudeExtraFlags; this pins that they keep doing so).
func TestChannelsFlags_StartAndResumeEmitOneFlagPerChannel(t *testing.T) {
	channelsTestEnv(t)
	want := "--channels plugin:telegram@claude-plugins-official --channels plugin:discord@claude-plugins-official"

	start := NewInstanceWithTool("ch-multi-start", t.TempDir(), "claude")
	start.Channels = append([]string(nil), twoChannels...)
	if cmd := start.buildClaudeCommand("claude"); !strings.Contains(cmd, want) {
		t.Errorf("start command lacks %q, got:\n%s", want, cmd)
	}

	resume := NewInstanceWithTool("ch-multi-resume", t.TempDir(), "claude")
	resume.ClaudeSessionID = "00000000-0000-0000-0000-000000000000"
	resume.Channels = append([]string(nil), twoChannels...)
	if cmd := resume.buildClaudeResumeCommand(); !strings.Contains(cmd, want) {
		t.Errorf("restart/resume command lacks %q, got:\n%s", want, cmd)
	}
}

// Conductor launch path: a conductor that owns discord gets telegram added
// from its env_file on every spawn, which is exactly how most hosts end up
// with two channels. Both must arrive as their own values.
func TestChannelsFlags_ConductorHealedTelegramPlusDiscord(t *testing.T) {
	channelsTestEnv(t)
	withConductorEnvFile(t, "ops", "export TELEGRAM_STATE_DIR=/home/u/.claude/channels/telegram-ops\n")
	inst := lostChannelsConductor("ops")
	inst.Channels = []string{"plugin:discord@claude-plugins-official"}

	argv := claudeArgvFromFlags(t, inst.buildClaudeExtraFlags(nil))
	assertOneValuePerChannel(t, "conductor", argv, []string{
		"plugin:discord@claude-plugins-official",
		"plugin:telegram@claude-plugins-official",
	})
}

// A single channel keeps the exact form that already works today.
func TestChannelsFlags_SingleChannelUnchanged(t *testing.T) {
	channelsTestEnv(t)
	inst := NewInstanceWithTool("ch-single", t.TempDir(), "claude")
	inst.Channels = []string{"plugin:telegram@claude-plugins-official"}

	flags := inst.buildClaudeExtraFlags(&ClaudeOptions{})
	if strings.Count(flags, "--channels") != 1 ||
		!strings.Contains(flags, "--channels plugin:telegram@claude-plugins-official") {
		t.Fatalf("single channel flags = %q; want exactly one --channels plugin:telegram@claude-plugins-official", flags)
	}
}

// Blank entries (a stray comma in `session set <id> channels a,`) must not
// become an empty --channels value, which Claude rejects.
func TestChannelsFlags_BlankEntriesSkipped(t *testing.T) {
	channelsTestEnv(t)
	inst := NewInstanceWithTool("ch-blank", t.TempDir(), "claude")
	inst.Channels = []string{"plugin:telegram@claude-plugins-official", "  ", ""}

	argv := claudeArgvFromFlags(t, inst.buildClaudeExtraFlags(&ClaudeOptions{}))
	assertOneValuePerChannel(t, "blank entries", argv, []string{"plugin:telegram@claude-plugins-official"})
}
