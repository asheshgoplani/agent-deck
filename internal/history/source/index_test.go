package source

import (
	"path/filepath"
	"testing"
)

func TestReadClaudeIndex(t *testing.T) {
	idx, err := ReadClaudeIndex(filepath.Join("testdata", "claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	if idx["/tmp/proj"].LastSessionID != "aaa" {
		t.Errorf("got %+v", idx["/tmp/proj"])
	}
	if idx["/tmp/proj"].LastModifiedMs != 1779099078389 {
		t.Errorf("modms = %d", idx["/tmp/proj"].LastModifiedMs)
	}
}

func TestReadClaudeIndexMissingFileIsEmpty(t *testing.T) {
	idx, err := ReadClaudeIndex(filepath.Join("testdata", "nope.json"))
	if err != nil || len(idx) != 0 {
		t.Fatalf("missing file: idx=%v err=%v", idx, err)
	}
}
