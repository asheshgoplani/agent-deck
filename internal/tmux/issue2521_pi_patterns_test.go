package tmux

import (
	"strings"
	"testing"
	"time"
)

const pi2521Footer = "~/tmp\n0.0%/1.0M (auto)                  (provider) vendor/model-id • max\n"
const pi2521Busy = "escape interrupt · ctrl+c/ctrl+d clear/exit\n\n────────────────\n\n────────────────\n" + pi2521Footer

func TestIssue2521PiPrompt(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		want          bool
	}{
		{"reported footer", pi2521Footer, true},
		{"integer percent", "42%/200k (auto) (provider) model", true},
		{"decimal percent", "  12.5%/1.0M (auto) (provider) model", true},
		{"no suffix", "0%/32768 (auto)", true},
		{"legacy glyph", "pi> ", true},
		{"legacy tokens", "↑5.9k ↓77 R5.5k CH96.6% $0.01", true},
		{"legacy no cache", "↑2.2k ↓198 $0.017", true},
		{"prose", "The example footer is 0.0%/1.0M (auto)", false},
		{"missing window", "0.0% (auto)", false},
		{"missing auto", "0.0%/1.0M downloaded", false},
		{"split line", "0.0%/1.0M\n(auto)", false},
		{"auto suffix", "0.0%/1.0M (auto)matic", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Session{Command: "pi --no-skills"}
			if got := s.hasPromptIndicator(tc.content); got != tc.want {
				t.Fatalf("prompt=%v, want %v for %q", got, tc.want, tc.content)
			}
		})
	}
}

func TestIssue2521PiBusy(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		want          bool
	}{
		{"top bar above composer", pi2521Busy, true},
		{"standalone interrupt", "escape interrupt", true},
		{"legacy esc", "esc to interrupt", true},
		{"legacy ctrl c", "ctrl+c to interrupt", true},
		{"legacy spinner", "⠴ Working\n" + pi2521Footer, true},
		{"idle", pi2521Footer, false},
		{"prose", "Use escape interrupt to stop a running turn.\n" + pi2521Footer, false},
		{"quoted shortcut", "The shortcut is escape interrupt · ctrl+c/ctrl+d clear/exit\n" + pi2521Footer, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Session{Command: "pi --no-skills"}
			if got := s.hasBusyIndicator(tc.content); got != tc.want {
				t.Fatalf("busy=%v, want %v for %q", got, tc.want, tc.content)
			}
		})
	}
}

func TestIssue2521PiOverduePane(t *testing.T) {
	for _, tc := range []struct{ name, content, want string }{
		{"idle", pi2521Footer, "waiting"},
		{"busy", pi2521Busy, "active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := startPaneWithContent(t, "issue2521-"+tc.name, tc.content, "pi --no-skills", "vendor/model-id")
			oldPID, _ := s.getPaneProcessTree()
			s.mu.Lock()
			s.startupAt = time.Now().Add(-startupStateWindow - time.Second)
			s.lastStableStatus = "starting"
			s.mu.Unlock()
			got, err := s.GetStatus()
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("status=%q, want %q", got, tc.want)
			}
			newPID, _ := s.getPaneProcessTree()
			if oldPID == 0 || newPID != oldPID {
				t.Fatalf("pane replaced: %d -> %d", oldPID, newPID)
			}
			if !s.startupAt.IsZero() {
				t.Fatal("startup watchdog still armed")
			}
		})
	}
}

func BenchmarkIssue2521PiPrompt(b *testing.B) {
	s := &Session{Command: "pi --no-skills"}
	for b.Loop() {
		s.hasPromptIndicator(pi2521Footer)
	}
}

func TestIssue2521PiQuotedBusy(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		want          FrameVerdict
	}{
		{"closed backticks", "Here is the shortcut:\n```text\nescape interrupt\n```\nDone.\n" + pi2521Footer, FrameWaiting},
		{"closed full bar", "```text\nescape interrupt · ctrl+c/ctrl+d clear/exit\n```\n" + pi2521Footer, FrameWaiting},
		{"indented tilde", "  ~~~text\n  escape interrupt\n  ~~~\n" + pi2521Footer, FrameWaiting},
		{"long fence", "````text\nescape interrupt\n```\n````\n" + pi2521Footer, FrameWaiting},
		{"unclosed fence", "```text\n" + pi2521Busy, FrameActive},
		{"different closing fence", "```text\nescape interrupt\n~~~\n" + pi2521Footer, FrameActive},
		{"live after example", "```text\nescape interrupt\n```\n" + pi2521Busy, FrameActive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyPaneFrame("pi", tc.content); got != tc.want {
				t.Fatalf("frame=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestIssue2521PiBusyWindowAfterFence(t *testing.T) {
	content := "escape interrupt\n" + strings.Repeat("old answer\n", 23) + "```text\nexample\n```\n\n"
	s := &Session{Command: "pi"}
	if s.hasBusyIndicator(content) {
		t.Fatal("closed trailing fence pulled stale busy text into the 25-line window")
	}
}
