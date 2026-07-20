package tmux

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Fixtures in testdata/omp/ are real pane captures of omp v17.0.1 (oh-my-pi,
// 200x50 pane), with MCP server names scrubbed:
//
//   idle.txt            fresh start, welcome box, prompt rendered, no task yet
//   busy.txt            two snapshots while thinking/streaming ("⠧ Working… ⟦esc⟧")
//   permission.txt      a bash tool call executing. omp AUTO-APPROVES tool calls
//                       by default, so there is no separate waiting-for-approval
//                       state: this pane is BUSY ("⠼ Running echo probe ⟦esc⟧")
//                       with the finished call's output box above it.
//   idle-after-task.txt back at the prompt after a completed task. The border
//                       task label ("◀ <task> ──") and tool-output timing lines
//                       ("⟦Wall: 0.05s | Timeout: 300s⟧") PERSIST here — only
//                       the spinner + "⟦esc⟧" interrupt hint disappear.
//
// Like Claude Code's and codewhale's TUIs, omp keeps its input box visible in
// every state, so PromptPatterns intentionally match busy panes too. Busy is
// authoritative in the detector (checked before prompt), which is what makes
// that safe — see the codewhale comment in patterns.go (#1577).

func ompFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/omp/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func ompAnyPatternMatches(t *testing.T, patterns []string, content string) bool {
	t.Helper()
	for _, p := range patterns {
		if rest, ok := strings.CutPrefix(p, "re:"); ok {
			re, err := regexp.Compile(rest)
			if err != nil {
				t.Fatalf("bad regex %q: %v", p, err)
			}
			if re.MatchString(content) {
				return true
			}
		} else if strings.Contains(content, p) {
			return true
		}
	}
	return false
}

func TestOmpDefaultPatternsExist(t *testing.T) {
	if DefaultRawPatterns("omp") == nil {
		t.Fatal("DefaultRawPatterns(\"omp\") returned nil — omp has no built-in patterns")
	}
}

func TestOmpBusyDetection(t *testing.T) {
	rp := DefaultRawPatterns("omp")

	// True positives: thinking/streaming AND tool-execution phases. The busy
	// verb is model-generated per task ("Working…", "Running echo probe", …),
	// so only the trailing "⟦esc⟧" interrupt hint is stable across both.
	for _, name := range []string{"busy.txt", "permission.txt"} {
		if !ompAnyPatternMatches(t, rp.BusyPatterns, ompFixture(t, name)) {
			t.Errorf("no BusyPattern matched %s", name)
		}
	}

	// False-positive candidates: idle-after-task.txt still shows the border
	// task label and "⟦Wall: …⟧" timing lines from finished tool calls — a
	// bare "⟦" (or the label) as a busy pattern would misread it as running,
	// leaving the conductor waiting forever.
	for _, name := range []string{"idle.txt", "idle-after-task.txt"} {
		if ompAnyPatternMatches(t, rp.BusyPatterns, ompFixture(t, name)) {
			t.Errorf("a BusyPattern false-positived on %s", name)
		}
	}
}

func TestOmpPromptDetection(t *testing.T) {
	rp := DefaultRawPatterns("omp")

	for _, name := range []string{"idle.txt", "idle-after-task.txt"} {
		if !ompAnyPatternMatches(t, rp.PromptPatterns, ompFixture(t, name)) {
			t.Errorf("no PromptPattern matched %s", name)
		}
	}
	// No "prompt must not match busy" assertion: omp's input box is visible
	// while busy (structural, like codewhale). Busy precedence resolves it.
}
