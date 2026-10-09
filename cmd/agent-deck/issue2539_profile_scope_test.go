package main

// Issue #2539 (reported by jwr456): `inbox export`, the read `remote drain`
// runs on the remote over ssh, returned the pending records of EVERY profile on
// the host. The inbox directory, the reserved _unowned ledger, the turn
// journals and the completion ledger are host-wide, and nothing filtered them
// by the invoking profile, so a remote configured with profile A drained
// profile B's titles, status flips and done summaries.
//
// What these pin:
//   - the full export and the cursor export return only the invoking profile's
//     records, from every source they read, and the cursor never names
//     another profile's child;
//   - a record with no profile cannot prove it belongs to the caller and is
//     never exported (fail closed);
//   - `remote drain` re-filters by the remote's configured profile before it
//     writes anything, and reports only the count of what it dropped.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// seedIssue2539Records writes one record per profile (A, B and blank) into
// each host-wide source the export reads.
func seedIssue2539Records(t *testing.T) {
	t.Helper()
	at := time.Now().Add(-time.Minute)
	for _, p := range []struct{ profile, tag string }{{"A", "a"}, {"B", "b"}, {"", "blank"}} {
		if err := session.CommitToInbox(session.UnownedInboxID, session.TransitionNotificationEvent{
			ChildSessionID: p.tag + "-unowned", ChildTitle: p.tag + " unowned title", Profile: p.profile,
			FromStatus: "running", ToStatus: "waiting", Timestamp: at,
		}); err != nil {
			t.Fatal(err)
		}
		if err := session.CommitToInbox("parent-2539", session.TransitionNotificationEvent{
			ChildSessionID: p.tag + "-inbox", ChildTitle: p.tag + " inbox title", Profile: p.profile,
			FromStatus: "running", ToStatus: "waiting", Timestamp: at,
		}); err != nil {
			t.Fatal(err)
		}
		if err := session.WriteLedgerEntry(session.CompletionLedgerEntry{
			ChildID: p.tag + "-ledger", Profile: p.profile, Title: p.tag + " ledger title",
			Status: "ok", Summary: p.tag + " private summary", FinishedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := session.AppendTurnJournal(session.TurnJournalEntry{
			TS: at, Child: p.tag + "-journal", Profile: p.profile, Status: "waiting",
			Tier: "urgent", UUID: "u-" + p.tag, Text: p.tag + " journal text",
		}, 0); err != nil {
			t.Fatal(err)
		}
	}
}

func issue2539AssertOnly(t *testing.T, label, out, own string, others ...string) {
	t.Helper()
	for _, other := range others {
		for _, src := range []string{"-unowned", "-inbox", "-ledger", "-journal"} {
			if strings.Contains(out, `"`+other+src) {
				t.Errorf("%s: record %s%s of another profile leaked:\n%s", label, other, src, out)
			}
		}
		if strings.Contains(out, other+" private summary") || strings.Contains(out, other+" unowned title") {
			t.Errorf("%s: another profile's content leaked:\n%s", label, out)
		}
	}
	if !strings.Contains(out, `"`+own+"-unowned") || !strings.Contains(out, `"`+own+"-ledger") {
		t.Errorf("%s: the invoking profile's own records are missing:\n%s", label, out)
	}
}

func TestIssue2539_InboxExportScopesToInvokingProfile(t *testing.T) {
	drainTestHome(t)
	seedIssue2539Records(t)

	for _, tc := range []struct{ profile, own, other string }{{"A", "a", "b"}, {"B", "b", "a"}} {
		var out bytes.Buffer
		if err := runInboxWithProfile(&out, []string{"export", "--json"}, tc.profile); err != nil {
			t.Fatalf("-p %s inbox export --json: %v", tc.profile, err)
		}
		issue2539AssertOnly(t, "-p "+tc.profile+" export", out.String(), tc.own, tc.other, "blank")
		if !strings.Contains(out.String(), `"`+tc.own+"-inbox") {
			t.Errorf("-p %s export: the per-parent inbox record is missing:\n%s", tc.profile, out.String())
		}
	}
}

func TestIssue2539_InboxExportAfterScopesToInvokingProfile(t *testing.T) {
	drainTestHome(t)
	seedIssue2539Records(t)

	for _, tc := range []struct{ profile, own, other string }{{"A", "a", "b"}, {"B", "b", "a"}} {
		var out bytes.Buffer
		if err := runInboxWithProfile(&out, []string{"export", "--json", "--after", "{}", "--with-writer"}, tc.profile); err != nil {
			t.Fatalf("-p %s inbox export --after: %v", tc.profile, err)
		}
		// The whole reply, cursor_next included: a cursor naming another
		// profile's child ids leaks them just as a record would.
		issue2539AssertOnly(t, "-p "+tc.profile+" export --after", out.String(), tc.own, tc.other, "blank")
	}
}

func TestIssue2539_RemoteDrainDropsRecordsOfOtherProfiles(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		name := "cursor"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			drainTestHome(t)
			cfg, err := session.LoadUserConfig()
			if err != nil || cfg == nil {
				cfg = &session.UserConfig{}
			}
			cfg.Remotes = map[string]session.RemoteConfig{"boxa": {Host: "worker@box-a", Profile: "A"}}
			if err := session.SaveUserConfig(cfg); err != nil {
				t.Fatal(err)
			}
			const conductor = "conductor-2539"
			registerDrainTarget(t, conductor)

			own := remoteCompletion("own-child", "own summary", time.Now().Add(-time.Minute))
			own.Profile = "A"
			foreign := remoteCompletion("foreign-child", "FOREIGN-SECRET", time.Now().Add(-time.Minute))
			foreign.Profile = "B"
			blank := remoteCompletion("blank-child", "BLANK-SECRET", time.Now().Add(-time.Minute))
			blank.Profile = ""
			mixed := []session.TransitionNotificationEvent{own, foreign, blank}

			fetch, _ := stubFetch(mixed, nil)
			if !legacy {
				remoteCursorFetch = func(context.Context, string, session.RemoteConfig, session.RemoteCursor) (session.RemoteExport, error) {
					return session.RemoteExport{Records: mixed, CursorNext: session.RemoteCursor{Seqs: map[string]int64{}},
						Writer: &session.WriterStatus{Running: true}}, nil
				}
			}

			var stdout, stderr bytes.Buffer
			if code := runRemoteDrain(&stdout, &stderr, []string{"--json", "--into", conductor, "boxa"}, fetch); code != 0 {
				t.Fatalf("drain exit=%d stderr=%s", code, stderr.String())
			}
			pending, err := session.ReadInboxEvents(conductor)
			if err != nil {
				t.Fatal(err)
			}
			if len(pending) != 1 || pending[0].ChildSessionID != "boxa:own-child" {
				t.Fatalf("only the remote profile's record may land: %+v", pending)
			}
			all := stdout.String() + stderr.String()
			if strings.Contains(all, "FOREIGN-SECRET") || strings.Contains(all, "foreign-child") ||
				strings.Contains(all, "BLANK-SECRET") || strings.Contains(all, "blank-child") {
				t.Fatalf("dropped records must be counted, never printed:\n%s", all)
			}
			var res map[string]json.RawMessage
			if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
				t.Fatalf("drain --json: %v\n%s", err, stdout.String())
			}
			if string(res["foreign_dropped"]) != "2" {
				t.Fatalf("foreign_dropped want 2: %s", stdout.String())
			}
		})
	}
}

// --profile names the scope explicitly (the drain always sends it); it must
// agree with -p when both are given, and scopes the export when -p is absent.
func TestIssue2539_InboxExportProfileFlag(t *testing.T) {
	drainTestHome(t)
	seedIssue2539Records(t)

	if err := runInboxWithProfile(&bytes.Buffer{}, []string{"export", "--json", "--profile", "B"}, "A"); err == nil ||
		!strings.Contains(err.Error(), "disagrees") {
		t.Fatalf("a --profile that disagrees with -p must be refused, got %v", err)
	}
	var out bytes.Buffer
	if err := runInboxWithProfile(&out, []string{"export", "--json", "--profile", "A"}, "A"); err != nil {
		t.Fatalf("matching --profile and -p: %v", err)
	}
	issue2539AssertOnly(t, "-p A --profile A", out.String(), "a", "b", "blank")
	out.Reset()
	if err := runInboxWithProfile(&out, []string{"export", "--json", "--profile", "B", "--after", "{}"}, ""); err != nil {
		t.Fatalf("--profile without -p: %v", err)
	}
	issue2539AssertOnly(t, "--profile B --after", out.String(), "b", "a", "blank")
}
