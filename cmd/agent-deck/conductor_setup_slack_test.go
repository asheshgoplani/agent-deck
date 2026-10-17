package main

import (
	"reflect"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// #2548 review: adding Slack in `conductor setup` must not drop the
// [conductor.slack] keys the user already wrote (default_conductor from
// #2547, allowed_user_ids, listen_mode) when it saves the new tokens.
func TestWithSlackCredentials_KeepsExistingSlackKeys(t *testing.T) {
	def := "beta"
	existing := session.SlackSettings{
		ListenMode:       "all",
		AllowedUserIDs:   []string{"U123"},
		DefaultConductor: &def,
	}

	got := withSlackCredentials(existing, "xoxb-1", "xapp-1", "C01")

	want := existing
	want.BotToken, want.AppToken, want.ChannelID = "xoxb-1", "xapp-1", "C01"
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("withSlackCredentials() = %+v, want %+v", got, want)
	}
	if got.DefaultConductor == nil || *got.DefaultConductor != "beta" {
		t.Fatalf("default_conductor lost: %v", got.DefaultConductor)
	}
}
