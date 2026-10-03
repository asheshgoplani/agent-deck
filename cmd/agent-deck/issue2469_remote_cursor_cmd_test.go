package main

// Incremental remote talkback at the CLI (issue #2469 family, PR3):
// `remote drain --json` reports cursor_before/cursor_after and sends the saved
// cursor on the next run; an old remote falls back to the full export;
// `inbox export --after` answers the wrapper; `inbox cursor` lists cursors.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestIssue2469PR3_RemoteDrainJSONCarriesCursorsAndWakesOnce(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	const conductor = "conductor-cursor"
	registerDrainTarget(t, conductor)

	var sent []session.RemoteCursor
	remoteCursorFetch = func(_ context.Context, _ string, _ session.RemoteConfig, c session.RemoteCursor) (session.RemoteExport, error) {
		sent = append(sent, c)
		if len(sent) > 1 {
			return session.RemoteExport{Records: nil, CursorNext: c, Writer: &session.WriterStatus{Running: true}}, nil
		}
		return session.RemoteExport{
			Records: []session.TransitionNotificationEvent{{
				ChildSessionID: "w1", ChildTitle: "worker", Profile: "default", FromStatus: "running", ToStatus: "waiting",
				Tier: "urgent", Text: "need a decision", Seq: 2, LastOutputHash: "turn:u2",
				Timestamp: time.Now().Add(-time.Minute),
			}},
			CursorNext: session.RemoteCursor{Seqs: map[string]int64{"w1": 2}},
			Writer:     &session.WriterStatus{Running: true},
		}, nil
	}
	var woke []string
	remoteDrainWake = func(p *session.Instance, _ string, ev session.TransitionNotificationEvent) {
		woke = append(woke, p.ID+"|"+ev.ChildSessionID)
	}
	fetch, legacyCalls := stubFetch(nil, nil)

	run := func() map[string]json.RawMessage {
		var stdout, stderr bytes.Buffer
		if code := runRemoteDrain(&stdout, &stderr, []string{"--json", "--into", conductor, "boxb"}, fetch); code != 0 {
			t.Fatalf("drain exit=%d stderr=%s", code, stderr.String())
		}
		var out map[string]json.RawMessage
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Fatalf("json: %v: %s", err, stdout.String())
		}
		return out
	}
	first := run()
	if string(first["cursor_before"]) != "{}" || string(first["cursor_after"]) != `{"w1":2}` {
		t.Fatalf("cursors wrong: before=%s after=%s", first["cursor_before"], first["cursor_after"])
	}
	if _, legacy := first["legacy_export"]; legacy || *legacyCalls != 0 {
		t.Fatal("a cursor-capable remote must not use the full export")
	}
	if len(woke) != 1 || woke[0] != conductor+"|boxb:w1" {
		t.Fatalf("urgent ingested record must wake the conductor once: %v", woke)
	}

	second := run()
	if len(sent) != 2 || sent[1].Seqs["w1"] != 2 {
		t.Fatalf("the saved cursor was not sent: %+v", sent)
	}
	if string(second["cursor_before"]) != `{"w1":2}` || string(second["fetched"]) != "0" || len(woke) != 1 {
		t.Fatalf("second drain: %v woke=%v", second, woke)
	}
}

func TestIssue2469PR3_RemoteDrainOldRemoteFallsBackToFullExport(t *testing.T) {
	drainTestHome(t) // the incremental read answers "unsupported"
	configureRemote(t, "boxb", "worker@box-b")
	const conductor = "conductor-oldremote"
	registerDrainTarget(t, conductor)
	fetch, calls := stubFetch([]session.TransitionNotificationEvent{
		remoteCompletion("w-old", "done the old way", time.Now().Add(-time.Minute)),
	}, nil)

	var stdout, stderr bytes.Buffer
	if code := runRemoteDrain(&stdout, &stderr, []string{"--json", "--into", conductor, "boxb"}, fetch); code != 0 {
		t.Fatalf("drain exit=%d stderr=%s", code, stderr.String())
	}
	var out map[string]json.RawMessage
	_ = json.Unmarshal(stdout.Bytes(), &out)
	if *calls != 1 || string(out["legacy_export"]) != "true" || out["cursor_after"] != nil || string(out["written"]) != "1" {
		t.Fatalf("fallback wrong: calls=%d out=%s", *calls, stdout.String())
	}
	if c, found, _ := session.LoadRemoteCursor("boxb", conductor); !found || !c.Legacy || len(c.Seqs) != 0 {
		t.Fatalf("a legacy drain saves only the position-less _legacy enrollment marker: %+v found=%v", c, found)
	}
}

func TestIssue2469PR3_InboxExportAfterAndCursorCLI(t *testing.T) {
	drainTestHome(t)
	if _, err := session.AppendTurnJournal(session.TurnJournalEntry{
		TS: time.Now().Add(-time.Minute), Child: "w9", Profile: "default", Status: "waiting",
		Tier: "urgent", UUID: "u9", Text: "question?", Question: true,
	}, 0); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runInbox(&out, []string{"export", "--json", "--after", "{}", "--with-writer"}); err != nil {
		t.Fatalf("export --after: %v", err)
	}
	var exp session.RemoteExport
	if err := json.Unmarshal(out.Bytes(), &exp); err != nil {
		t.Fatalf("wrapper not JSON: %v: %s", err, out.String())
	}
	if len(exp.Records) != 1 || exp.Records[0].Seq != 1 || exp.CursorNext.Seqs["w9"] != 1 || exp.Writer == nil {
		t.Fatalf("wrapper wrong: %s", out.String())
	}

	out.Reset()
	if err := runInbox(&out, []string{"export", "--json"}); err != nil || !strings.HasPrefix(strings.TrimSpace(out.String()), "[") {
		t.Fatalf("without --after the export must stay a bare array: %v %s", err, out.String())
	}
	if err := runInbox(&bytes.Buffer{}, []string{"export", "--after", "{}"}); err == nil {
		t.Fatal("--after without --json must be refused")
	}
	if err := runInbox(&bytes.Buffer{}, []string{"export", "--json", "--after", "garbage"}); err == nil {
		t.Fatal("a corrupt cursor must be refused, not treated as empty")
	}

	if err := session.SaveRemoteCursor("boxb", "conductor-x", exp.CursorNext); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runInbox(&out, []string{"cursor", "--json", "boxb"}); err != nil {
		t.Fatal(err)
	}
	var cursors []session.RemoteCursorFile
	if err := json.Unmarshal(out.Bytes(), &cursors); err != nil || len(cursors) != 1 ||
		cursors[0].Parent != "conductor-x" || cursors[0].Cursor.Seqs["w9"] != 1 {
		t.Fatalf("inbox cursor --json wrong: %v %s", err, out.String())
	}
	out.Reset()
	if err := runInbox(&out, []string{"cursor", "--json", "other"}); err != nil || strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("filter by remote: %v %s", err, out.String())
	}
}

// The remote reads the cursor from stdin (`--after -`): it names one entry per
// recently active child and would outgrow a single argv string.
func TestIssue2469PR3_InboxExportAfterReadsCursorFromStdin(t *testing.T) {
	drainTestHome(t)
	at := time.Now().Add(-time.Minute)
	for i := 1; i <= 2; i++ {
		if _, err := session.AppendTurnJournal(session.TurnJournalEntry{
			TS: at.Add(time.Duration(i) * time.Second), Child: "w7", Profile: "default", Status: "waiting",
			Tier: "urgent", UUID: "u7-" + string(rune('0'+i)), Text: "turn",
		}, 0); err != nil {
			t.Fatal(err)
		}
	}
	prev := inboxExportStdin
	t.Cleanup(func() { inboxExportStdin = prev })
	inboxExportStdin = func() io.Reader { return strings.NewReader(`{"w7": 1}`) }

	var out bytes.Buffer
	if err := runInbox(&out, []string{"export", "--json", "--after", "-", "--with-writer"}); err != nil {
		t.Fatalf("export --after -: %v", err)
	}
	var exp session.RemoteExport
	if err := json.Unmarshal(out.Bytes(), &exp); err != nil {
		t.Fatalf("wrapper not JSON: %v: %s", err, out.String())
	}
	if len(exp.Records) != 1 || exp.Records[0].Seq != 2 || exp.CursorNext.Seqs["w7"] != 2 {
		t.Fatalf("the stdin cursor was not applied: %s", out.String())
	}
}
