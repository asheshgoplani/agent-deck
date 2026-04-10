package web

import (
	"encoding/json"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestAPIErrorCodeConstantsAreNonEmpty(t *testing.T) {
	constants := map[string]string{
		"ErrCodeUnauthorized":     ErrCodeUnauthorized,
		"ErrCodeForbidden":        ErrCodeForbidden,
		"ErrCodeNotFound":         ErrCodeNotFound,
		"ErrCodeBadRequest":       ErrCodeBadRequest,
		"ErrCodeMethodNotAllowed": ErrCodeMethodNotAllowed,
		"ErrCodeRateLimited":      ErrCodeRateLimited,
		"ErrCodeInternalError":    ErrCodeInternalError,
		"ErrCodeNotImplemented":   ErrCodeNotImplemented,
		"ErrCodeReadOnly":         ErrCodeReadOnly,
	}
	for name, val := range constants {
		if val == "" {
			t.Errorf("error code constant %s is empty", name)
		}
	}
}

func TestCreateSessionRequestJSONRoundTrip(t *testing.T) {
	original := CreateSessionRequest{
		Title:       "My Session",
		Tool:        "claude",
		ProjectPath: "/home/user/project",
		GroupPath:   "work/infra",
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var decoded CreateSessionRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if decoded.Title != original.Title {
		t.Errorf("Title mismatch: got %q, want %q", decoded.Title, original.Title)
	}
	if decoded.Tool != original.Tool {
		t.Errorf("Tool mismatch: got %q, want %q", decoded.Tool, original.Tool)
	}
	if decoded.ProjectPath != original.ProjectPath {
		t.Errorf("ProjectPath mismatch: got %q, want %q", decoded.ProjectPath, original.ProjectPath)
	}
	if decoded.GroupPath != original.GroupPath {
		t.Errorf("GroupPath mismatch: got %q, want %q", decoded.GroupPath, original.GroupPath)
	}
}

func TestCreateSessionRequestGroupPathOmitempty(t *testing.T) {
	req := CreateSessionRequest{
		Title:       "Test",
		Tool:        "claude",
		ProjectPath: "/path",
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal to map failed: %v", err)
	}
	if _, exists := m["groupPath"]; exists {
		t.Error("groupPath should be omitted when empty")
	}
}

func TestSessionActionResponseJSONRoundTrip(t *testing.T) {
	original := SessionActionResponse{
		SessionID: "sess-abc",
		Status:    session.StatusRunning,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var decoded SessionActionResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if decoded.SessionID != "sess-abc" {
		t.Errorf("SessionID mismatch: got %q", decoded.SessionID)
	}
	if decoded.Status != session.StatusRunning {
		t.Errorf("Status mismatch: got %q", decoded.Status)
	}
}

func TestSSESessionEventJSONRoundTrip(t *testing.T) {
	original := SSESessionEvent{
		EventType: "session:created",
		Session: &MenuSession{
			ID:    "sess-1",
			Title: "Test",
		},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var decoded SSESessionEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if decoded.EventType != "session:created" {
		t.Errorf("EventType mismatch: got %q", decoded.EventType)
	}
	if decoded.Session == nil || decoded.Session.ID != "sess-1" {
		t.Errorf("Session mismatch: got %+v", decoded.Session)
	}
}

func TestSSEDeleteEventJSONRoundTrip(t *testing.T) {
	original := SSEDeleteEvent{
		EventType: "session:deleted",
		ID:        "sess-42",
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var decoded SSEDeleteEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if decoded.EventType != "session:deleted" {
		t.Errorf("EventType mismatch: got %q", decoded.EventType)
	}
	if decoded.ID != "sess-42" {
		t.Errorf("ID mismatch: got %q", decoded.ID)
	}
}

func TestSettingsResponseJSONRoundTrip(t *testing.T) {
	original := SettingsResponse{
		Profile:      "work",
		ReadOnly:     false,
		WebMutations: true,
		Version:      "0.26.4",
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var decoded SettingsResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if decoded.Profile != "work" {
		t.Errorf("Profile mismatch: got %q", decoded.Profile)
	}
	if decoded.ReadOnly != false {
		t.Errorf("ReadOnly mismatch: got %v", decoded.ReadOnly)
	}
	if decoded.WebMutations != true {
		t.Errorf("WebMutations mismatch: got %v", decoded.WebMutations)
	}
	if decoded.Version != "0.26.4" {
		t.Errorf("Version mismatch: got %q", decoded.Version)
	}
}

func TestSSEGroupEventJSONFields(t *testing.T) {
	original := SSEGroupEvent{
		EventType: "group:created",
		Group: &MenuGroup{
			Name: "infra",
			Path: "work/infra",
		},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if _, ok := m["eventType"]; !ok {
		t.Error("missing eventType field")
	}
	if _, ok := m["group"]; !ok {
		t.Error("missing group field")
	}
}

func TestSSECostEventJSONFields(t *testing.T) {
	original := SSECostEvent{
		EventType: "cost:updated",
		SessionID: "sess-99",
		Cost:      1.23,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if _, ok := m["eventType"]; !ok {
		t.Error("missing eventType field")
	}
	if _, ok := m["sessionId"]; !ok {
		t.Error("missing sessionId field")
	}
	if _, ok := m["cost"]; !ok {
		t.Error("missing cost field")
	}
}
