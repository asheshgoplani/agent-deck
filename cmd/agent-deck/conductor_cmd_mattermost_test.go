package main

import (
	"bufio"
	"io"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func promptMattermostFrom(input string) (session.MattermostSettings, error) {
	return promptMattermostSettings(bufio.NewReader(strings.NewReader(input)), io.Discard)
}

func TestPromptMattermostSettings_DMByDefault(t *testing.T) {
	got, err := promptMattermostFrom("https://mattermost.example.com/\ntoken123\n@mwallace\n\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := session.MattermostSettings{ServerURL: "https://mattermost.example.com", BotToken: "token123", User: "mwallace"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestPromptMattermostSettings_Channel(t *testing.T) {
	got, err := promptMattermostFrom("https://mm.example.com\n$MM_TOKEN\nmwallace\nabcdefghijklmnopqrstuvwxyz\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ChannelID != "abcdefghijklmnopqrstuvwxyz" || got.BotToken != "$MM_TOKEN" {
		t.Fatalf("got %+v", got)
	}
}

func TestPromptMattermostSettings_RejectsIncompleteInput(t *testing.T) {
	for name, input := range map[string]string{
		"not a URL":    "mattermost.example.com\ntoken\nme\n\n",
		"wrong scheme": "ftp://mattermost.example.com\ntoken\nme\n\n",
		"no token":     "https://mm.example.com\n\nme\n\n",
		"no username":  "https://mm.example.com\ntoken\n@\n\n",
		"closed stdin": "",
	} {
		if _, err := promptMattermostFrom(input); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestPromptMattermostSettings_PlainHTTP(t *testing.T) {
	for name, tc := range map[string]struct {
		input        string
		wantErr      bool
		wantInsecure bool
	}{
		"remote host, declined":  {"http://mm.example.com\nn\ntoken\nme\n\n", true, false},
		"remote host, confirmed": {"http://mm.example.com\ny\ntoken\nme\n\n", false, true},
		"localhost":              {"http://localhost:8065\ntoken\nme\n\n", false, false},
		"loopback IP":            {"http://127.0.0.1:8065\ntoken\nme\n\n", false, false},
		"https needs no consent": {"https://mm.example.com\ntoken\nme\n\n", false, false},
	} {
		got, err := promptMattermostFrom(tc.input)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", name, err, tc.wantErr)
			continue
		}
		if err == nil && got.AllowInsecureHTTP != tc.wantInsecure {
			t.Errorf("%s: AllowInsecureHTTP = %v, want %v", name, got.AllowInsecureHTTP, tc.wantInsecure)
		}
		if err == nil && got.BotToken != "token" {
			t.Errorf("%s: prompts out of step, got %+v", name, got)
		}
	}
}
