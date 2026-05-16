package crossfixture_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil/crossfixture"
)

func TestNew_IsolatesHomeAndAgentDeckDir(t *testing.T) {
	cf := crossfixture.New(t, crossfixture.Options{})

	if cf.Home == "" {
		t.Fatal("Home empty")
	}
	if cf.AgentDeckDir == "" {
		t.Fatal("AgentDeckDir empty")
	}
	if got := os.Getenv("HOME"); got != cf.Home {
		t.Fatalf("HOME=%q want %q", got, cf.Home)
	}
	// The agent-deck dir must live under HOME so all production code paths
	// resolve to it (via os.UserHomeDir or HOME-relative paths).
	if rel, _ := filepath.Rel(cf.Home, cf.AgentDeckDir); rel == ".." || rel == "" || rel[0] == '/' {
		t.Fatalf("AgentDeckDir=%q not under Home=%q", cf.AgentDeckDir, cf.Home)
	}
	if _, err := os.Stat(cf.AgentDeckDir); err != nil {
		t.Fatalf("AgentDeckDir not created: %v", err)
	}
}

func TestAttachWeb_RoutesRequests(t *testing.T) {
	cf := crossfixture.New(t, crossfixture.Options{})

	// Stand up a tiny mux that returns the profile from env.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"profile": os.Getenv("AGENTDECK_PROFILE"),
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cf.AttachWeb(srv.URL)

	if cf.WebURL == "" {
		t.Fatal("WebURL empty after AttachWeb")
	}

	resp, err := http.Get(cf.WebURL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestGetJSON_DecodesBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/sessions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[{"id":"s1"},{"id":"s2"}]`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cf := crossfixture.New(t, crossfixture.Options{})
	cf.AttachWeb(srv.URL)

	var sessions []map[string]any
	cf.GetJSON(t, "/api/sessions", &sessions)

	if len(sessions) != 2 || sessions[0]["id"] != "s1" {
		t.Fatalf("decoded=%v", sessions)
	}
}

func TestAssertParity_DivergenceFails(t *testing.T) {
	cf := crossfixture.New(t, crossfixture.Options{})
	stub := &stubT{}
	cf.AssertParity(stub, crossfixture.Snapshots{
		CLI: []byte(`{"a":1}`),
		Web: []byte(`{"a":2}`),
		TUI: []byte(`{"a":1}`),
	})
	if !stub.failed {
		t.Fatal("AssertParity should have failed for diverged snapshots")
	}
}

func TestAssertParity_AgreementPasses(t *testing.T) {
	cf := crossfixture.New(t, crossfixture.Options{})
	cf.AssertParity(t, crossfixture.Snapshots{
		CLI: []byte(`{"a":1}`),
		Web: []byte(`{"a":1}`),
		TUI: []byte(`{"a":1}`),
	})
}

func TestRunCLI_RequiresBinaryPath(t *testing.T) {
	cf := crossfixture.New(t, crossfixture.Options{})

	// No binary attached -> RunCLI should error out clearly.
	_, err := cf.RunCLI("list", "--json")
	if err == nil {
		t.Fatal("expected error when no CLI binary attached")
	}
}

type stubT struct{ failed bool }

func (s *stubT) Errorf(format string, args ...any) { s.failed = true }
func (s *stubT) Fatalf(format string, args ...any) { s.failed = true }
func (s *stubT) Helper()                           {}
