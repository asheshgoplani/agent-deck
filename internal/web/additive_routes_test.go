package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The assistant connection feature is purely additive: it may add routes,
// never drop or rename one. routesBeforeAssistants is the full set of
// patterns registered in internal/web on main before that feature landed
// (82f89da). A missing pattern here means an existing client (browser, app,
// script) would start getting the index page or a 404 for a route it used.
var routesBeforeAssistants = []string{
	"/",
	"/api/command-center/status",
	"/api/costs/batch",
	"/api/costs/daily",
	"/api/costs/export",
	"/api/costs/groups",
	"/api/costs/models",
	"/api/costs/session",
	"/api/costs/sessions",
	"/api/costs/stream",
	"/api/costs/summary",
	"/api/files",
	"/api/groups",
	"/api/groups/",
	"/api/mcps",
	"/api/menu",
	"/api/path-suggestions",
	"/api/profiles",
	"/api/push/config",
	"/api/push/presence",
	"/api/push/subscribe",
	"/api/push/unsubscribe",
	"/api/remotes",
	"/api/session-directory",
	"/api/session/",
	"/api/sessions",
	"/api/sessions/",
	"/api/sessions/archived",
	"/api/settings",
	"/api/skills",
	"/api/system/stats",
	"/events/command-center",
	"/events/menu",
	"/g/",
	"/healthz",
	"/manifest.webmanifest",
	"/s/",
	"/static/",
	"/sw.js",
	"/ws/session/",
	"DELETE /api/sessions/{id}/mcps/{name}",
	"GET /api/sessions/{id}/mcps",
	"PATCH /api/sessions/{id}/mcps/{name}",
	"POST /api/command-center/ask",
	"POST /api/sessions/undelete",
	"POST /api/sessions/{id}/mcps/{name}",
}

func TestAssistantFeatureKeepsEveryExistingRoute(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing web sources: %v", err)
	}
	pat := regexp.MustCompile(`Handle(?:Func)?\("([^"]+)"`)
	have := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		for _, m := range pat.FindAllStringSubmatch(string(b), -1) {
			have[m[1]] = true
		}
	}
	for _, r := range routesBeforeAssistants {
		if !have[r] {
			t.Errorf("route %q existed before the assistant feature and is no longer registered", r)
		}
	}
}
