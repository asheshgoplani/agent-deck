package ui

import (
	"os"
	"strings"
	"testing"
)

func TestUILifecycleConsumersRequireConfirmedKillAndWait(t *testing.T) {
	for file, minimum := range map[string]int{"home.go": 3, "web_mutator.go": 3} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(string(raw), ".KillAndWait()"); got < minimum {
			t.Fatalf("%s confirmed lifecycle teardowns=%d, want at least %d", file, got, minimum)
		}
	}
}
