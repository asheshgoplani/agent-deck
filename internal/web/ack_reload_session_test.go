package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// GET /api/session/{id} rebuilds every session from state.db and runs the
// web status pass over the hook files on disk. A session the user already
// looked at must stay seen (idle) when its Stop hook is older than the visit,
// and turn waiting again when the Stop is newer.

// seedAckedWebSession writes one waiting Claude session on a live, inert pane
// of a private tmux server, marks it seen visitAgo ago, and leaves a Stop hook
// file from stopAgo ago. It returns the profile and the session id.
func seedAckedWebSession(t *testing.T, visitAgo, stopAgo time.Duration) (profile, id string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	if os.Getenv("AGENT_DECK_TEST_HOME_ISOLATED") != "1" || os.Getenv("AGENT_DECK_TEST_ISOLATED") != "1" {
		t.Skip("needs the isolated HOME and tmux dir from TestMain")
	}
	t.Setenv("AGENTDECK_SKIP_UPDATE_CHECK", "1")

	seq := menuStatusPassSeq.Add(1)
	profile = fmt.Sprintf("ackreload-%d-%d", os.Getpid(), seq)
	socket := fmt.Sprintf("ackreload%d-%d", os.Getpid(), seq)
	id = fmt.Sprintf("ack-reload-%d", seq)

	path, err := session.GetDBPathForProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := statedb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	row := &statedb.InstanceRow{
		ID: id, Title: "ack reload", ProjectPath: project, GroupPath: "my-sessions",
		Tool: "claude", Status: string(session.StatusWaiting), TmuxSession: "agentdeck_" + id,
		TmuxSocketName: socket, CreatedAt: time.Now().Add(-time.Hour), ToolData: json.RawMessage(`{}`),
	}
	if err := db.SaveInstance(row); err != nil {
		t.Fatal(err)
	}
	if err := db.SetAcknowledged(id, true); err != nil {
		t.Fatal(err)
	}
	// SetAcknowledged stamps the visit now; move it back for an earlier visit.
	if visitAgo > 0 {
		if _, err := db.DB().Exec(`UPDATE instances SET acknowledged_at = ? WHERE id = ?`, time.Now().Add(-visitAgo).UnixMilli(), id); err != nil {
			t.Fatal(err)
		}
	}

	hook, _ := json.Marshal(map[string]any{"status": "waiting", "event": "Stop", "cwd": project, "ts": time.Now().Add(-stopAgo).Unix()})
	hookPath := filepath.Join(session.GetHooksDir(), id+".json")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, hook, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(hookPath) })

	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	if out, err := exec.Command("tmux", "-L", socket, "new-session", "-d", "-s", row.TmuxSession, "sleep", "300").CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v %s", err, out)
	}
	prev := tmux.DefaultSocketName()
	tmux.SetDefaultSocketName(socket)
	t.Cleanup(func() { tmux.SetDefaultSocketName(prev) })
	return profile, id
}

// sessionAPIStatus returns the status GET /api/session/{id} reports, through
// the server's default storage-backed menu data.
func sessionAPIStatus(t *testing.T, profile, id string) session.Status {
	t.Helper()
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Profile: profile})
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, newLocalRequest(http.MethodGet, "/api/session/"+id, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/session/%s: %d %s", id, rr.Code, rr.Body.String())
	}
	var resp sessionDetailsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil || resp.Session == nil {
		t.Fatalf("decode response: %v %s", err, rr.Body.String())
	}
	return resp.Session.Status
}

// A Stop recorded before the user's visit is the turn they already saw.
func TestSessionAPIAck_StopOlderThanVisitStaysIdle(t *testing.T) {
	profile, id := seedAckedWebSession(t, 0, 30*time.Second)
	if got := sessionAPIStatus(t, profile, id); got != session.StatusIdle {
		t.Fatalf("GET /api/session status = %q, want idle: the reload undid the user's visit", got)
	}
}

// A Stop recorded after the visit is a turn the user has not seen yet.
func TestSessionAPIAck_StopNewerThanVisitIsWaiting(t *testing.T) {
	profile, id := seedAckedWebSession(t, 10*time.Minute, 30*time.Second)
	if got := sessionAPIStatus(t, profile, id); got != session.StatusWaiting {
		t.Fatalf("GET /api/session status = %q, want waiting: a turn finished after the visit shows as seen", got)
	}
}
