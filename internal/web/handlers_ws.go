package web

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

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
	SessionID string    `json:"sessionId,omitempty"`
	Profile   string    `json:"profile,omitempty"`
	ReadOnly  bool      `json:"readOnly,omitempty"`
	Time      time.Time `json:"time,omitempty"`
}

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(_ *http.Request) bool {
		// Token/auth checks are handled in authorizeWSRequest.
		// For local-first development we allow all origins.
		return true
	},
}

func (s *Server) handleSessionWS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}

	const prefix = "/ws/session/"
	sessionID := strings.TrimPrefix(r.URL.Path, prefix)
	if sessionID == "" || strings.Contains(sessionID, "/") {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "session id is required")
		return
	}

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
		bridge, err = newTmuxPTYBridge(menuSession.TmuxSession, sessionID, writer)
		if err != nil {
			log.Printf("web terminal attach failed session_id=%s tmux_session=%s err=%v", sessionID, menuSession.TmuxSession, err)
			code := "TERMINAL_ATTACH_FAILED"
			message := "failed to attach terminal bridge"
			if errors.Is(err, ErrTmuxSessionNotFound) {
				code = "TMUX_SESSION_NOT_FOUND"
				message = "tmux session is not available"
			}
			_ = writer.WriteJSON(wsServerMessage{
				Type:      "error",
				Code:      code,
				Message:   message,
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
			if websocket.IsUnexpectedCloseError(
				err,
				websocket.CloseNormalClosure,
				websocket.CloseGoingAway,
				websocket.CloseNoStatusReceived,
			) {
				log.Printf("web websocket closed unexpectedly session_id=%s err=%v", sessionID, err)
			}
			return
		}

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
