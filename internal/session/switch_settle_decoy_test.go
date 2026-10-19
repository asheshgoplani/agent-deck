package session

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Lander security re-review of PR 2778: a first-start screen's block left in
// earlier tool output (or inside another dialog's text) above a live menu
// that carries no permission marker. The answer must never reach the live
// menu: a screen is answered only when its block is the only menu on screen
// and the last thing on it. These are the lander's probes, as regressions.

const decoyThemeBlock = "Choose the text style that looks best with your terminal\n❯ 1. Dark mode\n  2. Light mode\n"

// The startup dialog for a project .mcp.json server: no permission marker.
func decoyMCPDialog(name string) string {
	return strings.Join([]string{
		"╭──────────────────────────────────────────────────────────────╮",
		"│ New MCP server found in .mcp.json: " + name,
		"│",
		"│ MCP servers may execute code or access system resources. All tool calls require approval.",
		"│",
		"│ ❯ 1. Use this and all future MCP servers in this project",
		"│   2. Use this MCP server",
		"│   3. Continue without using this MCP server",
		"╰──────────────────────────────────────────────────────────────╯",
		"   Enter to confirm · Esc to reject",
	}, "\n")
}

var decoyPanes = map[string]string{
	"theme block in tool output above the MCP dialog":    "⏺ Bash(cat notes.txt)\n  ⎿  hello\n     " + strings.ReplaceAll(decoyThemeBlock, "\n", "\n     ") + "\n" + decoyMCPDialog("foo"),
	"MCP server name carrying the theme block":           decoyMCPDialog("x\n" + decoyThemeBlock),
	"question carrying the theme block":                  "☐ Pick\n\n" + decoyThemeBlock + "\nWhich approach?\n\n❯ 1. Delete the prod database\n  2. Keep it\n\nEnter to select · ↑/↓ to navigate · Esc to cancel\n",
	"permission prompt with theme rows in its preview":   " Bash command\n\n   " + decoyThemeBlock + "\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n",
	"permission marker wrapped by a narrow pane":         " Bash command\n   cat x\n" + decoyThemeBlock + "\n Do you\n want to\n proceed?\n ❯ 1. Yes\n   2. No\n",
	"own-path trust block above the MCP dialog":          "Accessing workspace:\n\n" + settleProject + "\n\nQuick safety check: Is this a project you created or one you trust?\n\n❯ 1. Yes, I trust this folder\n  2. No, exit\n\n" + decoyMCPDialog("foo"),
	"theme picker followed by another menu":              "Choose the text style that looks best with your terminal\n\n❯ 1. Dark mode\n  2. Light mode\n\n" + decoyMCPDialog("foo"),
	"trust dialog followed by unrecognised text":         trustScreen(settleProject, 1) + "\n Use this and all future MCP servers in this project\n",
	"bypass block above another numbered menu":           "WARNING: Claude Code running in Bypass Permissions mode\n\n❯ 1. No, exit\n  2. Yes, I accept\n\nWhich one?\n  1. Delete it\n  2. Keep it\n",
	"live menu above a theme block that ends the screen": "Pick one\n❯ 1. Delete the prod database\n  2. Keep it\n\n" + "Choose the text style that looks best with your terminal\n\n❯ 1. Dark mode\n  2. Light mode\n",
	"live menu above a trust block that ends the screen": "Pick one\n  1. Delete the prod database\n  2. Keep it\n\n" + trustScreen(settleProject, 1),
	"wrapped permission marker over a clean theme block": "Do you\nwant to\nproceed?\n\nChoose the text style that looks best with your terminal\n\n❯ 1. Dark mode\n  2. Light mode\n",
	"security notes above a live menu":                   "Security notes:\n\n1. Claude can make mistakes\n\nPress Enter to continue…\n\nPick one\n  Use this MCP server\n",
}

func TestClassifySettlePane_DecoyAboveLiveMenuIsNeverAnswerable(t *testing.T) {
	for name, pane := range decoyPanes {
		t.Run(name, func(t *testing.T) {
			got := ClassifySettlePane(pane, SettleProjectPaths(settleProject))
			switch got.State {
			case SettlePaneTheme, SettlePaneBypass, SettlePaneOnboarding, SettlePaneTrustOwn, SettlePaneReady:
				t.Fatalf("state = %s (%s): a decoy over a live menu must stop", got.State, got.Detail)
			}
		})
	}
}

func TestSettleSwitch_DecoyAboveLiveMenuGetsNoKey(t *testing.T) {
	for name, pane := range decoyPanes {
		t.Run(name, func(t *testing.T) {
			cfgDir := t.TempDir()
			f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
			f.set(pane)
			f.onKey = func(f *fakeSettle, key string) {
				if key == "Enter" {
					f.set(readyPane)
				}
			}
			opts := settleOpts(cfgDir)
			opts.Bypass = true
			report, err := SettleSwitch(f.deps(), opts)
			if !errors.Is(err, ErrSettleStopped) || len(f.keys) != 0 || len(f.kicks) != 0 {
				t.Fatalf("err=%v keys=%v kicks=%d receipt=%s", err, f.keys, len(f.kicks), report.Receipt())
			}
		})
	}
}
