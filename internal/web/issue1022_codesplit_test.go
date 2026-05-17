package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// TestWebBundle_ChartNotInInitialPayload_RegressionFor1022 guards against
// chart.umd.min.js (~206 KB) being shipped in the initial page payload.
//
// Issue #1022 part 2: weekly Lighthouse showed script.size = 291 KB vs
// 120 KB budget because index.html eagerly loaded the Chart.js UMD
// bundle. Chart.js is only used by the Costs route (CostDashboard), so
// it must be code-split / lazy-loaded on demand. This test fails if a
// future refactor reintroduces an eager <script src="chart.umd*">
// reference in the served HTML or in the entry JS module.
func TestWebBundle_ChartNotInInitialPayload_RegressionFor1022(t *testing.T) {
	s := NewServer(Config{Token: "test-token"})

	// 1. Initial HTML payload must not reference chart.umd*.
	req := httptest.NewRequest(http.MethodGet, "/?token=test-token", nil)
	w := httptest.NewRecorder()
	s.handleIndex(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleIndex: expected 200, got %d", w.Code)
	}
	indexHTML := w.Body.String()

	// Match any reference to chart.umd in a script src or import. Comments
	// are ignored — the failure mode we care about is the asset being
	// pulled in by the browser on first paint.
	chartRef := regexp.MustCompile(`(?i)<script[^>]+src\s*=\s*['"][^'"]*chart\.umd[^'"]*['"]`)
	if chartRef.MatchString(indexHTML) {
		t.Errorf("index.html eagerly loads chart.umd.* — must be lazy-loaded from Costs route only.\n"+
			"Found in body:\n%s", firstChartLine(indexHTML))
	}

	// 2. Primary entry JS module must not statically import chart.umd*.
	// In dev mode (no manifest) the entry is /static/app/main.js — read
	// the embedded source directly so the assertion is independent of
	// route wiring.
	mainBody := readEmbedded(t, "static/app/main.js")
	if strings.Contains(mainBody, "chart.umd") {
		t.Errorf("main.js references chart.umd.* — must be dynamically imported from CostDashboard only")
	}

	// 3. App.js (mounted on first paint) must not statically reference chart.umd.
	appBody := readEmbedded(t, "static/app/App.js")
	if strings.Contains(appBody, "chart.umd") {
		t.Errorf("App.js references chart.umd.* — must be dynamically imported from CostDashboard only")
	}

	_ = s
}

// readEmbedded reads a file from the embedded static FS, failing the
// test if the file is missing.
func readEmbedded(t *testing.T, name string) string {
	t.Helper()
	data, err := embeddedStaticFiles.ReadFile(name)
	if err != nil {
		t.Fatalf("read embedded %s: %v", name, err)
	}
	return string(data)
}

// firstChartLine returns the first line of body containing "chart.umd"
// so test failures point at the offending tag directly.
func firstChartLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(strings.ToLower(line), "chart.umd") {
			return strings.TrimSpace(line)
		}
	}
	return "(no line matched — regex matched across lines)"
}
