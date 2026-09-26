package send

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// The send gate and prompt detector must agree on every labelled Codex frame.
// Checking the label too catches cases where both make the same unsafe choice.
func TestCodexSendGateMatchesCorpus(t *testing.T) {
	dir := filepath.Join("..", "tmux", "testdata", "status_corpus")
	labels, err := os.Open(filepath.Join(dir, "labels.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer labels.Close()

	scanner := bufio.NewScanner(labels)
	count := 0
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), "\t", 4)
		if len(fields) < 3 || fields[1] != "codex" {
			continue
		}
		name, expected := fields[0], fields[2] == "waiting"
		frame, err := os.ReadFile(filepath.Join(dir, name+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		count++
		t.Run(name, func(t *testing.T) {
			pane := string(frame)
			prompt := tmux.NewPromptDetector("codex").HasPrompt(tmux.StripANSI(pane))
			if fields[2] != "error" && prompt != expected {
				t.Errorf("detector ready = %v, want %v", prompt, expected)
			}
			for _, gates := range []PromptGates{{}, {CodexPrompt: true}} {
				ready := paneShowsReadyPrompt(&mockReadyChecker{pane: pane}, "codex", gates)
				if ready != prompt || (fields[2] != "error" && ready != expected) {
					t.Errorf("send ready = %v, detector = %v, label = %s (gates=%+v)", ready, prompt, fields[2], gates)
				}
			}
		})
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if count < 20 {
		t.Fatalf("only %d labelled Codex frames", count)
	}
}
