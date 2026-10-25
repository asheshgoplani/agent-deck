package watcher

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The public ntfy.sh server lets anyone who knows a topic read and publish to
// it. These tests pin the access token support (sent as a Bearer header on the
// subscribe request) and the warning for a guessable topic on the public
// server with no token.

func TestGuessableNtfyTopic(t *testing.T) {
	tests := []struct {
		topic string
		want  bool
	}{
		{"alerts", true},                      // short
		{"my-private-topic", true},            // 16 chars but words only
		{"abcdefghijklmnopqrstuvwxyz", true},  // long, letters only
		{"alerts-2026", true},                 // has a digit but short
		{"phone-alerts-7f3k9q2x8m", false},    // long and has digits
		{"agentdeck-4b1f9c0e2d7a8b3c", false}, // generated shape
		{"0123456789abcdef", false},           // exactly 16 with digits
		{"0123456789abcde", true},             // 15 chars
	}
	for _, tt := range tests {
		if got := GuessableNtfyTopic(tt.topic); got != tt.want {
			t.Errorf("GuessableNtfyTopic(%q) = %v, want %v", tt.topic, got, tt.want)
		}
	}
}

func TestPublicNtfyTopicWarning(t *testing.T) {
	want := "ntfy watcher: this topic is easy to guess and the public server https://ntfy.sh has no access token set, " +
		"so anyone who guesses the topic can read its events and inject fake ones. " +
		"Fix one of: use a random topic (agent-deck watcher create ntfy --name <name> --random-topic); " +
		"set an access token ($NTFY_TOKEN or --token-file <path> with chmod 600, or token = \"...\" under [source] in watcher.toml); " +
		"or use your own ntfy server (--server https://ntfy.example.com, or server = \"...\" under [source])."

	if got := PublicNtfyTopicWarning("ntfy", "", "alerts", ""); got != want {
		t.Errorf("warning for a guessable topic on the default server:\n got: %s\nwant: %s", got, want)
	}
	if got := PublicNtfyTopicWarning("slack", "https://ntfy.sh/", "my-slack-topic", ""); !strings.HasPrefix(got, "slack watcher: ") ||
		!strings.Contains(got, "agent-deck watcher create slack --name <name> --random-topic") {
		t.Errorf("slack warning should name the slack type, got: %q", got)
	}

	quiet := []struct {
		name, server, topic, token string
	}{
		{"token set", "", "alerts", "tk_x"},
		{"self-hosted server", "https://ntfy.example.com", "alerts", ""},
		{"random topic", "https://ntfy.sh", "agentdeck-4b1f9c0e2d7a8b3c", ""},
	}
	for _, q := range quiet {
		if got := PublicNtfyTopicWarning("ntfy", q.server, q.topic, q.token); got != "" {
			t.Errorf("%s: expected no warning, got %q", q.name, got)
		}
	}
}

// The warning must never repeat the topic: it is a bearer capability and logs
// outlive the process.
func TestPublicNtfyTopicWarning_DoesNotEchoTopic(t *testing.T) {
	const topic = "secretish"
	if got := PublicNtfyTopicWarning("ntfy", "", topic, ""); strings.Contains(got, topic) {
		t.Errorf("warning echoes the topic: %q", got)
	}
}

func TestRandomNtfyTopic_IsNotGuessable(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		topic, err := RandomNtfyTopic()
		if err != nil {
			t.Fatalf("RandomNtfyTopic: %v", err)
		}
		if GuessableNtfyTopic(topic) {
			t.Fatalf("generated topic %q counts as guessable", topic)
		}
		if seen[topic] {
			t.Fatalf("generated the same topic twice: %q", topic)
		}
		seen[topic] = true
	}
}

// captureWatcherLog swaps the package logger for one writing into a buffer.
func captureWatcherLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := ntfyLog
	ntfyLog = slog.New(slog.NewTextHandler(&buf, nil))
	t.Cleanup(func() { ntfyLog = prev })
	return &buf
}

func TestNtfyAndSlackSetup_WarnsForGuessablePublicTopic(t *testing.T) {
	for _, tc := range []struct {
		typ   string
		setup func(map[string]string) error
	}{
		{"ntfy", func(s map[string]string) error {
			return (&NtfyAdapter{}).Setup(context.Background(), AdapterConfig{Type: "ntfy", Name: "w", Settings: s})
		}},
		{"slack", func(s map[string]string) error {
			return (&SlackAdapter{}).Setup(context.Background(), AdapterConfig{Type: "slack", Name: "w", Settings: s})
		}},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			buf := captureWatcherLog(t)
			if err := tc.setup(map[string]string{"topic": "alerts"}); err != nil {
				t.Fatalf("Setup: %v", err)
			}
			if !strings.Contains(buf.String(), "level=WARN") ||
				!strings.Contains(buf.String(), tc.typ+" watcher: this topic is easy to guess") {
				t.Errorf("expected a startup warning, log was: %s", buf.String())
			}

			buf.Reset()
			if err := tc.setup(map[string]string{"topic": "alerts", "token": "tk_x"}); err != nil {
				t.Fatalf("Setup with token: %v", err)
			}
			if buf.Len() != 0 {
				t.Errorf("no warning expected with a token, log was: %s", buf.String())
			}
		})
	}
}

// subscribeAuthHeader runs one subscription against a test server and returns
// the Authorization header it received.
func subscribeAuthHeader(t *testing.T, stream func(ctx context.Context, server string) error) string {
	t.Helper()
	got := make(chan string, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"id":"o1","event":"open"}`)
	}))
	defer ts.Close()
	if err := stream(context.Background(), ts.URL); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	return <-got
}

func TestNtfy_Subscribe_SendsBearerToken(t *testing.T) {
	for _, tc := range []struct{ token, want string }{
		{"tk_abc123", "Bearer tk_abc123"},
		{"", ""},
	} {
		got := subscribeAuthHeader(t, func(ctx context.Context, server string) error {
			a := &NtfyAdapter{}
			settings := map[string]string{"topic": "t", "server": server}
			if tc.token != "" {
				settings["token"] = tc.token
			}
			if err := a.Setup(ctx, AdapterConfig{Type: "ntfy", Name: "w", Settings: settings}); err != nil {
				return err
			}
			_, err := a.streamOnce(ctx, make(chan Event, 1))
			return err
		})
		if got != tc.want {
			t.Errorf("token %q: Authorization = %q, want %q", tc.token, got, tc.want)
		}
	}
}

func TestSlack_Subscribe_SendsBearerToken(t *testing.T) {
	got := subscribeAuthHeader(t, func(ctx context.Context, server string) error {
		a := &SlackAdapter{}
		if err := a.Setup(ctx, AdapterConfig{Type: "slack", Name: "w",
			Settings: map[string]string{"topic": "t", "server": server, "token": "tk_slack"}}); err != nil {
			return err
		}
		_, err := a.streamOnce(ctx, make(chan Event, 1))
		return err
	})
	if got != "Bearer tk_slack" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer tk_slack")
	}
}
