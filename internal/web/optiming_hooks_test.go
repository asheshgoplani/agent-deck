package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/optiming"
)

// TestOptiming_MenuRecordsWebMenu: with timings on, one /api/menu request
// appends one web.menu line (ASK-106). Not parallel: timing state is global.
func TestOptiming_MenuRecordsWebMenu(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(optiming.SetForTest(dir, true))
	fx := newParityFixture()

	w := httptest.NewRecorder()
	fx.server.handleMenu(w, httptest.NewRequest(http.MethodGet, "/api/menu", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/menu: status=%d", w.Code)
	}
	entries, err := optiming.ReadFile(filepath.Join(dir, optiming.FileName), time.Time{})
	if err != nil || len(entries) != 1 || entries[0].Op != "web.menu" {
		t.Fatalf("entries = %+v err=%v, want one web.menu", entries, err)
	}
}
