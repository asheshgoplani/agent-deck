package tmux

import (
	"strings"
	"testing"
)

// Muse Code CLI (`muse`). tmux-layer detection + pattern defaults.
// All content fixtures are verbatim tmux capture-pane lines from Muse Code
// 1.0.2 running in a pane (echo provider).

// Captured busy line: reply delayed 8s via --echo-delay-ms.
const museCapturedBusyLine = "◈ Thinking (2s · esc to interrupt)"

// Captured idle screen lines.
const museCapturedBanner = "Muse Code 1.0.2"

const museCapturedIdlePrompt = "⟩ Type @ to search and insert workspace file paths"
const museCapturedStatusBar = "echo · /private/tmp/muse-probe-proj"

func TestDetectToolFromCommand_Muse(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{"bare muse", "muse", "muse"},
		{"muse with trust flag", "muse --trust-workspace", "muse"},
		{"muse with yolo flag", "muse --yolo", "muse"},
		{"muse absolute path", "/Users/x/.local/bin/muse", "muse"},
		{"muse resume subcommand", "muse resume 01a063dd-45dd-7402-b516-4346e923db3e", "muse"},
		{"uppercase binary", "MUSE", "muse"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectToolFromCommand(tt.command); got != tt.want {
				t.Fatalf("detectToolFromCommand(%q) = %q, want %q", tt.command, got, tt.want)
			}
		})
	}
}

func TestDetectToolFromCommand_Muse_Negative(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		// Token-position matching (not substring): English words and paths
		// containing "muse" must not claim the pane.
		{"empty", ""},
		{"unrelated tool", "ls -la"},
		{"echo muse", "echo muse"},
		{"amuse", "amuse"},
		{"museum", "echo museum"},
		{"muse in path", "git -C /tmp/muse-project status"},
		{"wrapper trailing bare muse fails closed", "my-wrapper muse"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectToolFromCommand(tt.command)
			if got == "muse" {
				t.Fatalf("detectToolFromCommand(%q) = %q, should NOT match muse", tt.command, got)
			}
		})
	}
}

func TestDetectToolFromCommand_Muse_Wrapper(t *testing.T) {
	// Wrapper with trailing flags still resolves via the token arm.
	if got := detectToolFromCommand("my-wrapper muse --trust-workspace"); got != "muse" {
		t.Fatalf("detectToolFromCommand(wrapper) = %q, want muse", got)
	}
}

func TestDetectToolFromContent_Muse(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"muse code banner", "  Muse Code 1.0.2\n  Log in with browser", "muse"},
		{"thinking marker", museCapturedBusyLine, "muse"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectToolFromContent(tt.content); got != tt.want {
				t.Fatalf("detectToolFromContent(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestDetectToolFromContent_Muse_Negative(t *testing.T) {
	// Content patterns anchor on the product banner and busy marker, so
	// prose containing "muse" must not claim the pane.
	for _, content := range []string{
		"planning a museum visit",
		"this will amuse the team",
	} {
		if got := detectToolFromContent(content); got == "muse" {
			t.Errorf("detectToolFromContent(%q) = %q, should NOT match muse", content, got)
		}
	}
}

func TestDefaultRawPatterns_Muse(t *testing.T) {
	raw := DefaultRawPatterns("muse")
	if raw == nil {
		t.Fatal("expected non-nil RawPatterns for muse")
	}
	if len(raw.BusyPatterns) == 0 {
		t.Error("muse should have busy patterns")
	}
	if len(raw.PromptPatterns) == 0 {
		t.Error("muse should have prompt patterns")
	}
}

// TestMuseBusyPattern_MatchesCapturedLine pins the captured busy line
// against the compiled busy strings using the same case-insensitive
// substring semantics as hasBusyIndicatorResolved.
func TestMuseBusyPattern_MatchesCapturedLine(t *testing.T) {
	raw := DefaultRawPatterns("muse")
	if raw == nil {
		t.Fatal("expected non-nil RawPatterns for muse")
	}
	resolved, err := CompilePatterns(raw)
	if err != nil {
		t.Fatalf("CompilePatterns: %v", err)
	}
	lowerLine := strings.ToLower(museCapturedBusyLine)
	matched := false
	for _, str := range resolved.BusyStrings {
		if strings.Contains(lowerLine, strings.ToLower(str)) {
			matched = true
			break
		}
	}
	for _, re := range resolved.BusyRegexps {
		if re.MatchString(museCapturedBusyLine) {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("captured busy line %q matched no muse busy pattern", museCapturedBusyLine)
	}
}

// TestMusePromptPattern_MatchesCapturedIdle pins the captured idle
// placeholder against the prompt patterns.
func TestMusePromptPattern_MatchesCapturedIdle(t *testing.T) {
	raw := DefaultRawPatterns("muse")
	if raw == nil {
		t.Fatal("expected non-nil RawPatterns for muse")
	}
	found := false
	for _, p := range raw.PromptPatterns {
		if strings.Contains(museCapturedIdlePrompt, strings.TrimPrefix(p, "re:")) && !strings.HasPrefix(p, "re:") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("captured idle prompt %q matched no muse prompt pattern", museCapturedIdlePrompt)
	}
}

// TestMuseFixtures_Documented pins the remaining captured fixtures so a
// future edit cannot silently drop the evidence they carry.
func TestMuseFixtures_Documented(t *testing.T) {
	if museCapturedBanner == "" || museCapturedStatusBar == "" {
		t.Error("captured fixtures must be non-empty")
	}
}
