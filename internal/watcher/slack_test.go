package watcher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestSlack_Setup_DefaultServer(t *testing.T) {
	a := &SlackAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "slack",
		Name:     "test",
		Settings: map[string]string{"topic": "slack-bridge"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if a.server != "https://ntfy.sh" {
		t.Errorf("expected default server https://ntfy.sh, got %q", a.server)
	}
}

func TestSlack_Setup_CustomServer(t *testing.T) {
	a := &SlackAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "slack",
		Name:     "test",
		Settings: map[string]string{"topic": "slack-bridge", "server": "https://my.ntfy.example.com/"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if a.server != "https://my.ntfy.example.com" {
		t.Errorf("expected trailing slash trimmed, got %q", a.server)
	}
}

func TestSlack_Setup_MissingTopic(t *testing.T) {
	a := &SlackAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "slack",
		Name:     "test",
		Settings: map[string]string{},
	})
	if err == nil {
		t.Fatal("expected error for missing topic")
	}
}

// slackNtfyMsg builds an ntfy NDJSON line wrapping a Slack v2 payload in the Message field.
func slackNtfyMsg(id string, ts int64, slackPayload interface{}) []byte {
	payloadJSON, _ := json.Marshal(slackPayload)
	msg := ntfyMessage{
		ID:      id,
		Time:    ts,
		Event:   "message",
		Topic:   "slack-bridge",
		Message: string(payloadJSON),
	}
	data, _ := json.Marshal(msg)
	return data
}

func TestSlack_Listen_V2Payload(t *testing.T) {
	v2Payload := slackV2Payload{
		Type:        "message",
		V:           2,
		Channel:     "C0AABSF5GKD",
		User:        "U12345",
		TS:          "1712345678.123456",
		TextPreview: "Hello from Slack",
		SlackLink:   "https://slack.com/archives/C0AABSF5GKD/p1712345678123456",
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, _ := w.(http.Flusher)
		line := slackNtfyMsg("msg1", 1712345678, v2Payload)
		fmt.Fprintf(w, "%s\n", line)
		if flusher != nil {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer ts.Close()

	a := &SlackAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "slack",
		Name:     "test",
		Settings: map[string]string{"topic": "slack-bridge", "server": ts.URL},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = a.Listen(ctx, events)
	}()

	select {
	case evt := <-events:
		if evt.Source != "slack" {
			t.Errorf("expected Source=slack, got %q", evt.Source)
		}
		if evt.Subject != "Hello from Slack" {
			t.Errorf("expected Subject='Hello from Slack', got %q", evt.Subject)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for v2 event")
	}

	cancel()
}

func TestSlack_Listen_SenderFormat(t *testing.T) {
	v2Payload := slackV2Payload{
		Type:        "message",
		V:           2,
		Channel:     "C0AABSF5GKD",
		User:        "U12345",
		TS:          "1712345679.000001",
		TextPreview: "sender test",
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, _ := w.(http.Flusher)
		line := slackNtfyMsg("msg1", 1712345679, v2Payload)
		fmt.Fprintf(w, "%s\n", line)
		if flusher != nil {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer ts.Close()

	a := &SlackAdapter{}
	_ = a.Setup(context.Background(), AdapterConfig{
		Type:     "slack",
		Name:     "test",
		Settings: map[string]string{"topic": "slack-bridge", "server": ts.URL},
	})

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = a.Listen(ctx, events)
	}()

	select {
	case evt := <-events:
		expected := "slack:C0AABSF5GKD"
		if evt.Sender != expected {
			t.Errorf("expected Sender=%q, got %q", expected, evt.Sender)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}

	cancel()
}

func TestSlack_Listen_DedupKeyFormat(t *testing.T) {
	v2Payload := slackV2Payload{
		Type:    "message",
		V:       2,
		Channel: "C0AABSF5GKD",
		TS:      "1712345678.123456",
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, _ := w.(http.Flusher)
		line := slackNtfyMsg("msg1", 1712345678, v2Payload)
		fmt.Fprintf(w, "%s\n", line)
		if flusher != nil {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer ts.Close()

	a := &SlackAdapter{}
	_ = a.Setup(context.Background(), AdapterConfig{
		Type:     "slack",
		Name:     "test",
		Settings: map[string]string{"topic": "slack-bridge", "server": ts.URL},
	})

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = a.Listen(ctx, events)
	}()

	select {
	case evt := <-events:
		expected := "slack-C0AABSF5GKD-1712345678.123456"
		if evt.CustomDedupKey != expected {
			t.Errorf("expected CustomDedupKey=%q, got %q", expected, evt.CustomDedupKey)
		}
		// DedupKey() should return the custom key
		if evt.DedupKey() != expected {
			t.Errorf("expected DedupKey()=%q, got %q", expected, evt.DedupKey())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}

	cancel()
}

func TestSlack_Listen_ThreadReply(t *testing.T) {
	v2Payload := slackV2Payload{
		Type:        "message",
		V:           2,
		Channel:     "C0AABSF5GKD",
		User:        "U12345",
		TS:          "1712345690.000001",
		ThreadTS:    "1712345678.000000",
		TextPreview: "Thread reply",
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, _ := w.(http.Flusher)
		line := slackNtfyMsg("msg1", 1712345690, v2Payload)
		fmt.Fprintf(w, "%s\n", line)
		if flusher != nil {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer ts.Close()

	a := &SlackAdapter{}
	_ = a.Setup(context.Background(), AdapterConfig{
		Type:     "slack",
		Name:     "test",
		Settings: map[string]string{"topic": "slack-bridge", "server": ts.URL},
	})

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = a.Listen(ctx, events)
	}()

	select {
	case evt := <-events:
		expectedDedup := "slack-C0AABSF5GKD-1712345690.000001"
		expectedParent := "slack-C0AABSF5GKD-1712345678.000000"
		if evt.CustomDedupKey != expectedDedup {
			t.Errorf("expected CustomDedupKey=%q, got %q", expectedDedup, evt.CustomDedupKey)
		}
		if evt.ParentDedupKey != expectedParent {
			t.Errorf("expected ParentDedupKey=%q, got %q", expectedParent, evt.ParentDedupKey)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}

	cancel()
}

func TestSlack_Listen_V1Fallback(t *testing.T) {
	// Plain text message, not JSON
	msg := ntfyMessage{
		ID:      "msg1",
		Time:    1712345678,
		Event:   "message",
		Topic:   "slack-bridge",
		Message: "plain text notification from legacy bridge",
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, _ := w.(http.Flusher)
		data, _ := json.Marshal(msg)
		fmt.Fprintf(w, "%s\n", data)
		if flusher != nil {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer ts.Close()

	a := &SlackAdapter{}
	_ = a.Setup(context.Background(), AdapterConfig{
		Type:     "slack",
		Name:     "test",
		Settings: map[string]string{"topic": "slack-bridge", "server": ts.URL},
	})

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = a.Listen(ctx, events)
	}()

	select {
	case evt := <-events:
		if evt.Source != "slack" {
			t.Errorf("expected Source=slack, got %q", evt.Source)
		}
		if evt.Sender != "slack:unknown" {
			t.Errorf("expected Sender=slack:unknown for v1, got %q", evt.Sender)
		}
		if evt.CustomDedupKey != "" {
			t.Errorf("expected empty CustomDedupKey for v1, got %q", evt.CustomDedupKey)
		}
		if evt.Body != "plain text notification from legacy bridge" {
			t.Errorf("unexpected body: %q", evt.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}

	cancel()
}

func TestSlack_Listen_SkipsOpenKeepalive(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, _ := w.(http.Flusher)

		lines := []ntfyMessage{
			{ID: "open1", Time: time.Now().Unix(), Event: "open", Topic: "slack-bridge"},
			{ID: "ka1", Time: time.Now().Unix(), Event: "keepalive", Topic: "slack-bridge"},
		}
		for _, msg := range lines {
			data, _ := json.Marshal(msg)
			fmt.Fprintf(w, "%s\n", data)
			if flusher != nil {
				flusher.Flush()
			}
		}

		// Now send one actual message
		v2 := slackV2Payload{Type: "message", V: 2, Channel: "C123", TS: "1712345678.000000", TextPreview: "real event"}
		line := slackNtfyMsg("msg1", time.Now().Unix(), v2)
		fmt.Fprintf(w, "%s\n", line)
		if flusher != nil {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer ts.Close()

	a := &SlackAdapter{}
	_ = a.Setup(context.Background(), AdapterConfig{
		Type:     "slack",
		Name:     "test",
		Settings: map[string]string{"topic": "slack-bridge", "server": ts.URL},
	})

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = a.Listen(ctx, events)
	}()

	// Should only get 1 event (the message, not open/keepalive)
	select {
	case evt := <-events:
		if evt.Subject != "real event" {
			t.Errorf("expected Subject='real event', got %q", evt.Subject)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for message event")
	}

	// Verify no more events
	select {
	case evt := <-events:
		t.Errorf("unexpected extra event: %+v", evt)
	case <-time.After(200 * time.Millisecond):
		// Expected
	}

	cancel()
}

func TestSlack_Listen_ReconnectsOnDisconnect(t *testing.T) {
	var connCount atomic.Int32

	v2Payload := slackV2Payload{Type: "message", V: 2, Channel: "C123", TextPreview: "reconnect test"}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := connCount.Add(1)
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, _ := w.(http.Flusher)

		v2Payload.TS = fmt.Sprintf("1712345678.%06d", n)
		line := slackNtfyMsg(fmt.Sprintf("msg%d", n), time.Now().Unix(), v2Payload)
		fmt.Fprintf(w, "%s\n", line)
		if flusher != nil {
			flusher.Flush()
		}

		if n == 1 {
			return // Close first connection
		}
		<-r.Context().Done()
	}))
	defer ts.Close()

	a := &SlackAdapter{}
	_ = a.Setup(context.Background(), AdapterConfig{
		Type:     "slack",
		Name:     "test",
		Settings: map[string]string{"topic": "slack-bridge", "server": ts.URL},
	})
	a.initialBackoff = 100 * time.Millisecond

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = a.Listen(ctx, events)
	}()

	var received []Event
	for i := 0; i < 2; i++ {
		select {
		case evt := <-events:
			received = append(received, evt)
		case <-time.After(10 * time.Second):
			t.Fatalf("timeout waiting for event %d (got %d so far)", i+1, len(received))
		}
	}

	if len(received) != 2 {
		t.Fatalf("expected 2 events across reconnect, got %d", len(received))
	}
	if count := connCount.Load(); count < 2 {
		t.Errorf("expected at least 2 connections, got %d", count)
	}

	cancel()
}

func TestSlack_HealthCheck_Reachable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	a := &SlackAdapter{}
	_ = a.Setup(context.Background(), AdapterConfig{
		Type:     "slack",
		Name:     "test",
		Settings: map[string]string{"topic": "test", "server": ts.URL},
	})

	if err := a.HealthCheck(); err != nil {
		t.Errorf("expected nil from HealthCheck when server is reachable, got %v", err)
	}
}

func TestSlack_HealthCheck_Unreachable(t *testing.T) {
	a := &SlackAdapter{}
	_ = a.Setup(context.Background(), AdapterConfig{
		Type:     "slack",
		Name:     "test",
		Settings: map[string]string{"topic": "test", "server": "http://127.0.0.1:19998"},
	})

	if err := a.HealthCheck(); err == nil {
		t.Error("expected error from HealthCheck when server is unreachable")
	}
}

func TestSlack_Listen_StopNoLeaks(t *testing.T) {
	defer goleak.VerifyNone(t,
		goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"),
		goleak.IgnoreTopFunction("database/sql.(*DB).connectionResetter"),
		goleak.IgnoreAnyFunction("modernc.org"),
		goleak.IgnoreAnyFunction("poll.runtime_pollWait"),
		// Plan 17-01: adding the Google client pulls in go.opencensus.io, whose
		// stats worker is started from an init() and lives for the test binary.
		goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start"),
	)

	v2Payload := slackV2Payload{
		Type:        "message",
		V:           2,
		Channel:     "C0AABSF5GKD",
		TS:          "1712345678.000001",
		TextPreview: "leak test",
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, _ := w.(http.Flusher)
		line := slackNtfyMsg("msg1", time.Now().Unix(), v2Payload)
		fmt.Fprintf(w, "%s\n", line)
		if flusher != nil {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer ts.Close()

	a := &SlackAdapter{}
	_ = a.Setup(context.Background(), AdapterConfig{
		Type:     "slack",
		Name:     "test",
		Settings: map[string]string{"topic": "slack-bridge", "server": ts.URL},
	})

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- a.Listen(ctx, events)
	}()

	// Wait for event
	select {
	case <-events:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for event")
	}

	cancel()
	select {
	case <-listenErr:
	case <-time.After(5 * time.Second):
		t.Fatal("Listen did not return after cancel")
	}
	// goleak.VerifyNone checks for leaked goroutines via defer
}

// TestSlack_NormalizeV2_BodyIsFullText pins the slack-truncation fix: the
// Subject is the (first-line, 200-byte) label, but Body carries the COMPLETE
// multi-line message text so the conductor bridge forwards the whole thing.
func TestSlack_NormalizeV2_BodyIsFullText(t *testing.T) {
	full := "first line\nsecond line\nтретья строка"
	payload := slackV2Payload{
		Type:        "message",
		V:           2,
		Channel:     "C0AABSF5GKD",
		User:        "U12345",
		TS:          "1712345678.123456",
		TextPreview: full,
	}
	a := &SlackAdapter{}
	evt := a.normalizeV2(ntfyMessage{Time: 1712345678}, payload, []byte("{}"))

	if evt.Subject != "first line" {
		t.Errorf("Subject: want first line only, got %q", evt.Subject)
	}
	if evt.Body != full {
		t.Errorf("Body: want full text %q, got %q", full, evt.Body)
	}
}
