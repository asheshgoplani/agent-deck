package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

var (
	menuEventsPollInterval      = 2 * time.Second
	menuEventsHeartbeatInterval = 15 * time.Second
)

func (s *Server) handleMenuEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "stream unavailable")
		return
	}

	snapshot, err := s.menuData.LoadMenuSnapshot()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load menu data")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	lastFingerprint := menuSnapshotFingerprint(snapshot)
	if err := writeSSEEvent(w, flusher, "menu", snapshot); err != nil {
		return
	}

	menuChanges := s.subscribeMenuChanges()
	defer s.unsubscribeMenuChanges(menuChanges)

	taskChanges := s.subscribeTaskChanges()
	defer s.unsubscribeTaskChanges(taskChanges)

	pollTicker := time.NewTicker(menuEventsPollInterval)
	defer pollTicker.Stop()

	heartbeatTicker := time.NewTicker(menuEventsHeartbeatInterval)
	defer heartbeatTicker.Stop()

	ctx := r.Context()
	emitIfChanged := func() error {
		nextSnapshot, err := s.menuData.LoadMenuSnapshot()
		if err != nil {
			logging.ForComponent(logging.CompWeb).Error("menu_stream_refresh_failed",
				slog.String("error", err.Error()))
			return nil
		}

		nextFingerprint := menuSnapshotFingerprint(nextSnapshot)
		if nextFingerprint == lastFingerprint {
			return nil
		}

		if err := writeSSEEvent(w, flusher, "menu", nextSnapshot); err != nil {
			return err
		}
		lastFingerprint = nextFingerprint
		return nil
	}

	emitTasksSnapshot := func() error {
		if s.hubTasks == nil {
			return nil
		}
		tasks, err := s.hubTasks.List()
		if err != nil {
			logging.ForComponent(logging.CompWeb).Error("task_stream_refresh_failed",
				slog.String("error", err.Error()))
			return nil
		}
		return writeSSEEvent(w, flusher, "tasks", tasksSSEPayload{Tasks: tasks})
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeatTicker.C:
			if err := writeSSEComment(w, flusher, "keepalive"); err != nil {
				return
			}
		case <-menuChanges:
			if err := emitIfChanged(); err != nil {
				return
			}
		case <-taskChanges:
			if err := emitTasksSnapshot(); err != nil {
				return
			}
		case <-pollTicker.C:
			if err := emitIfChanged(); err != nil {
				return
			}
		}
	}
}

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeSSEComment(w http.ResponseWriter, flusher http.Flusher, comment string) error {
	if _, err := fmt.Fprintf(w, ": %s\n\n", comment); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func menuSnapshotFingerprint(snapshot *MenuSnapshot) string {
	if snapshot == nil {
		return "nil"
	}
	fingerprintPayload := struct {
		Profile       string     `json:"profile"`
		TotalGroups   int        `json:"totalGroups"`
		TotalSessions int        `json:"totalSessions"`
		Items         []MenuItem `json:"items"`
	}{
		Profile:       snapshot.Profile,
		TotalGroups:   snapshot.TotalGroups,
		TotalSessions: snapshot.TotalSessions,
		Items:         snapshot.Items,
	}

	raw, err := json.Marshal(fingerprintPayload)
	if err != nil {
		return "marshal-error"
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
