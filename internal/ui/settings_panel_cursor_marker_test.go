package ui

// Issue #3039: the Settings panel marked the cursor row only with a
// ColorSurface background, about 1.17:1 against the dialog background in the
// dark theme (1.20:1 in light), so the focused row was close to invisible.
// The focused row now carries a "▶" gutter marker in ColorAccent in place of
// its two-space indent, which does not depend on background contrast.

import (
	"strconv"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

const settingsCursorMarker = "▶"

func hexContrastRatio(t *testing.T, a, b string) float64 {
	t.Helper()
	lum := func(hex string) float64 {
		h := strings.TrimPrefix(hex, "#")
		if len(h) != 6 {
			t.Fatalf("not a #rrggbb colour: %q", hex)
		}
		v, err := strconv.ParseUint(h, 16, 32)
		if err != nil {
			t.Fatalf("parse %q: %v", hex, err)
		}
		return relativeLuminance(int(v>>16&0xff), int(v>>8&0xff), int(v&0xff))
	}
	la, lb := lum(a), lum(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func TestSettingsPanel_CursorRowHasMarker_Issue3039(t *testing.T) {
	for _, theme := range []string{"dark", "light"} {
		t.Run(theme, func(t *testing.T) {
			useThemeForTest(t, theme)
			t.Setenv("HOME", t.TempDir())
			session.ClearUserConfigCache()
			t.Cleanup(session.ClearUserConfigCache)

			// The marker colour must stand out against the dialog background
			// (WCAG AA for text, 4.5:1) in both themes.
			if r := hexContrastRatio(t, string(ColorAccent), string(ColorBg)); r < 4.5 {
				t.Errorf("cursor marker contrast %s on %s = %.2f:1, want >= 4.5:1", ColorAccent, ColorBg, r)
			}

			panel := NewSettingsPanel()
			// Tall enough that no scroll-windowing hides rows.
			panel.SetSize(120, 300)
			panel.Show()

			// Rows named in the issue: radio rows, a checkbox row, the
			// inline "Lines to keep" field and an inline stats column.
			want := map[SettingType]string{
				SettingTheme:                "Dark",
				SettingDefaultTool:          "Claude",
				SettingDangerousMode:        "Dangerous mode",
				SettingClaudeDefaultAccount: "Default account",
				SettingLogMaxLines:          "Lines to keep",
				SettingStatsShowRAM:         "RAM",
				SettingPrivacy:              "Usage data",
			}

			for c := 0; c < settingsCount; c++ {
				panel.cursor = c
				view := stripAnsi(panel.View())
				if n := strings.Count(view, settingsCursorMarker); n != 1 {
					t.Errorf("cursor=%d: want exactly one %q marker in the view, got %d", c, settingsCursorMarker, n)
					continue
				}
				token, ok := want[SettingType(c)]
				if !ok {
					continue
				}
				for _, ln := range strings.Split(view, "\n") {
					if !strings.Contains(ln, settingsCursorMarker) {
						continue
					}
					after := ln[strings.Index(ln, settingsCursorMarker):]
					if !strings.Contains(after, token) {
						t.Errorf("cursor=%d: marker row should contain %q, got %q", c, token, ln)
					}
				}
			}
		})
	}
}
