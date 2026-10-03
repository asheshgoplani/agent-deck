package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2469: conductor -> human tier CLIs.

func stubConductorTitles(t *testing.T, titles map[string]string) {
	t.Helper()
	prev := conductorTitleLookup
	conductorTitleLookup = func(id, _ string) string { return titles[id] }
	t.Cleanup(func() { conductorTitleLookup = prev })
}

func TestIssue2469_NotifyWritesRecord(t *testing.T) {
	withTempHomeAndConfig(t, "")
	var out bytes.Buffer
	if err := runConductorNotify(&out, strings.NewReader(""), []string{"--tier", "urgent", "--conductor", "ops", "--json", "prod", "is", "down"}, ""); err != nil {
		t.Fatalf("notify: %v", err)
	}
	var got struct {
		session.HumanOutboxRecord
		Conductor string `json:"conductor"`
		Created   bool   `json:"created"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("notify --json output %q: %v", out.String(), err)
	}
	if !got.Created || got.Conductor != "ops" || got.Tier != "urgent" || got.Text != "prod is down" || got.ID == "" {
		t.Fatalf("unexpected record: %+v", got)
	}
	items, err := session.ListHumanOutbox("ops", true)
	if err != nil || len(items) != 1 || items[0].ID != got.ID {
		t.Fatalf("record must be on disk: %+v err=%v", items, err)
	}

	// --message-file and the info tier; then a repeat is deduplicated.
	file := filepath.Join(t.TempDir(), "msg.txt")
	if err := os.WriteFile(file, []byte("lane C merged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"queued info", "already queued"} {
		out.Reset()
		if err := runConductorNotify(&out, strings.NewReader(""), []string{"--tier", "info", "--conductor", "ops", "--message-file", file}, ""); err != nil {
			t.Fatalf("notify %d: %v", i, err)
		}
		if !strings.Contains(out.String(), want) {
			t.Fatalf("notify %d printed %q, want %q", i, out.String(), want)
		}
	}
	if err := runConductorNotify(&out, strings.NewReader(""), []string{"--tier", "loud", "--conductor", "ops", "x"}, ""); err == nil {
		t.Fatal("unknown tier must fail")
	}
}

func TestIssue2469_ConductorNameFromSessionTitle(t *testing.T) {
	withTempHomeAndConfig(t, "")
	stubConductorTitles(t, map[string]string{"id-cond": "conductor-ops", "id-child": "worker"})
	t.Setenv("AGENTDECK_TITLE", "")

	t.Setenv("AGENTDECK_INSTANCE_ID", "id-cond")
	if name, err := resolveHumanConductor("", ""); err != nil || name != "ops" {
		t.Fatalf("from AGENTDECK_INSTANCE_ID's title: %q %v", name, err)
	}
	if name, err := resolveHumanConductor("infra", ""); err != nil || name != "infra" {
		t.Fatalf("--conductor wins: %q %v", name, err)
	}
	t.Setenv("AGENTDECK_INSTANCE_ID", "id-child")
	if _, err := resolveHumanConductor("", ""); err == nil {
		t.Fatal("a non-conductor session must not resolve")
	}
	t.Setenv("AGENTDECK_INSTANCE_ID", "unknown-id")
	t.Setenv("AGENTDECK_TITLE", "conductor-review")
	if name, err := resolveHumanConductor("", ""); err != nil || name != "review" {
		t.Fatalf("AGENTDECK_TITLE fallback: %q %v", name, err)
	}
	if _, err := resolveHumanConductor("../etc", ""); err == nil {
		t.Fatal("an invalid conductor name must be rejected")
	}
	if _, ok := conductorNameFromTitle("conductor-"); ok {
		t.Fatal("bare prefix is not a conductor")
	}

	// End to end: notify without --conductor lands in the resolved outbox.
	t.Setenv("AGENTDECK_INSTANCE_ID", "id-cond")
	var out bytes.Buffer
	if err := runConductorNotify(&out, strings.NewReader(""), []string{"--tier", "info", "progress"}, ""); err != nil {
		t.Fatal(err)
	}
	if items, _ := session.ListHumanOutbox("ops", true); len(items) != 1 {
		t.Fatalf("notify must resolve conductor ops from the session title: %+v", items)
	}
}

func TestIssue2469_OutboxListAndAck(t *testing.T) {
	withTempHomeAndConfig(t, "")
	a, _, _ := session.AppendHumanOutbox("ops", "urgent", "one")
	b, _, _ := session.AppendHumanOutbox("ops", "info", "two")

	var out bytes.Buffer
	if err := runConductorOutbox(&out, []string{"--json", "--conductor", "ops"}, ""); err != nil {
		t.Fatal(err)
	}
	var items []session.HumanOutboxRecord
	if err := json.Unmarshal(out.Bytes(), &items); err != nil || len(items) != 2 {
		t.Fatalf("outbox --json: %q err=%v", out.String(), err)
	}

	// --ack <id> <id>...: further ids may follow as arguments; repeat is a no-op.
	for i, want := range []int{2, 0} {
		out.Reset()
		if err := runConductorOutbox(&out, []string{"--conductor", "ops", "--json", "--ack", a.ID, b.ID}, ""); err != nil {
			t.Fatal(err)
		}
		var res struct{ Acked int }
		if err := json.Unmarshal(out.Bytes(), &res); err != nil || res.Acked != want {
			t.Fatalf("ack round %d: %q want acked=%d", i, out.String(), want)
		}
	}
	out.Reset()
	if err := runConductorOutbox(&out, []string{"--json", "--conductor", "ops"}, ""); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("nothing pending after ack, got %q", out.String())
	}
	out.Reset()
	if err := runConductorOutbox(&out, []string{"--json", "--all", "--conductor", "ops"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &items); err != nil || len(items) != 2 || !items[0].Acked {
		t.Fatalf("--all keeps delivered items: %q", out.String())
	}
}

func TestIssue2469_TierFilterCLI(t *testing.T) {
	withTempHomeAndConfig(t, "")
	reply := "[STATUS] 1 needs you.\nNEED: api-fix - staging or prod?\n[info] docs lane merged\n"
	var out bytes.Buffer
	if err := runConductorTierFilter(&out, strings.NewReader(reply), []string{"--json", "--conductor", "ops"}, ""); err != nil {
		t.Fatal(err)
	}
	var res tierFilterResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("tier-filter --json %q: %v", out.String(), err)
	}
	if len(res.SendNow) != 1 || res.Queued != 1 || !res.DigestDue || len(res.Digest) != 1 || res.Digest[0].Text != "docs lane merged" {
		t.Fatalf("urgent now, info queued and riding the urgent send: %+v", res)
	}

	// An info-only reply inside the digest window: queued, nothing due.
	out.Reset()
	if err := runConductorTierFilter(&out, strings.NewReader("[STATUS] ok\n[info] tests green"), []string{"--json", "--conductor", "calm"}, ""); err != nil {
		t.Fatal(err)
	}
	res = tierFilterResult{}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.SendNow) != 0 || res.Queued != 1 || res.DigestDue || len(res.Digest) != 0 || res.SendNow == nil || res.Digest == nil {
		t.Fatalf("info-only reply: %+v (arrays must encode as [], not null)", res)
	}
}

// --reply-id: the bridge's scan re-filters a reply whose send failed, and an
// outage spans several distinct heartbeat replies. Until --ack confirms a
// delivery, every one of them must keep returning the plain line.
func TestIssue2469_TierFilterCLIReplyIDRetry(t *testing.T) {
	withTempHomeAndConfig(t, "")
	const need = "NEED: api-fix - staging or prod?"
	filter := func(id string) []string {
		t.Helper()
		var out bytes.Buffer
		args := []string{"--json", "--conductor", "ops", "--reply-id", id}
		if err := runConductorTierFilter(&out, strings.NewReader("[STATUS] "+id+"\n"+need), args, ""); err != nil {
			t.Fatal(err)
		}
		var res tierFilterResult
		if err := json.Unmarshal(out.Bytes(), &res); err != nil {
			t.Fatalf("%s: %q (err=%v)", id, out.String(), err)
		}
		return res.SendNow
	}
	ack := func(id string) bool {
		t.Helper()
		var out bytes.Buffer
		if err := runConductorTierFilter(&out, strings.NewReader("ignored"), []string{"--json", "--conductor", "ops", "--ack", id}, ""); err != nil {
			t.Fatal(err)
		}
		var res struct {
			ReplyID   string `json:"reply_id"`
			Committed bool   `json:"committed"`
		}
		if err := json.Unmarshal(out.Bytes(), &res); err != nil || res.ReplyID != id {
			t.Fatalf("--ack %s: %q (err=%v)", id, out.String(), err)
		}
		return res.Committed
	}
	for _, id := range []string{"abc", "abc", "def", "ghi", "jkl"} { // failed sends, no ack
		if got := filter(id); len(got) != 1 || got[0] != need {
			t.Fatalf("undelivered %s: %q, want the plain NEED line", id, got)
		}
	}
	if !ack("jkl") || ack("jkl") || ack("abc") {
		t.Fatal("--ack commits the pending reply once; a repeat or a superseded id is a no-op")
	}
	if got := filter("mno"); len(got) != 1 || got[0] != need {
		t.Fatalf("cycle 2: %q", got)
	}
	ack("mno")
	if got := filter("pqr"); len(got) != 1 || !strings.HasPrefix(got[0], "STILL BLOCKED (3 cycles") {
		t.Fatalf("cycle 3 after two deliveries: %q", got)
	}
}
