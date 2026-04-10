package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// newTestCostStore creates an in-memory cost store backed by a temp-dir SQLite database.
func newTestCostStore(t *testing.T) *costs.Store {
	t.Helper()
	dir := t.TempDir()
	sdb, err := statedb.Open(filepath.Join(dir, "costs_test.db"))
	if err != nil {
		t.Fatalf("statedb.Open: %v", err)
	}
	if err := sdb.Migrate(); err != nil {
		t.Fatalf("sdb.Migrate: %v", err)
	}
	t.Cleanup(func() { sdb.Close() })
	return costs.NewStore(sdb.DB())
}

func TestCostsBatch(t *testing.T) {
	store := newTestCostStore(t)

	// Record $0.05 for sess1 (50000 microdollars)
	if err := store.WriteCostEvent(costs.CostEvent{
		ID:               "evt-1",
		SessionID:        "sess1",
		Timestamp:        time.Now(),
		Model:            "claude-sonnet-4-6",
		CostMicrodollars: 50000,
	}); err != nil {
		t.Fatalf("WriteCostEvent sess1: %v", err)
	}

	// Record $1.20 for sess2 (1200000 microdollars)
	if err := store.WriteCostEvent(costs.CostEvent{
		ID:               "evt-2",
		SessionID:        "sess2",
		Timestamp:        time.Now(),
		Model:            "claude-sonnet-4-6",
		CostMicrodollars: 1200000,
	}); err != nil {
		t.Fatalf("WriteCostEvent sess2: %v", err)
	}

	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.SetCostStore(store)

	req := httptest.NewRequest(http.MethodGet, "/api/costs/batch?ids=sess1,sess2", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Costs map[string]float64 `json:"costs"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Costs == nil {
		t.Fatal("expected non-nil costs map")
	}
	if got := resp.Costs["sess1"]; got < 0.049 || got > 0.051 {
		t.Errorf("sess1 cost = %f, want ~0.05", got)
	}
	if got := resp.Costs["sess2"]; got < 1.19 || got > 1.21 {
		t.Errorf("sess2 cost = %f, want ~1.20", got)
	}
}

func TestCostsBatchNoCostStore(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	// Intentionally do NOT call SetCostStore — costStore remains nil

	req := httptest.NewRequest(http.MethodGet, "/api/costs/batch?ids=sess1", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with nil costStore, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Costs map[string]float64 `json:"costs"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Costs == nil {
		t.Fatal("expected non-nil empty costs map")
	}
	if len(resp.Costs) != 0 {
		t.Errorf("expected empty costs map, got %v", resp.Costs)
	}
}

func TestCostsBatchUnauthorized(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Token:      "secret-token",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/costs/batch?ids=sess1", nil)
	// No Authorization header
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "UNAUTHORIZED") {
		t.Fatalf("expected UNAUTHORIZED in body, got: %s", rr.Body.String())
	}
}

func TestCostsBatchEmptyIDs(t *testing.T) {
	store := newTestCostStore(t)
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.SetCostStore(store)

	req := httptest.NewRequest(http.MethodGet, "/api/costs/batch?ids=", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Costs map[string]float64 `json:"costs"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Costs) != 0 {
		t.Errorf("expected empty costs for empty ids, got %v", resp.Costs)
	}
}

func TestCostsBatchMethodNotAllowed(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})

	// PERF-I: POST is now allowed (JSON body form). PUT / DELETE / PATCH
	// remain disallowed so the 405 path still has coverage.
	req := httptest.NewRequest(http.MethodPut, "/api/costs/batch", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for PUT, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestCostsBatchPOSTJSONBody exercises the PERF-I POST form with a JSON
// body — mirrors TestCostsBatch but uses the new transport. Guards the
// 414 URI Too Long regression that would come back if the frontend fell
// back to GET + query string for long session lists.
func TestCostsBatchPOSTJSONBody(t *testing.T) {
	store := newTestCostStore(t)
	if err := store.WriteCostEvent(costs.CostEvent{
		ID:               "evt-post-1",
		SessionID:        "sessA",
		Timestamp:        time.Now(),
		Model:            "claude-sonnet-4-6",
		CostMicrodollars: 75000,
	}); err != nil {
		t.Fatalf("WriteCostEvent sessA: %v", err)
	}

	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.SetCostStore(store)

	body := strings.NewReader(`{"ids":["sessA","sessB"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/costs/batch", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Costs map[string]float64 `json:"costs"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := resp.Costs["sessA"]; got < 0.074 || got > 0.076 {
		t.Errorf("sessA cost = %f, want ~0.075", got)
	}
}

func TestCostsBatchPOSTInvalidBody(t *testing.T) {
	store := newTestCostStore(t)
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.SetCostStore(store)

	req := httptest.NewRequest(http.MethodPost, "/api/costs/batch", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid json, got %d: %s", rr.Code, rr.Body.String())
	}
}
