package watcher

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go.uber.org/goleak"
)

func TestFirstLine_UTF8Boundary(t *testing.T) {
	// Cases where the byte cap falls inside a multi-byte UTF-8 sequence.
	// Each input is longer than maxLen bytes and the byte-cap position
	// lands inside a Cyrillic (2-byte), em-dash (3-byte), or emoji (4-byte)
	// codepoint. firstLine must return valid UTF-8 by trimming back to a
	// codepoint boundary.
	tests := []struct {
		name   string
		input  string
		maxLen int
	}{
		{
			name:   "cyrillic mid-codepoint at cap",
			input:  strings.Repeat("сессии. ", 30), // ~480 bytes of 2-byte runes
			maxLen: 199,                            // odd byte cap -> guaranteed mid-rune cut
		},
		{
			name:   "em-dash mid-codepoint",
			input:  strings.Repeat("a—", 80), // — is 3 bytes
			maxLen: 100,
		},
		{
			name:   "emoji mid-codepoint",
			input:  strings.Repeat("ab😀", 30), // 😀 is 4 bytes
			maxLen: 50,
		},
		{
			name:   "ascii baseline",
			input:  strings.Repeat("a", 500),
			maxLen: 200,
		},
		{
			name:   "exact boundary fit",
			input:  strings.Repeat("ы", 100), // each ы is 2 bytes -> exactly 200 bytes
			maxLen: 200,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := firstLine(tc.input, tc.maxLen)
			if !utf8.ValidString(got) {
				t.Errorf("firstLine produced invalid UTF-8 (len=%d, bytes=% x)", len(got), []byte(got))
			}
			if len(got) > tc.maxLen {
				t.Errorf("firstLine exceeded maxLen: got %d bytes, max %d", len(got), tc.maxLen)
			}
			// Trim should remove at most 3 bytes (UTF-8 max sequence is 4 bytes).
			if untrimmedLen := tc.maxLen; len(got) > 0 && untrimmedLen-len(got) > 3 {
				t.Errorf("trim removed more than 3 bytes: cap=%d, got=%d", untrimmedLen, len(got))
			}
		})
	}
}

func TestFirstLine_NewlineBeforeCap(t *testing.T) {
	// When a newline exists before maxLen, firstLine truncates at the newline
	// regardless of UTF-8 boundaries (newline is ASCII so always safe).
	in := "сессии\nrest of message"
	got := firstLine(in, 200)
	want := "сессии"
	if got != want {
		t.Errorf("firstLine(%q, 200) = %q, want %q", in, got, want)
	}
}

func TestWebhook_Setup_DefaultPort(t *testing.T) {
	a := &WebhookAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "webhook",
		Name:     "test",
		Settings: map[string]string{},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if a.addr != "127.0.0.1:18460" {
		t.Errorf("expected default addr 127.0.0.1:18460, got %q", a.addr)
	}
}

func TestWebhook_Setup_CustomPort(t *testing.T) {
	a := &WebhookAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "webhook",
		Name:     "test",
		Settings: map[string]string{"port": "19999"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if a.addr != "127.0.0.1:19999" {
		t.Errorf("expected addr 127.0.0.1:19999, got %q", a.addr)
	}
}

func TestWebhook_Listen_Accepts_POST(t *testing.T) {
	a := &WebhookAdapter{}
	// Use port 0 to get a random available port
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "webhook",
		Name:     "test",
		Settings: map[string]string{"port": "0"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- a.Listen(ctx, events)
	}()

	// Wait for server to start
	if !waitForServer(t, a, 2*time.Second) {
		t.Fatal("server did not start in time")
	}

	// POST a JSON body
	body := `{"action": "test", "message": "hello"}`
	resp, err := http.Post("http://"+a.addr+"/webhook", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("expected 202, got %d", resp.StatusCode)
	}

	// Read event from channel
	select {
	case evt := <-events:
		if evt.Source != "webhook" {
			t.Errorf("expected Source=webhook, got %q", evt.Source)
		}
		if evt.Body != body {
			t.Errorf("expected Body=%q, got %q", body, evt.Body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event received within 2s")
	}

	cancel()
	<-listenErr
}

func TestWebhook_Listen_Rejects_GET(t *testing.T) {
	a := &WebhookAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "webhook",
		Name:     "test",
		Settings: map[string]string{"port": "0"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- a.Listen(ctx, events)
	}()

	if !waitForServer(t, a, 2*time.Second) {
		t.Fatal("server did not start in time")
	}

	resp, err := http.Get("http://" + a.addr + "/webhook")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}

	cancel()
	<-listenErr
}

func TestWebhook_Listen_BodySizeLimit(t *testing.T) {
	a := &WebhookAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "webhook",
		Name:     "test",
		Settings: map[string]string{"port": "0"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- a.Listen(ctx, events)
	}()

	if !waitForServer(t, a, 2*time.Second) {
		t.Fatal("server did not start in time")
	}

	// Send body > 1MB
	bigBody := bytes.Repeat([]byte("x"), 1<<20+1)
	resp, err := http.Post("http://"+a.addr+"/webhook", "application/octet-stream", bytes.NewReader(bigBody))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d", resp.StatusCode)
	}

	cancel()
	<-listenErr
}

func TestWebhook_Normalization(t *testing.T) {
	a := &WebhookAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "webhook",
		Name:     "test",
		Settings: map[string]string{"port": "0"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- a.Listen(ctx, events)
	}()

	if !waitForServer(t, a, 2*time.Second) {
		t.Fatal("server did not start in time")
	}

	t.Run("with_headers", func(t *testing.T) {
		req, _ := http.NewRequest("POST", "http://"+a.addr+"/webhook", strings.NewReader("test body"))
		req.Header.Set("X-Webhook-Sender", "ci-bot")
		req.Header.Set("X-Webhook-Subject", "build passed")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()

		select {
		case evt := <-events:
			if evt.Sender != "ci-bot" {
				t.Errorf("expected Sender=ci-bot, got %q", evt.Sender)
			}
			if evt.Subject != "build passed" {
				t.Errorf("expected Subject='build passed', got %q", evt.Subject)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no event received")
		}
	})

	t.Run("without_headers", func(t *testing.T) {
		body := "first line of body\nsecond line"
		resp, err := http.Post("http://"+a.addr+"/webhook", "text/plain", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()

		select {
		case evt := <-events:
			if evt.Sender == "" {
				t.Error("expected non-empty Sender (remote addr)")
			}
			if evt.Subject != "first line of body" {
				t.Errorf("expected Subject='first line of body', got %q", evt.Subject)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no event received")
		}
	})

	cancel()
	<-listenErr
}

func TestWebhook_HealthCheck_Running(t *testing.T) {
	a := &WebhookAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "webhook",
		Name:     "test",
		Settings: map[string]string{"port": "0"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- a.Listen(ctx, events)
	}()

	if !waitForServer(t, a, 2*time.Second) {
		t.Fatal("server did not start in time")
	}

	if err := a.HealthCheck(); err != nil {
		t.Errorf("expected nil from HealthCheck when running, got %v", err)
	}

	cancel()
	<-listenErr
}

func TestWebhook_HealthCheck_NotRunning(t *testing.T) {
	a := &WebhookAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "webhook",
		Name:     "test",
		Settings: map[string]string{"port": "19998"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	// Without Listen, server is not running
	if err := a.HealthCheck(); err == nil {
		t.Error("expected error from HealthCheck when server not running")
	}
}

func TestWebhook_Listen_StopNoLeaks(t *testing.T) {
	defer goleak.VerifyNone(t,
		goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"),
		goleak.IgnoreTopFunction("database/sql.(*DB).connectionResetter"),
		goleak.IgnoreAnyFunction("modernc.org"),
		goleak.IgnoreAnyFunction("poll.runtime_pollWait"),
		// Plan 17-01: adding the Google client pulls in go.opencensus.io, whose
		// stats worker is started from an init() and lives for the test binary.
		goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start"),
	)

	a := &WebhookAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "webhook",
		Name:     "test",
		Settings: map[string]string{"port": "0"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- a.Listen(ctx, events)
	}()

	if !waitForServer(t, a, 2*time.Second) {
		t.Fatal("server did not start in time")
	}

	// Send one POST to exercise the handler goroutine path
	resp, err := http.Post("http://"+a.addr+"/webhook", "text/plain", strings.NewReader("leak test"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// Drain event
	select {
	case <-events:
	case <-time.After(time.Second):
	}

	cancel()
	<-listenErr
	// goleak.VerifyNone will verify no goroutines leaked
}

// waitForServer polls the adapter address until it accepts connections or timeout.
func waitForServer(t *testing.T, a *WebhookAdapter, timeout time.Duration) bool {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			return false
		case <-time.After(10 * time.Millisecond):
			if err := a.HealthCheck(); err == nil {
				return true
			}
		}
	}
}

func TestWebhook_Listen_NonJSON_RawPayload(t *testing.T) {
	a := &WebhookAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "webhook",
		Name:     "test",
		Settings: map[string]string{"port": "0"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- a.Listen(ctx, events)
	}()

	if !waitForServer(t, a, 2*time.Second) {
		t.Fatal("server did not start in time")
	}

	// Non-JSON body: RawPayload should be nil
	resp, err := http.Post("http://"+a.addr+"/webhook", "text/plain", strings.NewReader("not json"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	select {
	case evt := <-events:
		if evt.RawPayload != nil {
			t.Errorf("expected nil RawPayload for non-JSON body, got %s", string(evt.RawPayload))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event received")
	}

	// JSON body: RawPayload should be set
	jsonBody := `{"key":"value"}`
	resp2, err := http.Post("http://"+a.addr+"/webhook", "application/json", strings.NewReader(jsonBody))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp2.Body.Close()

	select {
	case evt := <-events:
		if evt.RawPayload == nil {
			t.Error("expected non-nil RawPayload for JSON body")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event received")
	}

	cancel()
	<-listenErr
}

func TestWebhook_Setup_CustomBind(t *testing.T) {
	a := &WebhookAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "webhook",
		Name:     "test",
		Settings: map[string]string{"bind": "0.0.0.0", "port": "18461"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	expected := "0.0.0.0:18461"
	if a.addr != expected {
		t.Errorf("expected addr %q, got %q", expected, a.addr)
	}
}

func TestWebhook_Listen_ContextCancel(t *testing.T) {
	a := &WebhookAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "webhook",
		Name:     "test",
		Settings: map[string]string{"port": "0"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- a.Listen(ctx, events)
	}()

	if !waitForServer(t, a, 2*time.Second) {
		t.Fatal("server did not start in time")
	}

	cancel()

	select {
	case err := <-listenErr:
		if err != nil {
			// nil is acceptable for graceful shutdown
			fmt.Printf("Listen returned: %v\n", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Listen did not return within 5s after cancel")
	}
}
