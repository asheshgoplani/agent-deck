package tmux

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKimiExecutableDetection(t *testing.T) {
	if got := toolForExecutable("kimi"); got != "kimi" {
		t.Fatalf("toolForExecutable(kimi) = %q, want kimi", got)
	}
	if got := detectToolFromCommand("AGENTDECK_INSTANCE_ID=test kimi --model mock"); got != "kimi" {
		t.Fatalf("detectToolFromCommand = %q, want kimi", got)
	}
}

func TestKimiRealFrameClassification(t *testing.T) {
	for _, tt := range []struct {
		name string
		want FrameVerdict
	}{
		{name: "kimi-local-2.4.1-idle", want: FrameWaiting},
		{name: "kimi-local-2.4.1-busy-retry", want: FrameActive},
		{name: "kimi-local-2.4.1-busy-retry-cycle-13", want: FrameActive},
		{name: "kimi-local-2.4.1-busy-generation", want: FrameActive},
		{name: "kimi-local-2.4.1-busy-generation-cycle-12", want: FrameActive},
		{name: "kimi-local-2.4.1-busy-generation-cycle-13", want: FrameActive},
		{name: "kimi-local-2.4.1-busy-generation-cycle-14", want: FrameActive},
		{name: "kimi-local-2.4.1-interrupted", want: FrameWaiting},
		{name: "kimi-local-2.4.1-provider-failure", want: FrameError},
		{name: "kimi-local-2.4.1-folder-trust", want: FrameWaiting},
		{name: "kimi-local-2.4.1-tool-permission", want: FrameWaiting},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "status_corpus", tt.name+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			if got := ClassifyPaneFrame("kimi", string(data)); got != tt.want {
				t.Fatalf("ClassifyPaneFrame = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestKimiCurrentApprovalOutranksEarlierRetry(t *testing.T) {
	approval := "  ● Running a command · $ printf permission-approved\n" +
		"  ▶ Run this command?\n" +
		"  ▶ 1. Approve once\n" +
		"    2. Approve for this session\n" +
		"    3. Reject\n" +
		"  ↑/↓ select · 1/2/3 choose · ↵ confirm\n"
	retry := "  🌘 Retrying (3/10) · APIConnectionError · in 2s\n"
	for _, tc := range []struct {
		name    string
		content string
	}{
		{
			name: "later submitted turn with composer",
			content: " ❯ first\n" + retry +
				" ❯ permission please\n" + approval +
				" ╭────────────────────────────────╮\n" +
				" │ >                              │\n" +
				" ╰────────────────────────────────╯\n",
		},
		{
			name:    "no composer to delimit the current turn",
			content: " ❯ first\n" + retry + " ❯ permission please\n" + approval,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyPaneFrame("kimi", tc.content); got != FrameWaiting {
				t.Fatalf("current approval after historical retry = %q, want waiting", got)
			}
		})
	}
}

func TestKimiHistoricalApprovalDoesNotOutrankLaterRetry(t *testing.T) {
	content := " ❯ permission please\n" +
		"  ▶ Run this command?\n" +
		"  ▶ 1. Approve once\n" +
		"    2. Reject\n" +
		"  🌘 Retrying (3/10) · APIConnectionError · in 2s\n"
	if got := ClassifyPaneFrame("kimi", content); got != FrameActive {
		t.Fatalf("historical approval before current retry = %q, want active", got)
	}
}

func TestKimiHistoricalRetryDoesNotOutliveSubmittedTurn(t *testing.T) {
	content := " ❯ first\n" +
		"  🌘 Retrying (3/10) · APIConnectionError · in 2s\n" +
		"     Connection error.\n" +
		" ❯ second\n" +
		"   Interrupted by user\n" +
		" ╭────────────────────────────────╮\n" +
		" │ >                              │\n" +
		" ╰────────────────────────────────╯\n"
	if got := ClassifyPaneFrame("kimi", content); got != FrameWaiting {
		t.Fatalf("historical retry classified current turn as %q, want waiting", got)
	}
}

func TestKimiHistoricalGenerationTipDoesNotOutliveSubmittedTurn(t *testing.T) {
	content := " ❯ first\n" +
		"  Assistant transcript quoting a prior live footer:\n" +
		"  🌑 · Tip: /sessions to browse and resume earlier sessions\n" +
		" ❯ second\n" +
		"   Interrupted by user\n" +
		" ╭────────────────────────────────╮\n" +
		" │ >                              │\n" +
		" ╰────────────────────────────────╯\n"
	if got := ClassifyPaneFrame("kimi", content); got != FrameWaiting {
		t.Fatalf("historical generation tip classified current turn as %q, want waiting", got)
	}
}

func TestKimiHistoricalPermissionPanelDoesNotOutliveSubmittedTurn(t *testing.T) {
	content := " ❯ first\n" +
		"  Assistant transcript quoting a prior approval panel:\n" +
		"   ▶ Run this command?\n" +
		" ❯ second\n" +
		"   Interrupted by user\n" +
		" ╭────────────────────────────────╮\n" +
		" │ >                              │\n" +
		" ╰────────────────────────────────╯\n"
	if got := ClassifyPaneFrame("kimi", content); got != FrameWaiting {
		t.Fatalf("historical permission panel classified current turn as %q, want waiting", got)
	}
}

func TestKimiHistoricalErrorDoesNotOutliveSubmittedTurn(t *testing.T) {
	content := " ❯ first\n" +
		"   Error: [provider.connection_error] Connection error.\n" +
		" ❯ second\n" +
		"   Interrupted by user\n" +
		" ╭────────────────────────────────╮\n" +
		" │ >                              │\n" +
		" ╰────────────────────────────────╯\n"
	if got := ClassifyPaneFrame("kimi", content); got != FrameWaiting {
		t.Fatalf("historical error classified current turn as %q, want waiting", got)
	}
}

func TestKimiErrorBannerRequiresRenderedShape(t *testing.T) {
	content := " ❯ explain failures\n" +
		"   The prose mentions Error: [provider.connection_error] without rendering a banner.\n" +
		" ╭────────────────────────────────╮\n" +
		" │ >                              │\n" +
		" ╰────────────────────────────────╯\n"
	if NewPromptDetector("kimi").HasErrorBanner(content) {
		t.Fatal("prose mentioning an error was classified as a Kimi error banner")
	}
}
