package ui

// The agent-deck mark in the TUI: three cells "●●○" (running green, waiting
// amber, idle hollow grey ring) in front of the product name, wherever the
// TUI shows the name as branding (header bar, loading/quit/restart splash,
// help footer, welcome box, quick check page). The colours are the brand
// kit's: one set for dark terminals and one for light ones, each with a
// fixed 256-colour and 16-colour fallback, picked by the active theme and
// the lipgloss colour profile. With no colour the mark is the plain glyphs.

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

const brandMarkPlain = "●●○"

// useColorProfileForTest sets the global lipgloss profile for one test.
func useColorProfileForTest(t *testing.T, p termenv.Profile) {
	t.Helper()
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(p)
	t.Cleanup(func() { lipgloss.SetColorProfile(old) })
}

// brandHeaderLine renders a full frame and returns its first row (the
// header bar), raw (with escapes).
func brandHeaderLine(t *testing.T, width int) string {
	t.Helper()
	h := newUpdateBannerTestHome(t, width)
	frame := h.View()
	line, _, _ := strings.Cut(frame, "\n")
	return line
}

func TestBrandMark_HeaderLeadsWithMark(t *testing.T) {
	for _, width := range []int{200, 120, 80, 60, 40} {
		t.Run(fmt.Sprintf("w%d", width), func(t *testing.T) {
			h := newUpdateBannerTestHome(t, width)
			frame := stripAnsi(h.View())
			lines := strings.Split(frame, "\n")
			if want := " " + brandMarkPlain + " Agent Deck"; !strings.HasPrefix(lines[0], want) {
				t.Fatalf("header row %q does not start with %q", lines[0], want)
			}
			for i, l := range lines {
				if w := lipgloss.Width(l); w > width {
					t.Fatalf("row %d is %d cells wide, terminal is %d: %q", i, w, width, l)
				}
			}
		})
	}
}

// sgrGlyph matches one SGR sequence directly followed by a mark glyph.
var sgrGlyph = regexp.MustCompile("\x1b\\[([0-9;]*)m([●○])")

func trueColorSGR(running, waiting, idle string) [3]string {
	var out [3]string
	for i, hex := range []string{running, waiting, idle} {
		out[i] = termenv.TrueColor.Color(hex).Sequence(false)
	}
	return out
}

// sgrAny matches any SGR sequence; its parameters say whether it sets a colour.
var sgrAny = regexp.MustCompile("\x1b\\[([0-9;]*)m")

func TestBrandMark_ColoursPerThemeAndProfile(t *testing.T) {
	cases := []struct {
		theme   string
		profile termenv.Profile
		name    string
		want    [3]string // SGR foreground parameter for running, waiting, idle
	}{
		// Truecolor: the kit's hex values, as termenv writes them (it
		// truncates a channel when converting, e.g. #E8B464 -> 232;179;100).
		{"dark", termenv.TrueColor, "truecolor", trueColorSGR("#5CC98F", "#E8B464", "#959DBD")},
		{"light", termenv.TrueColor, "truecolor", trueColorSGR("#485E30", "#855710", "#5E6170")},
		{"dark", termenv.ANSI256, "256", [3]string{"38;5;78", "38;5;179", "38;5;103"}},
		{"light", termenv.ANSI256, "256", [3]string{"38;5;64", "38;5;94", "38;5;60"}},
		{"dark", termenv.ANSI, "16", [3]string{"32", "33", "90"}},
		{"light", termenv.ANSI, "16", [3]string{"32", "33", "30"}},
	}
	for _, tc := range cases {
		t.Run(tc.theme+"-"+tc.name, func(t *testing.T) {
			useThemeForTest(t, tc.theme)
			useColorProfileForTest(t, tc.profile)
			line := brandHeaderLine(t, 120)
			head, _, ok := strings.Cut(line, "Agent Deck")
			if !ok {
				t.Fatalf("header has no title: %q", line)
			}
			if got := stripAnsi(head); got != " "+brandMarkPlain+" " {
				t.Fatalf("header before the title = %q, want %q", got, " "+brandMarkPlain+" ")
			}
			m := sgrGlyph.FindAllStringSubmatch(head, -1)
			if len(m) != 3 {
				t.Fatalf("want 3 coloured mark glyphs before the title, got %d in %q", len(m), head)
			}
			for i, glyph := range []string{"●", "●", "○"} {
				params, g := m[i][1], m[i][2]
				if g != glyph {
					t.Fatalf("glyph %d = %q, want %q", i, g, glyph)
				}
				if !strings.Contains(";"+params+";", ";"+tc.want[i]+";") {
					t.Fatalf("glyph %d %s: SGR %q lacks %q", i, glyph, params, tc.want[i])
				}
			}
		})
	}
}

func TestBrandMark_NoColourIsPlainGlyphs(t *testing.T) {
	for _, theme := range []string{"dark", "light"} {
		t.Run(theme, func(t *testing.T) {
			useThemeForTest(t, theme)
			useColorProfileForTest(t, termenv.Ascii)
			line := brandHeaderLine(t, 120)
			for _, m := range sgrAny.FindAllStringSubmatch(line, -1) {
				if m[1] != "" && m[1] != "0" {
					t.Fatalf("Ascii profile header sets a style (SGR %q): %q", m[1], line)
				}
			}
			if !strings.Contains(line, " "+brandMarkPlain+" Agent Deck") {
				t.Fatalf("Ascii header %q does not start with the plain mark", line)
			}
		})
	}
}

func TestBrandMark_Splashes(t *testing.T) {
	sizes := [][2]int{{80, 24}, {30, 8}, {20, 4}}
	for _, sz := range sizes {
		for frame := 0; frame < 8; frame++ {
			w, hgt := sz[0], sz[1]
			screens := map[string]string{
				"loading":    renderLoadingSplash(w, hgt, frame),
				"quitting":   renderQuittingSplash(w, hgt, frame),
				"restarting": renderShutdownSplash(w, hgt, frame, "Restarting..."),
			}
			for name, s := range screens {
				plain := stripAnsi(s)
				if !strings.Contains(plain, brandMarkPlain+" Agent Deck") {
					t.Fatalf("%s splash %dx%d frame %d lacks %q:\n%s", name, w, hgt, frame, brandMarkPlain+" Agent Deck", plain)
				}
				for i, l := range strings.Split(plain, "\n") {
					if lw := lipgloss.Width(l); lw > w {
						t.Fatalf("%s splash %dx%d row %d is %d wide", name, w, hgt, i, lw)
					}
				}
			}
		}
	}
}

func TestBrandMark_HelpFooter(t *testing.T) {
	for _, w := range []int{120, 60, 35} {
		overlay := NewHelpOverlay()
		overlay.SetSize(w, 400)
		overlay.ShowAll()
		view := stripAnsi(overlay.View())
		if want := brandMarkPlain + " Agent Deck v" + Version; !strings.Contains(view, want) {
			t.Fatalf("help at width %d lacks %q:\n%s", w, want, view)
		}
		for i, l := range strings.Split(view, "\n") {
			if lw := lipgloss.Width(l); lw > w {
				t.Fatalf("help at width %d: row %d is %d wide", w, i, lw)
			}
		}
	}
}

func TestBrandMark_WelcomeTitle(t *testing.T) {
	pinWelcomeForGolden(t)
	h := NewHome()
	if screen := stripAnsi(h.renderWelcome(120, 40)); !strings.Contains(screen, brandMarkPlain+" Welcome to Agent Deck") {
		t.Fatalf("welcome box lacks the mark:\n%s", screen)
	}
	// Too narrow for the mark: the title keeps its words, the mark gives way.
	if screen := stripAnsi(h.renderWelcome(30, 40)); !strings.Contains(screen, "Welcome to Agent Deck") {
		t.Fatalf("narrow welcome box lost its title:\n%s", screen)
	}
}

func TestBrandMark_QuickCheckTitle(t *testing.T) {
	newOnboardingEnv(t, "claude")
	o := NewOnboardingOverlay()
	o.show(onboardingPageCheck, true, "/work/billing-api", NewHome().onboardingKeys())
	o.SetSize(80, 24)
	if view := stripAnsi(o.View()); !strings.Contains(view, brandMarkPlain+" Agent Deck · quick check") {
		t.Fatalf("quick check page lacks the mark:\n%s", view)
	}
}
