package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestEmbeddedHyperlinksPreserveURLAndParameters(t *testing.T) {
	for _, params := range []string{"", "id=demo:tag=report"} {
		t.Run(params, func(t *testing.T) {
			const target = "https://example.invalid/report?line=2"
			size := embeddedTerminalSize{Cols: 80, Rows: 4}
			open := ansi.SetHyperlink(target, params)
			content := open + "report" + ansi.SetHyperlink("")
			emu := newEmbeddedTerminalEmulator(size, nil)
			defer emu.Close()
			// Split the input inside OSC 8 so a workaround that only scans a
			// complete incoming chunk cannot satisfy this contract.
			for _, chunk := range []string{content[:7], content[7:]} {
				if _, err := emu.WriteString(chunk); err != nil {
					t.Fatal(err)
				}
			}
			cell := emu.CellAt(0, 0)
			if cell == nil || cell.Link.URL != target || cell.Link.Params != params {
				t.Fatalf("wrong hyperlink metadata: %+v", cell)
			}
			terminal := &embeddedTerminal{emulator: emu}
			snapshot, _ := renderTerminalSnapshot(content, size, 0)
			for name, rendered := range map[string]string{"live": terminal.Render(), "snapshot": snapshot} {
				if !strings.Contains(rendered, open) {
					t.Errorf("%s lost or reversed hyperlink: %q", name, rendered)
				}
				if !strings.Contains(ansi.Strip(rendered), "report") {
					t.Errorf("%s lost label", name)
				}
			}
		})
	}
}
