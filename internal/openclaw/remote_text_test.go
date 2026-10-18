package openclaw

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/gorilla/websocket"
)

// The CLI prints connect errors to the terminal. Text the gateway chose (its
// rejection message, a frame type, a response ID) must reach that error with
// control characters removed, so a hostile gateway cannot send escape codes.
func TestClientConnectRejectionDropsControlCharacters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if err := conn.WriteJSON(EventFrame{
			Type:    FrameTypeEvent,
			Event:   "connect.challenge",
			Payload: mustMarshal(ChallengePayload{Nonce: "n"}),
		}); err != nil {
			return
		}
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req RequestFrame
		if err := json.Unmarshal(msg, &req); err != nil {
			return
		}
		_ = conn.WriteJSON(ResponseFrame{
			Type:  FrameTypeResponse,
			ID:    req.ID,
			OK:    false,
			Error: &ErrorShape{Code: "NOT\x1b[31m_LINKED", Message: "bad\x1b]0;title\x07\r\npassword"},
		})
	}))
	defer server.Close()

	client := NewClient("ws"+strings.TrimPrefix(server.URL, "http"), "pw")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := client.Connect(ctx)
	if err == nil {
		t.Fatal("expected a rejection error")
	}
	msg := err.Error()
	if strings.IndexFunc(msg, unicode.IsControl) >= 0 {
		t.Fatalf("control character reached the error: %q", msg)
	}
	if !strings.Contains(msg, "password") {
		t.Fatalf("rejection text lost: %q", msg)
	}
}

func TestErrorShapeDropsControlCharacters(t *testing.T) {
	e := &ErrorShape{Code: "X\x1b", Message: "a\nb\u009bc"}
	if got := e.Error(); got != "X: abc" {
		t.Fatalf("Error() = %q, want %q", got, "X: abc")
	}
}

// A gateway can close the socket instead of answering; the close reason is its
// own text and ends up in the connect error.
func TestClientConnectCloseReasonDropsControlCharacters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "no\x1b]0;title\x07\r\nentry"))
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	client := NewClient("ws"+strings.TrimPrefix(server.URL, "http"), "pw")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := client.Connect(ctx)
	if err == nil {
		t.Fatal("expected a connect error")
	}
	msg := err.Error()
	if strings.IndexFunc(msg, unicode.IsControl) >= 0 {
		t.Fatalf("control character reached the error: %q", msg)
	}
	if !strings.Contains(msg, "entry") {
		t.Fatalf("close reason lost: %q", msg)
	}
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("error no longer wraps the close error: %v", err)
	}
}
