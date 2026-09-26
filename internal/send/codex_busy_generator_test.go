package send

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

var interruptHint = regexp.MustCompile(`(?:esc|ctrl ?\+ ?c) to interrupt`)

// The generated cuts start after the complete interrupt hint. Before that
// point the rendered row has no observable proof that a turn is running.
func TestCodexBusyFrameVariantsGate(t *testing.T) {
	dir := filepath.Join("..", "tmux", "testdata", "status_corpus")
	labels, err := os.Open(filepath.Join(dir, "labels.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer labels.Close()

	frames := map[string]string{}
	sc := bufio.NewScanner(labels)
	for sc.Scan() {
		fields := strings.SplitN(sc.Text(), "\t", 4)
		if len(fields) < 3 || fields[1] != "codex" || fields[2] != "active" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, fields[0]+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		frames[fields[0]] = string(data)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(frames) < 20 {
		t.Fatalf("expected all busy Codex corpus frames, got %d", len(frames))
	}
	variants := 0
	for name, frame := range frames {
		lines := strings.Split(frame, "\n")
		status := -1
		for i, line := range lines {
			if interruptHint.MatchString(line) {
				status = i
			}
		}
		if status < 0 {
			t.Errorf("%s: no live interrupt hint", name)
			continue
		}
		check := func(label string, ls []string) {
			t.Helper()
			variants++
			pane := strings.Join(ls, "\n")
			if tmux.NewPromptDetector("codex").HasPrompt(tmux.StripANSI(pane)) ||
				paneShowsReadyPrompt(&mockReadyChecker{pane: pane}, "codex", PromptGates{}) ||
				paneShowsReadyPrompt(&mockReadyChecker{pane: pane}, "codex", PromptGates{CodexPrompt: true}) {
				t.Errorf("%s/%s: busy frame reads ready\n%s", name, label, pane)
			}
		}
		check("original", lines)
		row := lines[status]
		hintEnd := interruptHint.FindStringIndex(row)[1]
		// Rebuild the optional suffix before cutting. Some corpus captures are
		// already truncated, and appending another ellipsis would be a frame
		// Codex never renders.
		fullRow := row[:hintEnd] + ") · 1 background terminal running · /ps to view · /stop to close"
		for cut := hintEnd; cut <= len(fullRow); {
			if cut == len(fullRow) || utf8.RuneStart(fullRow[cut]) {
				v := append([]string(nil), lines...)
				v[status] = fullRow[:cut] + "…"
				check(fmt.Sprintf("cut-%d", cut), v)
			}
			cut++
		}
		for _, suffix := range []string{"", " · 1 background terminal running · /ps to view", "   "} {
			v := append([]string(nil), lines...)
			v[status] = row[:hintEnd] + ")" + suffix
			check("suffix-"+suffix, v)
		}
		v := append([]string(nil), lines...)
		v = append(v[:status+1], append([]string{"  └ detail", "", "    resumed detail"}, v[status+1:]...)...)
		check("blank-detail", v)
		for _, width := range []int{60, 80, 100} {
			v := append([]string(nil), lines...)
			v = append(v[:status+1], append([]string{"  └ " + strings.Repeat("x", width-5), "    wrapped detail"}, v[status+1:]...)...)
			check(fmt.Sprintf("wrap-%d", width), v)
		}
		v = append([]string(nil), lines...)
		v = append(v[:status+1], append([]string{"• Queued follow-up inputs", "  ↳ next", "    shift + ← edit last queued message"}, v[status+1:]...)...)
		check("queued", v)
		for i := status + 1; i < len(lines); i++ {
			if lines[i] != "• Queued follow-up inputs" {
				continue
			}
			end := i + 1
			for end < len(lines) && strings.TrimSpace(lines[end]) != "" {
				end++
			}
			v = append(append([]string(nil), lines[:i]...), lines[end:]...)
			check("without-queued", v)
			break
		}
		inDetails := false
		for i := status + 1; i < len(lines); i++ {
			if strings.HasPrefix(lines[i], "  └ ") {
				inDetails = true
			}
			if strings.HasPrefix(lines[i], "• Queued") || strings.HasPrefix(lines[i], "› ") {
				break
			}
			if !inDetails || !strings.HasPrefix(lines[i], "    ") {
				continue
			}
			v = append(append([]string(nil), lines[:i]...), append([]string{""}, lines[i:]...)...)
			check(fmt.Sprintf("blank-detail-%d", i), v)
		}
		v = append([]string(nil), lines...)
		v = append(v[:status], append([]string{"• Compacting context completed"}, v[status:]...)...)
		check("compaction-above", v)
	}
	t.Logf("busy bases=%d generated variants=%d", len(frames), variants)
}

func TestCodexQuotedProseVariantsStayReady(t *testing.T) {
	dir := filepath.Join("..", "tmux", "testdata", "status_corpus")
	for _, name := range []string{
		"synth-codex-agent-quotes-status-line",
		"synth-codex-ran-grep-for-status-line",
		"synth-codex-user-pasted-working",
		"codex-idle-quoted-status-last-prose-line",
		"codex-idle-quoted-status-continuation",
		"codex-idle-quoted-prose-last-line",
		"codex-idle-historical-status-then-rule",
		"codex-idle-ctrlc-prose",
	} {
		data, err := os.ReadFile(filepath.Join(dir, name+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		for _, suffix := range []string{"", "   ", "\n\n"} {
			pane := string(data) + suffix
			if !tmux.NewPromptDetector("codex").HasPrompt(tmux.StripANSI(pane)) ||
				!paneShowsReadyPrompt(&mockReadyChecker{pane: pane}, "codex", PromptGates{}) ||
				!paneShowsReadyPrompt(&mockReadyChecker{pane: pane}, "codex", PromptGates{CodexPrompt: true}) {
				t.Errorf("%s suffix %q: quoted prose blocked ready composer", name, suffix)
			}
		}
	}
}
