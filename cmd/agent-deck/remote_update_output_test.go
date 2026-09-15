package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestRemoteUpdateFlagsAnywhere(t *testing.T) {
	for _, args := range [][]string{
		{"box", "--from-build", "/tmp/build dir", "--force", "--dry-run", "--json"},
		{"--json", "--all", "--from-build=/tmp/build dir", "--dry-run", "--force"},
		{"--force", "box", "--from-build", "/tmp/build dir", "--json", "--dry-run"},
	} {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		all := fs.Bool("all", false, "")
		dry := fs.Bool("dry-run", false, "")
		force := fs.Bool("force", false, "")
		js := fs.Bool("json", false, "")
		build := fs.String("from-build", "", "")
		if err := fs.Parse(reorderRemoteArgs(fs, args)); err != nil {
			t.Fatal(err)
		}
		if !*dry || !*force || !*js || *build != "/tmp/build dir" {
			t.Fatalf("bad flags for %v", args)
		}
		if *all {
			if len(fs.Args()) != 0 {
				t.Fatal(fs.Args())
			}
		} else if !reflect.DeepEqual(fs.Args(), []string{"box"}) {
			t.Fatal(fs.Args())
		}
	}
}

func TestRemoteUpdateSummaryGoldenAndJSON(t *testing.T) {
	results := []session.RemoteUpdateResult{
		{Name: "alpha", Host: "a", From: "1.16.9", To: "1.16.10+local.abc", Outcome: session.RemoteUpdateOutcomeUpdated, Note: "deployed /opt/bin/agent-deck"},
		{Name: "beta", Host: "b", From: "1.17.0", To: "1.16.10+local.abc", Outcome: session.RemoteUpdateOutcomeFailed, Err: errors.New("refusing downgrade")},
		{Name: "gamma", Host: "c", To: "1.16.10+local.abc", Outcome: session.RemoteUpdateOutcomeSkipped, Note: "dry run"},
	}
	var out bytes.Buffer
	printRemoteUpdateTable(&out, results)
	golden, err := os.ReadFile(filepath.Join("testdata", "remote_update_summary.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != string(golden) {
		t.Fatalf("summary mismatch:\n%s\nwant:\n%s", out.String(), golden)
	}
	out.Reset()
	if err := writeRemoteUpdateJSON(&out, results); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[1]["error"] != "refusing downgrade" || rows[0]["outcome"] != "updated" || rows[2]["note"] != "dry run" {
		t.Fatalf("rows %+v", rows)
	}
}
