package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestDeadLetterInspectionPreservesBothRawStores(t *testing.T) {
	cliInboxTestHome(t)
	if err := os.MkdirAll(session.DeadLetterDir(), 0700); err != nil {
		t.Fatal(err)
	}
	valid := `{"child_session_id":"child","profile":"default","future_field":{"keep":true}}` + "\r\n"
	invalid := []byte("\xffbroken\x1b]52;tail")
	dead := append([]byte("\n"+valid+valid), invalid...)
	unowned := []byte("null\n{}\n{\"child_session_id\":\"orphan\"}\n")
	paths := map[string][]byte{session.DeadLetterPathFor("child"): dead, session.InboxPathFor(session.UnownedInboxID): unowned}
	for path, raw := range paths {
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := runInbox(&out, []string{"dead-letter", "list", "--json"}); err != nil {
		t.Fatal(err)
	}
	var records []struct {
		Ref   string `json:"ref"`
		Store string `json:"store"`
	}
	if err := json.Unmarshal(out.Bytes(), &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 6 {
		t.Fatalf("physical record count=%d, want6", len(records))
	}
	seen := make(map[string]bool)
	var gotDead, gotUnowned []byte
	for _, rec := range records {
		if seen[rec.Ref] {
			t.Fatal("duplicate physical lines have same reference")
		}
		seen[rec.Ref] = true
		out.Reset()
		if err := runInbox(&out, []string{"dead-letter", "show", rec.Ref, "--json"}); err != nil {
			t.Fatal(err)
		}
		var shown struct {
			RawBase64 string          `json:"raw_base64"`
			Event     json.RawMessage `json:"event"`
		}
		if err := json.Unmarshal(out.Bytes(), &shown); err != nil {
			t.Fatal(err)
		}
		raw, err := base64.StdEncoding.DecodeString(shown.RawBase64)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Store == "dead-letter" {
			gotDead = append(gotDead, raw...)
		} else {
			gotUnowned = append(gotUnowned, raw...)
		}
		if bytes.Equal(raw, []byte(valid)) && !bytes.Contains(shown.Event, []byte("future_field")) {
			t.Error("unknown parsed field lost")
		}
	}
	if !bytes.Equal(gotDead, dead[1:]) || !bytes.Equal(gotUnowned, unowned) {
		t.Fatal("show lost raw bytes, duplicates or line endings")
	}
	for path, want := range paths {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("inspection mutated %s", filepath.Base(path))
		}
	}
	out.Reset()
	if err := runInbox(&out, []string{"dead-letter", "show", records[2].Ref}); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(out.String(), '\x1b') {
		t.Fatal("human inspection emitted a raw terminal escape")
	}
	// An append invalidates refs rather than selecting another physical record.
	if err := os.WriteFile(session.DeadLetterPathFor("child"), append(dead, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runInbox(&out, []string{"dead-letter", "show", records[0].Ref}); err == nil {
		t.Fatal("stale source snapshot reference accepted")
	}
}

func TestDeadLetterInspectionRejectsUnsafeOrUnknownRequests(t *testing.T) {
	cliInboxTestHome(t)
	for _, args := range [][]string{
		{"dead-letter", "clear", "--all", "--yes"},
		{"dead-letter", "retry", "anything"},
		{"dead-letter", "show", "../../outside"},
		{"dead-letter", "list", "--store", "unknown"},
	} {
		if err := runInbox(&bytes.Buffer{}, args); err == nil {
			t.Errorf("accepted unsupported request %q", args)
		}
	}
	if _, err := os.Stat(session.InboxDir()); !os.IsNotExist(err) {
		t.Fatalf("inspection created inbox storage: %v", err)
	}
	if err := os.MkdirAll(session.DeadLetterDir(), 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("private-record"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, session.DeadLetterPathFor("link")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runInbox(&out, []string{"dead-letter", "list", "--json"}); err == nil {
		t.Fatal("followed nonregular ledger source")
	}
	if strings.Contains(out.String(), "private-record") {
		t.Fatal("printed symlink target content")
	}
}
