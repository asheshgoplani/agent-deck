package session

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

// Issue #2469, conductor -> human tier: a durable per-conductor outbox the
// bridge polls, and the tier filter that replaced the bridge's in-memory
// NEED retire counters.

func humanTierHome(t *testing.T, config string) {
	t.Helper()
	reviewTestHome(t, "default")
	if config == "" {
		return
	}
	path, err := GetUserConfigPath()
	if err != nil {
		t.Fatalf("GetUserConfigPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()
}

func mustTierFilter(t *testing.T, conductor, reply string, now time.Time) ([]string, int) {
	t.Helper()
	send, queued, err := TierFilter(conductor, reply, now)
	if err != nil {
		t.Fatalf("TierFilter: %v", err)
	}
	return send, queued
}

func TestIssue2469_TierFilterTable(t *testing.T) {
	humanTierHome(t, "")
	now := time.Now()
	cases := []struct {
		name       string
		reply      string
		wantSend   []string
		wantQueued int
	}{
		{"status only sends nothing", "[STATUS] All clear.", nil, 0},
		{"status and auto lines stay local", "[STATUS] Auto-responded to 1.\nAUTO: fe - used the middleware", nil, 0},
		{"urgent marker now", "[STATUS] x\n[urgent] prod deploy failed", []string{"[urgent] prod deploy failed"}, 0},
		{"URGENT: prefix now", "URGENT: disk full on build host", []string{"URGENT: disk full on build host"}, 0},
		{"NEED now", "  NEED: api-fix - staging or prod?  ", []string{"NEED: api-fix - staging or prod?"}, 0},
		{"info queued, not sent", "[STATUS] ok\n[info] lane C merged\nINFO: docs refreshed", nil, 2},
		{"duplicate urgent line in one reply is one alert", "[urgent] once\n[urgent] once", []string{"[urgent] once"}, 0},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conductor := "table" + string(rune('a'+i))
			send, queued := mustTierFilter(t, conductor, tc.reply, now)
			if !reflect.DeepEqual(send, tc.wantSend) || queued != tc.wantQueued {
				t.Fatalf("TierFilter(%q) = %q, %d; want %q, %d", tc.reply, send, queued, tc.wantSend, tc.wantQueued)
			}
		})
	}
	items, err := ListHumanOutbox("table"+string(rune('a'+5)), true)
	if err != nil || len(items) != 2 || items[0].Text != "lane C merged" || items[0].Tier != TurnTierInfo || items[1].Text != "docs refreshed" {
		t.Fatalf("info lines must be queued without their marker: %+v err=%v", items, err)
	}
}

// The retire counts live on disk, so every call below starts from what the
// previous one persisted, exactly like a bridge that restarted between ticks.
func TestIssue2469_NeedRetirePersistsAcrossRestart(t *testing.T) {
	humanTierHome(t, "")
	const need = "NEED: api-fix - staging or prod?"
	reply := "[STATUS] 1 needs you.\n" + need
	now := time.Now()
	want := [][]string{
		{need},
		{need},
		{"STILL BLOCKED (3 cycles, no reply): " + need},
		nil,
	}
	for cycle, w := range want {
		send, _ := mustTierFilter(t, "ops", reply, now)
		if !reflect.DeepEqual(send, w) {
			t.Fatalf("cycle %d: got %q, want %q", cycle+1, send, w)
		}
	}
	if counts := loadHumanNeedLedger("ops").Counts; counts[need] != 4 {
		t.Fatalf("ledger on disk must hold 4 cycles, got %v", counts)
	}
	// An empty reply (a digest-only query from the bridge) is not a reply
	// without the line: the count must survive it.
	if send, _ := mustTierFilter(t, "ops", "", now); send != nil || loadHumanNeedLedger("ops").Counts[need] != 4 {
		t.Fatalf("empty reply must leave the ledger alone: send=%q counts=%v", send, loadHumanNeedLedger("ops").Counts)
	}
	// The line disappears: its count resets, so a recurrence alerts again.
	if send, _ := mustTierFilter(t, "ops", "[STATUS] All clear.", now); send != nil {
		t.Fatalf("status-only reply sent %q", send)
	}
	if send, _ := mustTierFilter(t, "ops", reply, now); !reflect.DeepEqual(send, []string{need}) {
		t.Fatalf("recurring NEED must alert again, got %q", send)
	}
}

func TestIssue2469_NeedRetireCyclesConfigurable(t *testing.T) {
	humanTierHome(t, "[conductor]\nneed_retire_cycles = 2\n")
	reply := "[urgent] build red"
	now := time.Now()
	if send, _ := mustTierFilter(t, "ops", reply, now); len(send) != 1 || send[0] != reply {
		t.Fatalf("cycle 1: %q", send)
	}
	if send, _ := mustTierFilter(t, "ops", reply, now); len(send) != 1 || send[0] != "STILL BLOCKED (2 cycles, no reply): "+reply {
		t.Fatalf("cycle 2 must escalate with need_retire_cycles=2: %q", send)
	}
}

func TestIssue2469_DigestDueAtWindowOrWithUrgent(t *testing.T) {
	humanTierHome(t, "")
	t0 := time.Now()
	if _, queued := mustTierFilter(t, "ops", "[info] lane A done\n[info] lane B done", t0); queued != 2 {
		t.Fatalf("queued = %d, want 2", queued)
	}
	due, items, err := HumanDigest("ops", t0.Add(10*time.Minute), 30, false)
	if err != nil || due || len(items) != 2 {
		t.Fatalf("inside the window the digest is not due: due=%v items=%d err=%v", due, len(items), err)
	}
	if due, _, _ := HumanDigest("ops", t0.Add(10*time.Minute), 30, true); !due {
		t.Fatal("info must ride the next urgent send inside the window")
	}
	due, items, _ = HumanDigest("ops", t0.Add(31*time.Minute), 30, false)
	if !due || len(items) != 2 {
		t.Fatalf("past the window the digest is due with both items: due=%v items=%d", due, len(items))
	}

	// Delivering the digest (ack) records the flush; the window restarts.
	if n, err := AckHumanOutbox("ops", []string{items[0].ID, items[1].ID}); err != nil || n != 2 {
		t.Fatalf("ack: n=%d err=%v", n, err)
	}
	if _, _, err := AppendHumanOutbox("ops", TurnTierInfo, "lane C done"); err != nil {
		t.Fatal(err)
	}
	if due, _, _ := HumanDigest("ops", time.Now().Add(5*time.Minute), 30, false); due {
		t.Fatal("a new item right after a flush must wait for the window")
	}
	if due, _, _ := HumanDigest("ops", time.Now().Add(31*time.Minute), 30, false); !due {
		t.Fatal("digest due once the window after the last flush has passed")
	}
	if due, items, _ := HumanDigest("empty", time.Now().Add(time.Hour), 30, true); due || len(items) != 0 {
		t.Fatal("no info items: never due")
	}
}

func TestIssue2469_OutboxDedupAndIdempotentAck(t *testing.T) {
	humanTierHome(t, "")
	a, created, err := AppendHumanOutbox("ops", "urgent", "prod is down")
	if err != nil || !created || a.ID == "" || a.TextHash == "" || len(a.TextHash) != 16 {
		t.Fatalf("first append: %+v created=%v err=%v", a, created, err)
	}
	b, created, err := AppendHumanOutbox("ops", "urgent", "  prod is down \n")
	if err != nil || created || b.ID != a.ID {
		t.Fatalf("same text within 24h must be the same record: %+v created=%v err=%v", b, created, err)
	}
	if _, _, err := AppendHumanOutbox("ops", "loud", "x"); err == nil {
		t.Fatal("unknown tier must be rejected")
	}
	if _, _, err := AppendHumanOutbox("ops", "info", "   "); err == nil {
		t.Fatal("empty text must be rejected")
	}
	long := strings.Repeat("é", 3000) // 6000 bytes
	c, _, err := AppendHumanOutbox("ops", "info", long)
	if err != nil || len(c.Text) > MaxHumanOutboxTextBytes {
		t.Fatalf("text must be capped at %d bytes, got %d (err=%v)", MaxHumanOutboxTextBytes, len(c.Text), err)
	}

	for i := 0; i < 2; i++ {
		n, err := AckHumanOutbox("ops", []string{a.ID, "no-such-id"})
		if err != nil || n != 1-i {
			t.Fatalf("ack round %d: n=%d err=%v (second ack must be a no-op)", i, n, err)
		}
	}
	pending, _ := ListHumanOutbox("ops", true)
	if len(pending) != 1 || pending[0].ID != c.ID {
		t.Fatalf("only the unacked item stays pending: %+v", pending)
	}
	all, _ := ListHumanOutbox("ops", false)
	if len(all) != 2 || !all[0].Acked {
		t.Fatalf("--all lists acked items too, and never the digest marker: %+v", all)
	}
	// Dedup still holds for an acked item inside the window.
	if _, created, _ := AppendHumanOutbox("ops", "urgent", "prod is down"); created {
		t.Fatal("re-sending acked text within 24h must not queue it again")
	}
}

func TestIssue2469_ConductorHumanSettingsDefaults(t *testing.T) {
	var cfg struct {
		Conductor ConductorSettings `toml:"conductor"`
	}
	if _, err := toml.Decode("[conductor]\nheartbeat_interval = 15\n", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Conductor.GetHumanDigestMinutes() != 30 || cfg.Conductor.GetNeedRetireCycles() != 3 {
		t.Fatalf("defaults: digest=%d retire=%d", cfg.Conductor.GetHumanDigestMinutes(), cfg.Conductor.GetNeedRetireCycles())
	}
	if _, err := toml.Decode("[conductor]\nhuman_digest_minutes = 0\nneed_retire_cycles = 5\n", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Conductor.GetHumanDigestMinutes() != 0 || cfg.Conductor.GetNeedRetireCycles() != 5 {
		t.Fatalf("explicit: digest=%d retire=%d", cfg.Conductor.GetHumanDigestMinutes(), cfg.Conductor.GetNeedRetireCycles())
	}
}

func TestIssue2469_IdentitySentinelWording(t *testing.T) {
	inst := &Instance{ID: "c1", Title: "worker", Tool: "claude"}
	got := inst.BuildIdentityPrompt()
	if strings.Contains(got, "[DONE] event") || !strings.Contains(got, "one urgent record with that status and summary") {
		t.Fatalf("sentinel section must describe the urgent record the parent gets:\n%s", got)
	}
}

func TestIssue2469_IdentityCarriesSentinel(t *testing.T) {
	humanTierHome(t, "")
	claude := &Instance{ID: "c1", Title: "w", Tool: "claude"}
	if !claude.IdentityCarriesSentinel() {
		t.Fatal("claude at the default (full) context level carries the sentinel section")
	}
	for name, inst := range map[string]*Instance{
		"no-identity": {ID: "c2", Title: "w", Tool: "claude", IdentityInjectionDisabled: true},
		"primer":      {ID: "c3", Title: "w", Tool: "claude", ContextLevel: ContextLevelPrimer},
		"shell":       {ID: "c4", Title: "w", Tool: "shell"},
		"sandboxed":   {ID: "c5", Title: "w", Tool: "claude", Sandbox: NewSandboxConfig("")},
	} {
		if inst.IdentityCarriesSentinel() {
			t.Fatalf("%s: must not claim the sentinel section", name)
		}
	}
}

// mustTierFilterReply filters reply under id and returns what to send.
func mustTierFilterReply(t *testing.T, id, reply string) []string {
	t.Helper()
	send, _, err := TierFilterReply("ops", id, reply, time.Now())
	if err != nil {
		t.Fatalf("TierFilterReply(%s): %v", id, err)
	}
	return send
}

func mustAckTierFilterReply(t *testing.T, id string) bool {
	t.Helper()
	committed, err := AckTierFilterReply("ops", id, time.Now())
	if err != nil {
		t.Fatalf("AckTierFilterReply(%s): %v", id, err)
	}
	return committed
}

// The OS-heartbeat scan retries a reply whose delivery failed. Each retry
// must not count as a retire cycle, or a NEED line is retired (or first seen
// as STILL BLOCKED) without ever reaching the human.
func TestIssue2469_RetriedReplyDoesNotAdvanceRetire(t *testing.T) {
	humanTierHome(t, "")
	const need = "NEED: api-fix - staging or prod?"
	reply := "[STATUS] 1 needs you.\n" + need
	for attempt := 1; attempt <= 5; attempt++ { // 4 failed sends, then the one that lands
		if send := mustTierFilterReply(t, "r1", reply); !reflect.DeepEqual(send, []string{need}) {
			t.Fatalf("attempt %d of the same reply: got %q, want the NEED line", attempt, send)
		}
	}
	if counts := loadHumanNeedLedger("ops").Counts; counts[need] != 0 {
		t.Fatalf("nothing acked yet: the delivered counts must not move, ledger=%v", counts)
	}
	if !mustAckTierFilterReply(t, "r1") || loadHumanNeedLedger("ops").Counts[need] != 1 {
		t.Fatalf("the delivered reply is one cycle, ledger=%v", loadHumanNeedLedger("ops").Counts)
	}
	// New replies still advance and retire as before, each once delivered.
	if send := mustTierFilterReply(t, "r2", reply); !reflect.DeepEqual(send, []string{need}) {
		t.Fatalf("cycle 2: %q", send)
	}
	mustAckTierFilterReply(t, "r2")
	still := []string{"STILL BLOCKED (3 cycles, no reply): " + need}
	for i := 0; i < 2; i++ {
		if send := mustTierFilterReply(t, "r3", reply); !reflect.DeepEqual(send, still) {
			t.Fatalf("cycle 3 (try %d): %q", i+1, send)
		}
	}
	mustAckTierFilterReply(t, "r3")
	if send := mustTierFilterReply(t, "r4", reply); send != nil {
		t.Fatalf("cycle 4 must drop the line, got %q", send)
	}
}

// Review r2 #1: in OS-heartbeat mode every heartbeat is a NEW reply that
// repeats a standing NEED line. While the channel is down (stale token,
// invalid_auth, no network) none of them is delivered, so none may advance the
// retire count: the first reply that does get through must carry the plain
// NEED line, never STILL BLOCKED and never nothing.
func TestIssue2469_FailedDistinctRepliesDoNotAdvanceRetire(t *testing.T) {
	for _, failed := range []int{2, 3, 6} {
		t.Run(fmt.Sprintf("%d failed heartbeats", failed), func(t *testing.T) {
			humanTierHome(t, "")
			const need = "NEED: api-fix - staging or prod?"
			reply := func(n int) string {
				return fmt.Sprintf("[STATUS] heartbeat %d\n%s", n, need)
			}
			for n := 1; n <= failed; n++ { // sent, platform refused, never acked
				if send := mustTierFilterReply(t, fmt.Sprintf("rid-%d", n), reply(n)); !reflect.DeepEqual(send, []string{need}) {
					t.Fatalf("failed heartbeat %d: %q, want the plain NEED line", n, send)
				}
			}
			// A late ack for a superseded reply is ignored.
			if mustAckTierFilterReply(t, "rid-1") {
				t.Fatal("ack of a superseded reply must not commit its counts")
			}
			fixed := fmt.Sprintf("rid-%d", failed+1)
			if send := mustTierFilterReply(t, fixed, reply(failed+1)); !reflect.DeepEqual(send, []string{need}) {
				t.Fatalf("first delivered heartbeat after the outage: %q, want the plain NEED line", send)
			}
			if !mustAckTierFilterReply(t, fixed) || mustAckTierFilterReply(t, fixed) {
				t.Fatal("the delivered reply commits exactly once")
			}
			if c := loadHumanNeedLedger("ops").Counts[need]; c != 1 {
				t.Fatalf("one delivery is one cycle, got %d", c)
			}
		})
	}
}

// A reply with nothing to send needs no delivery, so it commits at once: a
// vanished line resets even while the channel is down, and the bridge
// restarting before it saved its state re-filters the last delivered reply
// from the counts that reply started with.
func TestIssue2469_TierFilterReplyCommitRules(t *testing.T) {
	humanTierHome(t, "")
	const need = "NEED: api-fix - staging or prod?"
	mustTierFilterReply(t, "a", need)
	mustAckTierFilterReply(t, "a")
	mustTierFilterReply(t, "b", need) // cycle 2, delivery fails
	if send := mustTierFilterReply(t, "c", "[STATUS] All clear."); send != nil {
		t.Fatalf("status-only reply sent %q", send)
	}
	if l := loadHumanNeedLedger("ops"); len(l.Counts) != 0 || l.Pending != nil {
		t.Fatalf("a reply without the line resets the count and drops the undelivered one: %+v", l)
	}
	if mustAckTierFilterReply(t, "b") {
		t.Fatal("an ack for a reply superseded by a nothing-to-send reply must be ignored")
	}

	mustTierFilterReply(t, "d", need)
	mustAckTierFilterReply(t, "d")
	mustTierFilterReply(t, "e", need)
	mustAckTierFilterReply(t, "e")
	// Replaying e (bridge restarted before saving that it handled e): the
	// same message again, still cycle 2, not cycle 3.
	if send := mustTierFilterReply(t, "e", need); !reflect.DeepEqual(send, []string{need}) {
		t.Fatalf("replay of the delivered reply: %q", send)
	}
	mustAckTierFilterReply(t, "e")
	if c := loadHumanNeedLedger("ops").Counts[need]; c != 2 {
		t.Fatalf("replay must not advance, got %d", c)
	}
	if _, err := AckTierFilterReply("ops", " ", time.Now()); err == nil {
		t.Fatal("an empty reply id must be rejected")
	}
}

// The outbox and the retire ledger hold what a conductor tells the human:
// owner-only directory and files.
func TestIssue2469_HumanOutboxIsOwnerOnly(t *testing.T) {
	humanTierHome(t, "")
	if _, _, err := AppendHumanOutbox("ops", TurnTierUrgent, "prod is down"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := TierFilterReply("ops", "r1", "NEED: x", time.Now()); err != nil {
		t.Fatal(err)
	}
	assertMode := func(path string, want os.FileMode) {
		t.Helper()
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Mode().Perm(); got != want {
			t.Fatalf("%s mode %o, want %o", filepath.Base(path), got, want)
		}
	}
	assertMode(HumanOutboxDir(), 0o700)
	assertMode(HumanOutboxPath("ops"), 0o600)
	assertMode(humanNeedLedgerPath("ops"), 0o600)
	// The rewrite path (ack) keeps the file owner-only.
	items, _ := ListHumanOutbox("ops", true)
	if _, err := AckHumanOutbox("ops", []string{items[0].ID}); err != nil {
		t.Fatal(err)
	}
	assertMode(HumanOutboxPath("ops"), 0o600)
}

// Urgent never dedups into info: the same text queued as info first must not
// leave the urgent notify waiting for the digest window.
func TestIssue2469_UrgentAfterInfoSameTextIsUpgraded(t *testing.T) {
	humanTierHome(t, "")
	info, created, err := AppendHumanOutbox("ops", TurnTierInfo, "prod is down")
	if err != nil || !created {
		t.Fatalf("info append: created=%v err=%v", created, err)
	}
	up, created, err := AppendHumanOutbox("ops", TurnTierUrgent, "prod is down")
	if err != nil || !created || up.Tier != TurnTierUrgent || up.ID != info.ID {
		t.Fatalf("urgent after pending info must upgrade it in place: %+v created=%v err=%v", up, created, err)
	}
	pending, _ := ListHumanOutbox("ops", true)
	if len(pending) != 1 || pending[0].Tier != TurnTierUrgent {
		t.Fatalf("one pending urgent record, got %+v", pending)
	}
	if again, created, _ := AppendHumanOutbox("ops", TurnTierInfo, "prod is down"); created || again.Tier != TurnTierUrgent {
		t.Fatalf("info after urgent dedups into the urgent record: %+v created=%v", again, created)
	}

	// Text already delivered as info (in a digest) gets a fresh urgent record.
	d, _, _ := AppendHumanOutbox("ops", TurnTierInfo, "disk at 90%")
	if n, err := AckHumanOutbox("ops", []string{d.ID}); err != nil || n != 1 {
		t.Fatalf("ack: n=%d err=%v", n, err)
	}
	u, created, err := AppendHumanOutbox("ops", TurnTierUrgent, "disk at 90%")
	if err != nil || !created || u.ID == d.ID || u.Tier != TurnTierUrgent || u.Acked {
		t.Fatalf("urgent after delivered info must queue anew: %+v created=%v err=%v", u, created, err)
	}
	if _, created, _ := AppendHumanOutbox("ops", TurnTierUrgent, "disk at 90%"); created {
		t.Fatal("a second urgent with the same text dedups again")
	}
}

// Review r3 (unbounded growth): nothing acks a deck-only conductor's outbox,
// so undelivered items must age out and the file must stay capped, on the
// append path itself, not only on an ack rewrite.
func TestIssue2469_UnackedOutboxIsBounded(t *testing.T) {
	humanTierHome(t, "")
	now := time.Now()
	old := now.Add(-humanOutboxUnackedRetention - time.Hour)
	if err := withHumanOutboxLock("ops", func() error {
		for i := 0; i < 50; i++ {
			rec := HumanOutboxRecord{ID: fmt.Sprintf("old%d", i), TS: old, Tier: TurnTierInfo, Text: fmt.Sprintf("old %d", i), TextHash: turnTextHash(fmt.Sprintf("old %d", i))}
			if err := appendJSONLine(HumanOutboxPath("ops"), rec); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fresh, _, err := AppendHumanOutbox("ops", TurnTierUrgent, "prod is down")
	if err != nil {
		t.Fatal(err)
	}
	pending, _ := ListHumanOutbox("ops", true)
	if len(pending) != 1 || pending[0].ID != fresh.ID {
		t.Fatalf("an append must prune unacked items older than %v: %d pending", humanOutboxUnackedRetention, len(pending))
	}

	// The cap: a chatty conductor never keeps more than humanOutboxMaxItems,
	// and info goes before urgent when the cap bites.
	for i := 0; i < humanOutboxMaxItems+20; i++ {
		if _, err := appendHumanOutboxLockedForTest("ops", TurnTierInfo, fmt.Sprintf("progress %d", i), now); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := ListHumanOutbox("ops", false)
	if len(all) != humanOutboxMaxItems {
		t.Fatalf("outbox holds %d items, want the cap %d", len(all), humanOutboxMaxItems)
	}
	if all[0].ID != fresh.ID || all[len(all)-1].Text != fmt.Sprintf("progress %d", humanOutboxMaxItems+19) {
		t.Fatalf("the cap must drop the oldest info, keeping the urgent item and the newest info: first=%+v last=%+v", all[0], all[len(all)-1])
	}
}

// An ack rewrite also drops unacked items past the retention window.
func TestIssue2469_AckRewritePrunesStaleUnacked(t *testing.T) {
	humanTierHome(t, "")
	now := time.Now()
	stale := HumanOutboxRecord{ID: "stale", TS: now.Add(-humanOutboxUnackedRetention - time.Minute), Tier: TurnTierUrgent, Text: "stale", TextHash: turnTextHash("stale")}
	keep, _, _ := AppendHumanOutbox("ops", TurnTierInfo, "keep me")
	ack, _, _ := AppendHumanOutbox("ops", TurnTierUrgent, "ack me")
	// Written raw, as an old binary or a long-gone append would have left it.
	if err := withHumanOutboxLock("ops", func() error { return appendJSONLine(HumanOutboxPath("ops"), stale) }); err != nil {
		t.Fatal(err)
	}
	if _, err := AckHumanOutbox("ops", []string{ack.ID}); err != nil {
		t.Fatal(err)
	}
	all, _ := ListHumanOutbox("ops", false)
	for _, r := range all {
		if r.ID == "stale" {
			t.Fatalf("ack rewrite kept an unacked item past retention: %+v", all)
		}
	}
	if len(all) != 2 || all[0].ID != keep.ID {
		t.Fatalf("fresh items survive the rewrite: %+v", all)
	}
}

func appendHumanOutboxLockedForTest(conductor, tier, text string, now time.Time) (HumanOutboxRecord, error) {
	var rec HumanOutboxRecord
	err := withHumanOutboxLock(conductor, func() error {
		var lerr error
		rec, _, lerr = appendHumanOutboxLocked(conductor, tier, text, now)
		return lerr
	})
	return rec, err
}
