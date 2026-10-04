package comms

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestRemoteLedgerImportCapsTextSummaryAndError(t *testing.T) {
	l, r, _ := openPair(t)
	large := strings.Repeat("é", MaxTextBytes)
	original := Record{Kind: KindTurn, From: "remote-child", Store: "remote-store", ID: NewID(time.Now()), Key: "turn-key", Text: large, Summary: large, Err: large, TH: "original-full-text-hash"}
	batch := []Exported{{Cursor: 7, Record: original}}
	for i := 0; i < 2; i++ {
		if cursor, err := l.Import("peer", batch); err != nil || cursor != 7 {
			t.Fatalf("import: %d %v", cursor, err)
		}
	}
	records, _, err := ReadAfter(r.Bus, 0, 0)
	if err != nil || len(records) != 1 {
		t.Fatalf("read imported records: %d %v", len(records), err)
	}
	got := records[0]
	for field, text := range map[string]string{"text": got.Text, "summary": got.Summary, "err": got.Err} {
		if len(text) > MaxTextBytes || text != CapText(large, MaxTextBytes) || !utf8.ValidString(text) {
			t.Errorf("%s: uncapped or malformed (%d bytes)", field, len(text))
		}
	}
	if got.TH != original.TH || got.ID != original.ID || got.Key != original.Key || got.SrcCursor != 7 {
		t.Fatalf("origin identity changed: %+v", got)
	}
}
