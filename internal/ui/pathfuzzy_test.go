package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFuzzyFilterPaths_EmptyQueryReturnsAll(t *testing.T) {
	corpus := []string{"/a/alpha", "/b/beta"}
	got := fuzzyFilterPaths(corpus, "")
	if len(got) != 2 {
		t.Fatalf("expected full corpus for empty query, got %v", got)
	}
}

func TestFuzzyFilterPaths_SubsequenceMatch(t *testing.T) {
	corpus := []string{
		"/home/me/projects/agent-deck",
		"/home/me/documents",
	}
	got := fuzzyFilterPaths(corpus, "adk")
	if len(got) != 1 || got[0] != corpus[0] {
		t.Fatalf("expected fuzzy hit on %q for query 'adk', got %v", corpus[0], got)
	}
}

func TestFuzzyFilterPaths_CaseInsensitive(t *testing.T) {
	corpus := []string{"/home/me/MyProject"}
	got := fuzzyFilterPaths(corpus, "myproj")
	if len(got) != 1 {
		t.Fatalf("expected case-insensitive match, got %v", got)
	}
}

func TestFuzzyFilterPaths_NoMatch(t *testing.T) {
	corpus := []string{"/a/alpha", "/b/beta"}
	got := fuzzyFilterPaths(corpus, "zzz")
	if len(got) != 0 {
		t.Fatalf("expected no matches for 'zzz', got %v", got)
	}
}

// stubDirCompletions swaps the filesystem completer for a fake and restores
// it when the test finishes.
func stubDirCompletions(t *testing.T, fn func(string) ([]string, error)) {
	t.Helper()
	prev := dirCompletionsFn
	dirCompletionsFn = fn
	t.Cleanup(func() { dirCompletionsFn = prev })
}

func TestFilterPathSuggestions_BareWordSkipsFilesystem(t *testing.T) {
	stubDirCompletions(t, func(string) ([]string, error) {
		t.Error("filesystem completions must not run for bare-word queries")
		return nil, nil
	})
	corpus := []string{"/home/me/agent-deck", "/home/me/other"}
	got := filterPathSuggestions(corpus, "deck")
	if len(got) != 1 || got[0] != corpus[0] {
		t.Fatalf("expected single fuzzy hit, got %v", got)
	}
}

func TestFilterPathSuggestions_PathQueryMergesFilesystem(t *testing.T) {
	stubDirCompletions(t, func(query string) ([]string, error) {
		if query != "/ho" {
			t.Errorf("unexpected completion query %q", query)
		}
		return []string{"/home"}, nil
	})
	corpus := []string{"/home/me/agent-deck"}
	got := filterPathSuggestions(corpus, "/ho")
	found := false
	for _, p := range got {
		if p == "/home" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected filesystem completion /home merged in, got %v", got)
	}
}

func TestFilterPathSuggestions_DedupsCorpusOverFilesystem(t *testing.T) {
	stubDirCompletions(t, func(string) ([]string, error) {
		return []string{"/home/me/agent-deck", "/home/newdir"}, nil
	})
	corpus := []string{"/home/me/agent-deck"}
	got := filterPathSuggestions(corpus, "/age")
	if len(got) != 2 {
		t.Fatalf("expected deduped result of 2, got %v", got)
	}
	if got[0] != "/home/me/agent-deck" {
		t.Fatalf("corpus match should rank first, got %v", got)
	}
}

func TestFilterPathSuggestions_CappedAtMax(t *testing.T) {
	stubDirCompletions(t, func(string) ([]string, error) {
		out := make([]string, 0, maxPathSuggestions+10)
		for i := 0; i < maxPathSuggestions+10; i++ {
			out = append(out, filepath.Join("/fs", string(rune('a'+i))))
		}
		return out, nil
	})
	var corpus []string
	got := filterPathSuggestions(corpus, "/f")
	if len(got) != maxPathSuggestions {
		t.Fatalf("expected results capped at %d, got %d", maxPathSuggestions, len(got))
	}
}

func TestFilterPathSuggestions_FilesystemErrorIgnored(t *testing.T) {
	stubDirCompletions(t, func(string) ([]string, error) {
		return nil, os.ErrPermission
	})
	corpus := []string{"/home/me/agent-deck"}
	got := filterPathSuggestions(corpus, "/ag")
	if len(got) != 1 {
		t.Fatalf("expected corpus-only fallback on fs error, got %v", got)
	}
}
