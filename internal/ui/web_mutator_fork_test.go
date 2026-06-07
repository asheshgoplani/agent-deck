package ui

import (
	"os"
	"strings"
	"testing"
)

func TestWebMutatorForkSessionUsesSharedToolDispatcher(t *testing.T) {
	src, err := os.ReadFile("web_mutator.go")
	if err != nil {
		t.Fatalf("read web_mutator.go: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "CreateForkedInstanceForTool") {
		t.Fatal("WebMutator.ForkSession must use the shared cross-tool fork dispatcher")
	}
	if strings.Contains(body, "switch parent.Tool") {
		t.Fatal("WebMutator.ForkSession must not maintain a separate stale tool switch")
	}
}
