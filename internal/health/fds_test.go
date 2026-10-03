package health

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// withOpenFDs swaps the descriptor-count source for one test.
func withOpenFDs(t *testing.T, f func() (*int, bool)) {
	t.Helper()
	previous := sampleOpenFDs
	sampleOpenFDs = f
	t.Cleanup(func() { sampleOpenFDs = previous })
}

func sampleOnce(t *testing.T, d, role string) {
	t.Helper()
	Start(d, role, t.TempDir(), "test")()
}

func reportOf(t *testing.T, d string) Summary {
	t.Helper()
	r, err := Report(d, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Processes) == 0 {
		t.Fatalf("no processes in report: %+v", r)
	}
	return r
}

// #2427: on macOS open_fds was null in every sample, so the descriptor budget
// could never fire. The sampler must count its own descriptors natively on
// both supported platforms, and say "unsupported" anywhere else.
func TestSamplerPopulatesOpenFDsOnThisPlatform(t *testing.T) {
	d := t.TempDir()
	sampleOnce(t, d, "tui")
	r := reportOf(t, d)
	latest := r.Processes[0].Latest
	switch runtime.GOOS {
	case "linux", "darwin":
		if latest.OpenFDs == nil || *latest.OpenFDs <= 0 || latest.OpenFDsSupport != OpenFDsSampled {
			t.Fatalf("open_fds not sampled on %s: open_fds=%v support=%q", runtime.GOOS, latest.OpenFDs, latest.OpenFDsSupport)
		}
		if r.Budgets.OpenFDsSupport != OpenFDsSampled {
			t.Fatalf("budget support = %q, want %q", r.Budgets.OpenFDsSupport, OpenFDsSampled)
		}
		// The count tracks real descriptors, not a constant.
		before, _ := sampleOpenFDs()
		const extra = 64
		for i := 0; i < extra; i++ {
			f, err := os.Open(os.DevNull)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
		}
		after, _ := sampleOpenFDs()
		if before == nil || after == nil || *after-*before < extra {
			t.Fatalf("descriptor count did not follow %d opened files: before=%v after=%v", extra, before, after)
		}
	default:
		if latest.OpenFDs != nil || latest.OpenFDsSupport != OpenFDsUnsupported {
			t.Fatalf("open_fds on %s: open_fds=%v support=%q, want null and %q", runtime.GOOS, latest.OpenFDs, latest.OpenFDsSupport, OpenFDsUnsupported)
		}
	}
}

func TestDescriptorBudgetBreachWithInjectedCount(t *testing.T) {
	withOpenFDs(t, func() (*int, bool) { return ptr(DescriptorBudget + 88), true })
	d := t.TempDir()
	stop := Start(d, "tui", t.TempDir(), "test")
	if got, want := CurrentWarning(), "Health: descriptor count exceeds 512 budget"; got != want {
		stop()
		t.Fatalf("warning = %q, want %q", got, want)
	}
	stop()
	r := reportOf(t, d)
	p := r.Processes[0]
	if p.Latest.OpenFDs == nil || *p.Latest.OpenFDs != DescriptorBudget+88 {
		t.Fatalf("injected count not recorded: %v", p.Latest.OpenFDs)
	}
	found := false
	for _, flag := range p.Flags {
		found = found || flag == "descriptor count exceeds 512 budget"
	}
	if !found {
		t.Fatalf("budget breach not flagged: %v", p.Flags)
	}
	text := Format(r)
	if !strings.Contains(text, "descriptors <512") || strings.Contains(text, "unsupported") {
		t.Fatalf("sampled budget misreported:\n%s", text)
	}
}

func TestReportSaysUnsupportedWhenPlatformCannotCountFDs(t *testing.T) {
	withOpenFDs(t, func() (*int, bool) { return nil, false })
	d := t.TempDir()
	sampleOnce(t, d, "tui")
	sampleOnce(t, d, "web")
	r := reportOf(t, d)
	if r.Budgets.OpenFDsSupport != OpenFDsUnsupported {
		t.Fatalf("budget support = %q, want %q", r.Budgets.OpenFDsSupport, OpenFDsUnsupported)
	}
	for _, p := range r.Processes {
		if p.Latest.OpenFDs != nil || p.Latest.OpenFDsSupport != OpenFDsUnsupported {
			t.Fatalf("process %q: open_fds=%v support=%q", p.Latest.Role, p.Latest.OpenFDs, p.Latest.OpenFDsSupport)
		}
	}
	text := Format(r)
	for _, want := range []string{"descriptors unsupported on this platform", `"open_fds": unsupported on this platform`} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"descriptors <512", `"open_fds": unknown`} {
		if strings.Contains(text, unwanted) {
			t.Errorf("unexpected %q in:\n%s", unwanted, text)
		}
	}
	// Flagged once for the report, not once per process.
	if n := strings.Count(text, openFDsUnsupportedFlag); n != 1 {
		t.Errorf("unsupported flag printed %d times, want 1:\n%s", n, text)
	}
	// JSON stays compatible: open_fds is still present and null.
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"open_fds":null`) || !strings.Contains(string(data), `"open_fds_support":"unsupported"`) {
		t.Fatalf("json: %s", data)
	}
}

// A failed sample on a supported platform is unknown, not unsupported: the
// budget is still advertised because later samples can check it.
func TestReportFailedOpenFDsSampleStaysUnknown(t *testing.T) {
	withOpenFDs(t, func() (*int, bool) { return nil, true })
	d := t.TempDir()
	sampleOnce(t, d, "tui")
	r := reportOf(t, d)
	if r.Budgets.OpenFDsSupport != "" || r.Processes[0].Latest.OpenFDsSupport != "" {
		t.Fatalf("failed sample reported as %q / %q", r.Budgets.OpenFDsSupport, r.Processes[0].Latest.OpenFDsSupport)
	}
	text := Format(r)
	if !strings.Contains(text, "descriptors <512") || !strings.Contains(text, `"open_fds": unknown`) || strings.Contains(text, "unsupported") {
		t.Fatalf("failed sample misreported:\n%s", text)
	}
}
