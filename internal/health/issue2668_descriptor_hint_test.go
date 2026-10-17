package health

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// An over-budget descriptor flag comes with guidance on what it means and
// how to find what holds the descriptors; an in-budget process gets none
// (#2668).
func TestIssue2668_FormatExplainsDescriptorBudgetBreach(t *testing.T) {
	d := t.TempDir()
	now := time.Now().UTC()
	write := func(pid, fds int) {
		line := `{"version":1,"timestamp":"` + now.Add(-time.Minute).Format(time.RFC3339Nano) + `","role":"tui","pid":` + strconv.Itoa(pid) +
			`,"started_at":"` + now.Add(-time.Hour).Format(time.RFC3339Nano) + `","open_fds":` + strconv.Itoa(fds) +
			`,"session_count":12,"open_fds_limit":10240}` + "\n"
		name := filepath.Join(d, "tui-"+strconv.Itoa(pid)+".jsonl")
		if err := os.WriteFile(name, []byte(line), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(60507, 720)
	write(4242, 100)
	text := Format(reportOf(t, d))
	for _, want := range []string{
		"descriptor count exceeds budget (720 open, budget 704)",
		"hint: the budget is 512 + 16 per session, capped at 80% of the soft open-file limit (ulimit -n = 10240)",
		"advisory",
		"lsof -p 60507 | awk 'NR>1 {print $5}' | sort | uniq -c | sort -rn",
		"docs/runtime-health.md#descriptor-budget",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Count(text, "hint:") != 1 || strings.Contains(text, "lsof -p 4242") {
		t.Errorf("in-budget process got a descriptor hint:\n%s", text)
	}
}
