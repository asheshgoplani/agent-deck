package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

const toolDataKimiSessionIDKey = "kimi_session_id"
const kimiOwnedSessionSuffix = ".kimi-session"

type kimiOwnedSession struct {
	ID string `json:"id"`
	At int64  `json:"at"`
}

func WriteKimiOwnedSessionID(instanceID, sessionID string) error {
	if !hermesHookInstanceIDPattern.MatchString(instanceID) || strings.Contains(instanceID, "..") {
		return fmt.Errorf("invalid instance id %q", instanceID)
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("empty Kimi session id")
	}
	path := filepath.Join(hermesHookScope(instanceID, false), instanceID+kimiOwnedSessionSuffix)
	if current, ok := readKimiOwnedSession(path); ok && current.ID == sessionID {
		return nil
	}
	return atomicHermesJSON(path, kimiOwnedSession{ID: sessionID, At: time.Now().UnixNano()})
}

func readKimiOwnedSession(path string) (kimiOwnedSession, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return kimiOwnedSession{}, false
	}
	var record kimiOwnedSession
	if json.Unmarshal(data, &record) != nil {
		return kimiOwnedSession{}, false
	}
	record.ID = strings.TrimSpace(record.ID)
	return record, record.ID != ""
}

func (i *Instance) kimiOwnedSession() (kimiOwnedSession, bool) {
	if !hermesHookInstanceIDPattern.MatchString(i.ID) || strings.Contains(i.ID, "..") {
		return kimiOwnedSession{}, false
	}
	sandboxed := i.IsSandboxed()
	for _, dir := range []string{hermesHookScope(i.ID, sandboxed), hermesHookScope(i.ID, !sandboxed)} {
		if record, ok := readKimiOwnedSession(filepath.Join(dir, i.ID+kimiOwnedSessionSuffix)); ok {
			return record, true
		}
	}
	return kimiOwnedSession{}, false
}

func (i *Instance) prepareKimiResume() {
	record, ok := i.kimiOwnedSession()
	if !ok {
		return
	}
	at := time.Unix(0, record.At).UTC()
	i.mu.Lock()
	if i.KimiSessionID != "" && i.KimiSessionID != record.ID && !at.After(i.kimiBoundAt) {
		i.mu.Unlock()
		return
	}
	changed := i.KimiSessionID != record.ID || !i.kimiBoundAt.Equal(at)
	i.KimiSessionID = record.ID
	i.kimiBoundAt = at
	i.mu.Unlock()
	if changed {
		i.persistKimiSessionBinding(record.ID, at)
	}
}

func (i *Instance) clearKimiOwnedSessionID() {
	if !hermesHookInstanceIDPattern.MatchString(i.ID) || strings.Contains(i.ID, "..") {
		return
	}
	for _, dir := range []string{hermesHookScope(i.ID, false), hermesHookScope(i.ID, true)} {
		_ = os.Remove(filepath.Join(dir, i.ID+kimiOwnedSessionSuffix))
	}
}

func WriteKimiSessionIDToToolData(td json.RawMessage, sessionID string) json.RawMessage {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return td
	}
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	raw, err := json.Marshal(sessionID)
	if err != nil {
		return td
	}
	m[toolDataKimiSessionIDKey] = raw
	out, err := json.Marshal(m)
	if err != nil {
		return td
	}
	return out
}

func ReadKimiSessionIDFromToolData(td json.RawMessage) string {
	if len(td) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(td, &m); err != nil {
		return ""
	}
	var id string
	if err := json.Unmarshal(m[toolDataKimiSessionIDKey], &id); err != nil {
		return ""
	}
	return strings.TrimSpace(id)
}

func ReadKimiDetectedAtFromToolData(td json.RawMessage) time.Time {
	if len(td) == 0 {
		return time.Time{}
	}
	var blob struct {
		At int64 `json:"kimi_detected_at"`
	}
	if json.Unmarshal(td, &blob) != nil || blob.At == 0 {
		return time.Time{}
	}
	return time.Unix(blob.At, 0).UTC()
}

func (i *Instance) persistKimiSessionBinding(sessionID string, detectedAt time.Time) {
	err := i.withOwningStateDB(func(db *statedb.StateDB) error {
		return db.WriteKimiSessionBinding(i.ID, sessionID, detectedAt)
	})
	if errors.Is(err, errNoStateDB) {
		return
	}
	if err != nil {
		sessionLog.Warn("kimi_session_bind_persist_failed",
			slog.String("instance_id", i.ID),
			slog.String("new_id", sessionID),
			slog.String("error", err.Error()))
	}
}

// bindKimiSessionFromHook binds only the managed process's SessionStart payload.
// Caller holds i.mu.
func (i *Instance) bindKimiSessionFromHook(sessionID, hookSource, hookEvent, clientType string, at time.Time) {
	if sessionID == "" || hookSource != "hook_payload" || hookEvent != "SessionStart" || clientType != "kimi_code_cli" {
		return
	}
	if sessionID == i.KimiSessionID {
		if at.After(i.kimiBoundAt) {
			i.kimiBoundAt = at
			i.persistKimiSessionBinding(sessionID, at)
		}
		return
	}
	if i.KimiSessionID != "" && at.Before(i.kimiBoundAt) {
		return
	}
	i.KimiSessionID = sessionID
	i.kimiBoundAt = at
	i.persistKimiSessionBinding(sessionID, at)
}
