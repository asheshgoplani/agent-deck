package main

import (
	"bufio"
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func promptSlackAccessFrom(input string) (session.SlackSettings, string, error) {
	var out bytes.Buffer
	got, err := promptSlackAccess(bufio.NewReader(strings.NewReader(input)), &out, session.SlackSettings{ChannelID: "C01"})
	return got, out.String(), err
}

func TestPromptSlackAccess_WritesAllowedUserIDs(t *testing.T) {
	got, out, err := promptSlackAccessFrom(" U01234567 , W0ABC\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got.AllowedUserIDs, []string{"U01234567", "W0ABC"}) || got.AllowAllUsers {
		t.Fatalf("got %+v", got)
	}
	if got.ChannelID != "C01" {
		t.Fatalf("other Slack keys lost: %+v", got)
	}
	if !strings.Contains(out, "Copy member ID") {
		t.Fatalf("prompt should explain how to find a member ID:\n%s", out)
	}
}

func TestPromptSlackAccess_ReasksOnInvalidID(t *testing.T) {
	got, out, err := promptSlackAccessFrom("u0123, bad-id\nU0123\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got.AllowedUserIDs, []string{"U0123"}) {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(out, `"u0123"`) {
		t.Fatalf("the invalid ID should be named:\n%s", out)
	}
}

func TestPromptSlackAccess_EmptyNeedsConfirmation(t *testing.T) {
	got, _, err := promptSlackAccessFrom("\ny\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.AllowAllUsers || len(got.AllowedUserIDs) != 0 {
		t.Fatalf("confirmed empty answer should allow all users: %+v", got)
	}

	got, _, err = promptSlackAccessFrom("\nn\nU0123\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.AllowAllUsers || !reflect.DeepEqual(got.AllowedUserIDs, []string{"U0123"}) {
		t.Fatalf("declined empty answer should re-ask: %+v", got)
	}
}

func TestPromptSlackAccess_GivesUpWithoutAnAnswer(t *testing.T) {
	for name, input := range map[string]string{
		"closed stdin":    "",
		"always declined": "\nn\n\nn\n\nn\n",
		"always invalid":  "x\nx\nx\n",
	} {
		if got, _, err := promptSlackAccessFrom(input); err == nil {
			t.Errorf("%s: expected an error, got %+v", name, got)
		}
	}
}

func TestSlackSettings_AllowAllUsersRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(session.SlackSettings{AllowAllUsers: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "allow_all_users = true") {
		t.Fatalf("allow_all_users not written:\n%s", buf.String())
	}
	var back session.SlackSettings
	if _, err := toml.Decode(buf.String(), &back); err != nil || !back.AllowAllUsers {
		t.Fatalf("allow_all_users not read back: %+v %v", back, err)
	}
}

// Setup warns an upgraded Slack install with no access rule, using the
// bridge's exact startup text, and stays quiet once a rule is set.
func TestSlackAccessWarning_UpgradedInstallWithoutAllowList(t *testing.T) {
	open := session.SlackSettings{BotToken: "xoxb", AppToken: "xapp", ChannelID: "C1"}
	got := slackAccessWarning(open)
	want := `WARNING: Slack bridge: no allowed users are set, so every Slack message and command is refused. ` +
		`Add your Slack member ID under [conductor.slack] in ~/.agent-deck/config.toml: ` +
		`allowed_user_ids = ["U01234567"] (or set allow_all_users = true to let every workspace member in), then restart the bridge.`
	if got != want {
		t.Fatalf("warning = %q\nwant      %q", got, want)
	}
	listed := open
	listed.AllowedUserIDs = []string{"U1"}
	all := open
	all.AllowAllUsers = true
	for name, s := range map[string]session.SlackSettings{"listed": listed, "allow_all": all, "no slack": {}} {
		if got := slackAccessWarning(s); got != "" {
			t.Errorf("%s: unexpected warning %q", name, got)
		}
	}
}
