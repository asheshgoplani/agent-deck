package main

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCapturePanePreservesLeadingSpaces(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "bin", "tmux")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '    ╭box╮\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	s := &suite{root: root, project: root, ctx: context.Background(), env: []string{"PATH=/usr/bin:/bin"}}
	got, err := s.capturePane("fixture")
	if err != nil {
		t.Fatal(err)
	}
	if got != "    ╭box╮" {
		t.Fatalf("capture = %q, want first-line indentation", got)
	}
}

func TestScrubRemotePollKeepsDividerFixed(t *testing.T) {
	a := "  ▾ remotes/lab (0) · unreachable: host down · poll 7ms    │ PREVIEW"
	b := "  ▾ remotes/lab (0) · unreachable: host down · poll 12ms   │ PREVIEW"
	if scrubFrame(a) != scrubFrame(b) {
		t.Fatalf("remote rows drift after scrub:\n%s\n%s", scrubFrame(a), scrubFrame(b))
	}
}

var regenerateGoldens = flag.Bool("visualcheck.regenerate", false, "regenerate visual check goldens")

func TestScrubFrameRedactsVolatileText(t *testing.T) {
	cases := []struct {
		name, in, wantContains string
	}{
		{"uuid", "session a1b2c3d4-e5f6-4789-a012-b3c4d5e6f789 attached", "<uuid>"},
		{"version", "Agent Deck v1.16.10", "<version>"},
		{"clock-time", "updated at 14:32:09", "<time>"},
		{"relative-age", "3m ago", "<age> ago"},
		{"socket-pid", "tmux -L vc-12345", "vc-<pid>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := scrubFrame(c.in)
			if !strings.Contains(got, c.wantContains) {
				t.Fatalf("scrubFrame(%q) = %q, want it to contain %q", c.in, got, c.wantContains)
			}
		})
	}
}

func TestScrubFrameIsIdempotent(t *testing.T) {
	in := "v1.2.3 session f47ac10b-58cc-4372-a567-0e02b2c3d479 at 09:15:00, 5m ago, socket vc-999"
	once := scrubFrame(in)
	twice := scrubFrame(once)
	if once != twice {
		t.Fatalf("scrubFrame is not idempotent:\nonce:  %q\ntwice: %q", once, twice)
	}
}

func TestListBodyLinesExcludesHeaderAndFooterBadges(t *testing.T) {
	// A minimal frame shaped like the real TUI's: a footer hotkey legend
	// paints its key badges with the exact same accent background as a
	// selected row (see cursorHighlightBG), and the header's filter tabs
	// do the same. Both must be excluded or cursor detection false-matches
	// on them (this pinned a real bug found during rehearsal).
	const badgeBG = "\x1b[48;2;121;162;247m"
	frame := strings.Join([]string{
		" Agent Deck",
		"  All " + badgeBG + " \x1b[49m filter",
		"SESSIONS                     ",
		"──────────────────────────── ",
		"  ▾ alpha (7)                ",
		"   ├─ ○ claude-idle claude   ",
		"────────────────────────────────────────────────────────────────────────────────",
		"⏎ " + badgeBG + "Toggle\x1b[49m n/N New",
	}, "\n")
	body := listBodyLines(frame)
	for _, line := range body {
		if strings.Contains(line, badgeBG) {
			t.Fatalf("listBodyLines leaked a header/footer badge line into the body: %q\nfull body: %#v", line, body)
		}
	}
	found := false
	for _, line := range body {
		if strings.Contains(stripANSI(line), "claude-idle") {
			found = true
		}
	}
	if !found {
		t.Fatalf("listBodyLines dropped a real row; body: %#v", body)
	}
}

func TestListBodyLinesKeepsRowBesidePreviewDivider(t *testing.T) {
	frame := strings.Join([]string{
		"SESSIONS                         │ PREVIEW",
		"──────────────────────────────── │ ────────────────────────────────────────",
		"  ▶└─ ■ claude-stopped claude      │ " + strings.Repeat("─", 80),
		"Session: Enter Attach",
	}, "\n")
	if _, found := cursorOnRow(frame, "claude-stopped"); !found {
		t.Fatalf("selected session row was discarded because preview has a divider:\n%s", frame)
	}
}

func TestGalleryRowOrderHasNoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, row := range galleryRowOrder {
		if seen[row] {
			t.Fatalf("galleryRowOrder has a duplicate: %q", row)
		}
		seen[row] = true
	}
}

// TestVisualCheckAgainstRealBinary is the actual visual check: it drives a
// real agent-deck binary through every screen in a private tmux server and
// diffs frames against the committed goldens. It needs Linux (the hook
// fixture and status classification paths are exercised the same way
// production runs them) and a real tmux, so it skips outright on macOS —
// this is the g14-test.sh / `make visual-check` path, never bare `go test`
// on a dev machine. VISUALCHECK_BINARY may name an already-built binary
// (set by `make visual-check`); otherwise this builds cmd/agent-deck itself,
// so a plain `go test ./tools/visualcheck` on the test box is enough.
func TestVisualCheckAgainstRealBinary(t *testing.T) {
	runVisualCheckTest(t)
}

func TestVisualCheckRegenerateGoldens(t *testing.T) {
	if !*regenerateGoldens {
		t.Skip("pass -args -visualcheck.regenerate explicitly")
	}
	t.Setenv("UPDATE_GOLDEN", "1")
	runVisualCheckTest(t)
}

func runVisualCheckTest(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("run on the g14 test box; see README.md")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Fatal("tmux required")
	}
	bin := os.Getenv("VISUALCHECK_BINARY")
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	if bin == "" {
		bin = filepath.Join(t.TempDir(), "agent-deck")
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/agent-deck")
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build cmd/agent-deck: %v\n%s", err, out)
		}
	}
	abs, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Chdir(dir)
	code := runMain([]string{abs})
	artifactDir := filepath.Join(repoRoot, "visualcheck-artifacts")
	if err := os.MkdirAll(artifactDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"contact-sheet.html", "visualcheck-report.json"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(artifactDir, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if code != 0 {
		t.Fatalf("visualcheck exited %d against %s; see contact-sheet.html in %s", code, abs, dir)
	}
}
