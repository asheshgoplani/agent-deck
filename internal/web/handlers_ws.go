package web

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/gorilla/websocket"
)

type wsClientMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

type wsServerMessage struct {
	Type      string    `json:"type"` // status, error
	Event     string    `json:"event,omitempty"`
	Code      string    `json:"code,omitempty"`
	Message   string    `json:"message,omitempty"`
	Hint      string    `json:"hint,omitempty"` // #782: actionable next step for terminal-fatal errors
	SessionID string    `json:"sessionId,omitempty"`
	Profile   string    `json:"profile,omitempty"`
	ReadOnly  bool      `json:"readOnly,omitempty"`
	Time      time.Time `json:"time,omitempty"`
}

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     allowWSOrigin,
}

// WebSocket keepalive tunables. The terminal socket sends protocol-level pings
// every wsPingPeriod and tears the connection down if no pong (or any message)
// arrives within wsPongWait, so a vanished peer can't leave the read loop — and
// its tmux attach bridge — blocked forever. Overridable in tests.
var (
	wsPongWait   = 60 * time.Second
	wsPingPeriod = (wsPongWait * 9) / 10
)

func allowWSOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}

	originURL, err := url.Parse(origin)
	if err != nil || originURL.Host == "" {
		return false
	}

	return strings.EqualFold(originURL.Host, r.Host)
}

func (s *Server) handleSessionWS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	if !s.authorizeWSRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}

	const prefix = "/ws/session/"
	sessionID := strings.TrimPrefix(r.URL.Path, prefix)
	if sessionID == "" || strings.Contains(sessionID, "/") {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "session id is required")
		return
	}
	// sessionID is attacker-controlled (raw URL path segment); every log call
	// below must use this sanitized copy, never sessionID itself, so a crafted
	// CRLF/control-char id can't forge fake log lines (go/log-injection).
	logSessionID := logging.SanitizeValue(sessionID)

	snapshot, err := s.menuData.LoadMenuSnapshot()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load session data")
		return
	}

	menuSession, found := snapshotSessionByID(snapshot, sessionID)
	if !found {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Keepalive so dead peers are detected and the deferred bridge teardown
	// actually runs. Without this, an idle session whose client vanished
	// (network drop, mobile app killed) leaves the read loop below blocked
	// forever in ReadMessage, so `defer bridge.Close()` never fires and the
	// tmux attach client leaks. Under `window-size largest` a single leaked
	// wide client then pins the shared window geometry for every other viewer
	// — the symptom being a phone terminal that stops wrapping to its screen.
	// We send protocol-level pings and require a pong within pongWait; both
	// browsers and URLSessionWebSocketTask answer pings automatically.
	pongWait, pingPeriod := wsPongWait, wsPingPeriod
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-stopPing:
				return
			case <-ticker.C:
				// WriteControl may be called concurrently with the writer. A
				// transient send failure (e.g. brief writer-lock contention)
				// must not permanently stop pings and strand a healthy peer —
				// the read deadline is the sole liveness arbiter, so just retry
				// on the next tick. A genuinely dead conn is reaped read-side,
				// after which close(stopPing) ends this goroutine.
				_ = conn.WriteControl(websocket.PingMessage, nil,
					time.Now().Add(10*time.Second))
			}
		}
	}()

	writer := newWSConnWriter(conn)

	_ = writer.WriteJSON(wsServerMessage{
		Type:      "status",
		Event:     "connected",
		SessionID: sessionID,
		Profile:   snapshot.Profile,
		ReadOnly:  s.cfg.ReadOnly,
		Time:      time.Now().UTC(),
	})
	_ = writer.WriteJSON(wsServerMessage{
		Type:      "status",
		Event:     "ready",
		SessionID: sessionID,
		Time:      time.Now().UTC(),
	})

	var bridge *tmuxPTYBridge
	if menuSession.TmuxSession != "" {
		bridge, err = newTmuxPTYBridge(menuSession.TmuxSession, menuSession.TmuxSocketName, sessionID, writer)
		if err != nil {
			logging.ForComponent(logging.CompWeb).Error("terminal_attach_failed",
				slog.String("session_id", logSessionID),
				slog.String("tmux_session", menuSession.TmuxSession),
				slog.String("error", err.Error()))
			code := "TERMINAL_ATTACH_FAILED"
			message := "failed to attach terminal bridge"
			// #782: terminal-fatal errors get an actionable hint so the
			// WebUI can render guidance instead of repeating an opaque
			// `[error:CODE]` line on every reconnect attempt.
			hint := "Check the server logs for details."
			if errors.Is(err, ErrTmuxSessionNotFound) {
				code = "TMUX_SESSION_NOT_FOUND"
				message = "tmux session is not available"
				hint = "The tmux session for this entry no longer exists. Restart it from the sidebar (Restart icon, or press 'r' with the row focused) to create a fresh tmux session."
			}
			_ = writer.WriteJSON(wsServerMessage{
				Type:      "error",
				Code:      code,
				Message:   message,
				Hint:      hint,
				SessionID: sessionID,
				Time:      time.Now().UTC(),
			})
		} else {
			defer bridge.Close()
			_ = writer.WriteJSON(wsServerMessage{
				Type:      "status",
				Event:     "terminal_attached",
				SessionID: sessionID,
				Time:      time.Now().UTC(),
			})
		}
	}

	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			// A keepalive read-deadline reap surfaces as a net.Error timeout,
			// which IsUnexpectedCloseError treats as expected — so log it
			// explicitly, otherwise a dead-peer teardown leaves no trace and is
			// indistinguishable from a normal close in production logs.
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				logging.ForComponent(logging.CompWeb).Warn("websocket_keepalive_timeout",
					slog.String("session_id", logSessionID),
					slog.String("error", logging.SanitizeValue(err.Error())))
			} else if websocket.IsUnexpectedCloseError(
				err,
				websocket.CloseNormalClosure,
				websocket.CloseGoingAway,
				websocket.CloseNoStatusReceived,
			) {
				// err may be a *websocket.CloseError whose Text is the
				// peer-supplied close reason (attacker-controlled) — sanitize
				// it same as logSessionID above (go/log-injection).
				logging.ForComponent(logging.CompWeb).Warn("websocket_closed_unexpectedly",
					slog.String("session_id", logSessionID),
					slog.String("error", logging.SanitizeValue(err.Error())))
			}
			return
		}
		// A real message is also liveness — extend the deadline alongside pongs.
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))

		var msg wsClientMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			_ = writer.WriteJSON(wsServerMessage{
				Type:      "error",
				Code:      "INVALID_MESSAGE",
				Message:   "invalid json payload",
				SessionID: sessionID,
				Time:      time.Now().UTC(),
			})
			continue
		}

		switch msg.Type {
		case "ping":
			_ = writer.WriteJSON(wsServerMessage{
				Type:      "status",
				Event:     "pong",
				SessionID: sessionID,
				Time:      time.Now().UTC(),
			})
		case "input":
			if s.cfg.ReadOnly {
				_ = writer.WriteJSON(wsServerMessage{
					Type:      "error",
					Code:      "READ_ONLY",
					Message:   "input is disabled in read-only mode",
					SessionID: sessionID,
					Time:      time.Now().UTC(),
				})
				continue
			}
			if bridge == nil {
				_ = writer.WriteJSON(wsServerMessage{
					Type:      "error",
					Code:      "NO_TERMINAL_BRIDGE",
					Message:   "terminal bridge is not attached",
					SessionID: sessionID,
					Time:      time.Now().UTC(),
				})
				continue
			}
			if err := bridge.WriteInput(msg.Data); err != nil {
				_ = writer.WriteJSON(wsServerMessage{
					Type:      "error",
					Code:      "INPUT_WRITE_FAILED",
					Message:   "failed to send input to terminal",
					SessionID: sessionID,
					Time:      time.Now().UTC(),
				})
			}
		case "resize":
			if bridge == nil {
				_ = writer.WriteJSON(wsServerMessage{
					Type:      "error",
					Code:      "NO_TERMINAL_BRIDGE",
					Message:   "terminal bridge is not attached",
					SessionID: sessionID,
					Time:      time.Now().UTC(),
				})
				continue
			}
			if err := bridge.Resize(msg.Cols, msg.Rows); err != nil {
				_ = writer.WriteJSON(wsServerMessage{
					Type:      "error",
					Code:      "RESIZE_FAILED",
					Message:   "failed to resize terminal",
					SessionID: sessionID,
					Time:      time.Now().UTC(),
				})
			}
		default:
			_ = writer.WriteJSON(wsServerMessage{
				Type:      "error",
				Code:      "UNSUPPORTED_MESSAGE",
				Message:   "supported message types: ping,input,resize",
				SessionID: sessionID,
				Time:      time.Now().UTC(),
			})
		}
	}
}

func snapshotSessionByID(snapshot *MenuSnapshot, sessionID string) (*MenuSession, bool) {
	if snapshot == nil {
		return nil, false
	}
	for _, item := range snapshot.Items {
		if item.Type != MenuItemTypeSession || item.Session == nil {
			continue
		}
		if item.Session.ID == sessionID {
			return item.Session, true
		}
	}
	return nil, false
}
