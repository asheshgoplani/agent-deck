package ui

// Issue #2449: with theme = "light", agent-deck painted its own fixed grey
// surface over every captured background in the preview (so a tool already
// drawing light backgrounds, e.g. OpenCode on Solarized Light, turned into
// grey stripes), and the New Session dialog box let the terminal background
// show through inside its text inputs and between inline segments.
//
// Regenerate the dark-theme dialog golden with:
//
//	UPDATE_GOLDEN=1 go test ./internal/ui/ -run 'TestNewDialog_DarkThemeViewGolden_Issue2449'
//
// and review the diff like any other test change. It pins the dark theme as
// byte-identical: the #2449 fix is light-theme only.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

const (
	solarizedBase3BG = "\x1b[48;2;253;246;227m" // #fdf6e3, Solarized Light background
	solarizedBase2BG = "\x1b[48;2;238;232;213m" // #eee8d5, Solarized Light highlight
	codexDarkBarBG   = "\x1b[48;2;40;42;54m"    // dark band a dark-detected tool paints (#322)
)

func useThemeForTest(t *testing.T, theme string) {
	t.Helper()
	InitTheme(theme)
	t.Cleanup(func() { InitTheme("dark") })
}

func TestRemapANSIBackground_KeepsLightRemapsDark_Issue2449(t *testing.T) {
	useThemeForTest(t, "light")
	surface := previewSurfaceANSI()
	if surface == "" {
		t.Fatal("light surface has no ANSI form")
	}

	cases := []struct {
		name string
		seq  string
		dark bool
	}{
		{"truecolor solarized base3", solarizedBase3BG, false},
		{"truecolor solarized base2", solarizedBase2BG, false},
		{"truecolor white", "\x1b[48;2;255;255;255m", false},
		{"truecolor codex dark bar", codexDarkBarBG, true},
		{"truecolor black", "\x1b[48;2;0;0;0m", true},
		{"truecolor mid grey", "\x1b[48;2;128;128;128m", true},
		{"256 cube light cream", "\x1b[48;5;230m", false},
		{"256 grey ramp light", "\x1b[48;5;255m", false},
		{"256 grey ramp dark", "\x1b[48;5;236m", true},
		{"256 cube dark green", "\x1b[48;5;22m", true},
		{"256 basic black", "\x1b[48;5;0m", true},
		{"256 basic white", "\x1b[48;5;7m", false},
		{"256 bright white", "\x1b[48;5;15m", false},
		{"basic black", "\x1b[40m", true},
		{"basic blue", "\x1b[44m", true},
		{"basic white", "\x1b[47m", false},
		{"bright black", "\x1b[100m", true},
		{"bright red", "\x1b[101m", false},
		{"bright white", "\x1b[107m", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := "\x1b[38;2;101;123;131m" + tc.seq + " text \x1b[0m"
			got := remapANSIBackground(in, surface)
			want := in
			if tc.dark {
				want = "\x1b[38;2;101;123;131m" + surface + " text \x1b[0m"
			}
			if got != want {
				t.Fatalf("remap(%q)\n got  %q\n want %q", tc.seq, got, want)
			}
		})
	}
}

// renderPreviewWithCapture renders the classic preview pane for one running
// session whose captured pane content is capture.
func renderPreviewWithCapture(t *testing.T, capture string) string {
	t.Helper()
	inst := session.NewInstance("preview-2449", t.TempDir())
	inst.Status = session.StatusRunning
	inst.Tool = "opencode"

	h := homeWithSession(inst)
	h.previewCacheMu.Lock()
	h.previewCache[inst.ID] = capture
	h.previewCacheTime[inst.ID] = time.Now()
	h.previewCacheMu.Unlock()
	return h.renderPreviewPane(120, 30)
}

const opencodeSolarizedCapture = solarizedBase3BG + "  opencode prompt area  \x1b[0m\n" +
	solarizedBase2BG + "  highlighted input block  \x1b[0m\n" +
	codexDarkBarBG + "  dark band from a dark-detected tool  \x1b[0m\n"

func TestPreviewPane_LightThemeKeepsToolLightBackgrounds_Issue2449(t *testing.T) {
	useThemeForTest(t, "light")
	rendered := renderPreviewWithCapture(t, opencodeSolarizedCapture)

	for _, keep := range []string{solarizedBase3BG, solarizedBase2BG} {
		if !strings.Contains(rendered, keep) {
			t.Errorf("light theme preview replaced the tool's light background %q\nrendered=%q", keep, rendered)
		}
	}
	// #322/#334: dark bands are still remapped onto the light surface.
	if strings.Contains(rendered, codexDarkBarBG) {
		t.Errorf("light theme preview let a dark background band through\nrendered=%q", rendered)
	}
	if !strings.Contains(rendered, previewSurfaceANSI()+"  dark band") {
		t.Errorf("dark band was not remapped to the surface %q\nrendered=%q", previewSurfaceANSI(), rendered)
	}
}

func TestPreviewPane_DarkThemePassesBackgroundsThrough_Issue2449(t *testing.T) {
	useThemeForTest(t, "dark")
	rendered := renderPreviewWithCapture(t, opencodeSolarizedCapture)

	for _, keep := range []string{solarizedBase3BG, solarizedBase2BG, codexDarkBarBG} {
		if !strings.Contains(rendered, keep) {
			t.Errorf("dark theme preview altered captured background %q\nrendered=%q", keep, rendered)
		}
	}
}

// backgroundHoles walks one rendered row and returns, for every printable
// cell strictly between the first and last "│" (the dialog's side borders),
// whether an SGR background was active when it was drawn. It reports the
// plain text of each run of cells drawn on the terminal's own background.
func backgroundHoles(row string) []string {
	type cell struct {
		r  rune
		bg bool
	}
	var cells []cell
	bg := false
	for i := 0; i < len(row); {
		if row[i] == 0x1b && i+1 < len(row) && row[i+1] == '[' {
			j := i + 2
			for j < len(row) && (row[j] < 0x40 || row[j] > 0x7e) {
				j++
			}
			if j >= len(row) {
				break
			}
			if row[j] == 'm' {
				params := strings.Split(row[i+2:j], ";")
				for k := 0; k < len(params); k++ {
					p := params[k]
					n, _ := strconv.Atoi(p)
					switch {
					case p == "" || p == "0" || p == "49":
						bg = false
					case (n >= 40 && n <= 47) || (n >= 100 && n <= 107):
						bg = true
					case p == "48" || p == "38" || p == "58":
						if p == "48" {
							bg = true
						}
						if k+1 < len(params) && params[k+1] == "5" {
							k += 2
						} else if k+1 < len(params) && params[k+1] == "2" {
							k += 4
						}
					}
				}
			}
			i = j + 1
			continue
		}
		r, size := rune(row[i]), 1
		if row[i] >= 0x80 {
			rs := []rune(row[i:])
			r = rs[0]
			size = len(string(r))
		}
		cells = append(cells, cell{r: r, bg: bg})
		i += size
	}

	first, last := -1, -1
	for idx, c := range cells {
		if c.r == '│' {
			if first < 0 {
				first = idx
			}
			last = idx
		}
	}
	if first < 0 || last <= first {
		return nil
	}
	var holes []string
	var run strings.Builder
	for _, c := range cells[first+1 : last] {
		if !c.bg {
			run.WriteRune(c.r)
			continue
		}
		if run.Len() > 0 {
			holes = append(holes, run.String())
			run.Reset()
		}
	}
	if run.Len() > 0 {
		holes = append(holes, run.String())
	}
	return holes
}

func openNewDialogForTest(t *testing.T, width, height int) *NewDialog {
	t.Helper()
	d := NewNewDialog()
	d.SetSize(width, height)
	d.ShowInGroup("default", "default", "/work/project", nil, "")
	return d
}

func TestNewDialog_LightThemeSurfaceFillIsUniform_Issue2449(t *testing.T) {
	forceTrueColorProfile()
	useThemeForTest(t, "light")

	for _, size := range []struct{ w, h int }{{120, 80}, {100, 30}} {
		t.Run(strconv.Itoa(size.w)+"x"+strconv.Itoa(size.h), func(t *testing.T) {
			d := openNewDialogForTest(t, size.w, size.h)
			view := d.View()

			sawName, sawPath := false, false
			for i, row := range strings.Split(view, "\n") {
				plain := stripAnsi(row)
				if strings.Contains(plain, "> session-name") {
					sawName = true
				}
				if strings.Contains(plain, "> /work/project") {
					sawPath = true
				}
				if holes := backgroundHoles(row); len(holes) > 0 {
					t.Errorf("row %d shows the terminal background inside the dialog at %q\nplain=%q\nraw=%q", i, holes, plain, row)
				}
			}
			if !sawName || !sawPath {
				t.Fatalf("name/path inputs not rendered (name=%v path=%v)\n%s", sawName, sawPath, stripAnsi(view))
			}
		})
	}
}

func TestNewDialog_DarkThemeViewGolden_Issue2449(t *testing.T) {
	forceTrueColorProfile()
	useThemeForTest(t, "dark")

	d := openNewDialogForTest(t, 120, 60)
	frame := d.View()

	path := filepath.Join("testdata", "issue2449_newdialog_dark_120x60.golden")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, []byte(frame), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != frame {
		t.Fatalf("dark theme New Session dialog is no longer byte-identical\nwant=%q\ngot =%q", want, frame)
	}
}
