package ask

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

const testSecret = "s3cr3t-value-for-the-fake-vault-only"

func TestAskCreateWritesRecordWithoutValue(t *testing.T) {
	e := newTestEnv(t)
	rec := mustCreate(t, e, secretRequest("sess-1"))
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(rec.RequestID) {
		t.Fatalf("request id %q is not 16 hex", rec.RequestID)
	}
	if rec.State != StatePending || rec.Kind != KindSecret || rec.Profile != e.profile {
		t.Fatalf("record %+v", rec)
	}
	if got := rec.ExpiresAt.Sub(rec.RequestedAt); got != DefaultWait {
		t.Fatalf("expires after %v, want the default %v", got, DefaultWait)
	}
	dir, err := Dir(e.profile)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("ask dir %v %v, want a 0700 dir", info, err)
	}
	finfo, err := os.Stat(filepath.Join(dir, rec.RequestID+".json"))
	if err != nil || finfo.Mode().Perm() != 0o600 {
		t.Fatalf("record file %v %v, want 0600", finfo, err)
	}
	raw := recordFileBytes(t, e, rec.RequestID)
	for _, field := range []string{`"value":`, `"secret":`} {
		if bytes.Contains(raw, []byte(field)) {
			t.Fatalf("record carries %s: %s", field, raw)
		}
	}
}

func TestAskCreateValidates(t *testing.T) {
	e := newTestEnv(t)
	bad := map[string]NewRequest{
		"bad name":             func() NewRequest { r := secretRequest("s"); r.Name = "conductor/../x"; return r }(),
		"scope":                func() NewRequest { r := secretRequest("s"); r.Name = "other/GH_TOKEN"; return r }(),
		"no session":           func() NewRequest { r := secretRequest(""); return r }(),
		"wait too long":        func() NewRequest { r := secretRequest("s"); r.Wait = MaxWait + time.Second; return r }(),
		"wait too short":       func() NewRequest { r := secretRequest("s"); r.Wait = 10 * time.Millisecond; return r }(),
		"choice one option":    {Kind: KindChoice, SessionID: "s", Question: "q?", Options: []string{"a"}},
		"choice seven options": {Kind: KindChoice, SessionID: "s", Question: "q?", Options: []string{"1", "2", "3", "4", "5", "6", "7"}},
		"choice duplicate":     {Kind: KindChoice, SessionID: "s", Question: "q?", Options: []string{"a", "a"}},
		"choice control char":  {Kind: KindChoice, SessionID: "s", Question: "q?", Options: []string{"a\u202E", "b"}},
		"choice long option":   {Kind: KindChoice, SessionID: "s", Question: "q?", Options: []string{strings.Repeat("x", 81), "b"}},
		"text no question":     {Kind: KindText, SessionID: "s"},
		"unknown kind":         {Kind: "password", SessionID: "s"},
	}
	for name, req := range bad {
		if _, err := Create(e.profile, req); err == nil || !IsRefusal(err) {
			t.Errorf("%s: Create = %v, want a refusal", name, err)
		}
	}
	// Untrusted text is sanitized and capped, never refused for it.
	req := secretRequest("s")
	req.Reason = "line\none\u202E" + strings.Repeat("r", 300)
	rec := mustCreate(t, e, req)
	if strings.ContainsAny(rec.Reason, "\n\u202E") || len([]rune(rec.Reason)) > MaxReason {
		t.Fatalf("reason not sanitized: %q", rec.Reason)
	}
}

func TestAskRefusesExistingNameWithoutReplace(t *testing.T) {
	e := newTestEnv(t)
	e.preset(t, "conductor/GH_TOKEN_DECKBOX", "old")
	if _, err := Create(e.profile, secretRequest("s1")); !errors.Is(err, ErrExists) {
		t.Fatalf("Create over an existing name = %v, want ErrExists", err)
	}
	req := secretRequest("s1")
	req.Replace = true
	rec := mustCreate(t, e, req)
	if !rec.Replace {
		t.Fatal("record does not carry replace")
	}
	res, err := Deliver(e.profile, seenAndHeader(t, e, rec), []byte("new"))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Replaced != "yes" {
		t.Fatalf("replaced=%q, want yes", res.Replaced)
	}
	if v, _ := e.stored("conductor/GH_TOKEN_DECKBOX"); v != "new" {
		t.Fatalf("vault has %q", v)
	}
	// --replace on a name that does not exist is refused too: the owner
	// would be warned about a replacement that is not one.
	req = secretRequest("s2")
	req.Name = "conductor/MISSING"
	req.Replace = true
	if _, err := Create(e.profile, req); !errors.Is(err, ErrNotExists) {
		t.Fatalf("replace of a missing name = %v, want ErrNotExists", err)
	}
}

func TestAskDeliverRechecksExistsUnderLock(t *testing.T) {
	e := newTestEnv(t)
	rec := mustCreate(t, e, secretRequest("s1"))
	h := seenAndHeader(t, e, rec)
	// The name appears after the request was made (replace=false).
	e.preset(t, rec.Name, "someone else's")
	if _, err := Deliver(e.profile, h, []byte(testSecret)); !errors.Is(err, ErrReplaceMismatch) {
		t.Fatalf("Deliver = %v, want ErrReplaceMismatch", err)
	}
	if v, _ := e.stored(rec.Name); v != "someone else's" {
		t.Fatalf("vault overwritten: %q", v)
	}
}

func TestAskOneOpenRequestPerSession(t *testing.T) {
	e := newTestEnv(t)
	first := mustCreate(t, e, secretRequest("s1"))
	if _, err := Create(e.profile, secretRequest("s1")); !errors.Is(err, ErrOneOpen) {
		t.Fatalf("second Create = %v, want ErrOneOpen", err)
	}
	other := secretRequest("s2")
	if _, err := Create(e.profile, other); err != nil {
		t.Fatalf("another session: %v", err)
	}
	if _, err := CancelUnshown(e.profile, first.RequestID, "app"); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(e.profile, secretRequest("s1")); err != nil {
		t.Fatalf("after the first ended: %v", err)
	}
}

func TestAskOneOpenFreesARequestWhoseAskerDied(t *testing.T) {
	e := newTestEnv(t)
	req := secretRequest("s1")
	req.PID = deadPID(t)
	first := mustCreate(t, e, req)
	if _, err := Create(e.profile, secretRequest("s1")); err != nil {
		t.Fatalf("Create after the asker died: %v", err)
	}
	got, err := Show(e.profile, first.RequestID)
	if err != nil || got.State != StateCancelled {
		t.Fatalf("dead asker's request %+v %v, want cancelled", got, err)
	}
}

func TestAskRateLimit(t *testing.T) {
	e := newTestEnv(t)
	for i := 0; i < PerSessionPerHour; i++ {
		rec := mustCreate(t, e, secretRequest("s1"))
		if _, err := CancelUnshown(e.profile, rec.RequestID, "app"); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(time.Minute)
	}
	if _, err := Create(e.profile, secretRequest("s1")); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("request %d in an hour = %v, want ErrRateLimited", PerSessionPerHour+1, err)
	}
	e.clock.Advance(time.Hour)
	rec := mustCreate(t, e, secretRequest("s1"))
	if _, err := CancelUnshown(e.profile, rec.RequestID, "app"); err != nil {
		t.Fatal(err)
	}

	// The profile-wide limit spans sessions.
	e.clock.Advance(2 * time.Hour)
	for i := 0; i < PerProfilePerHour; i++ {
		rec := mustCreate(t, e, secretRequest("p"+string(rune('a'+i))))
		if _, err := CancelUnshown(e.profile, rec.RequestID, "app"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Create(e.profile, secretRequest("fresh-session")); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("request %d in an hour on the profile = %v, want ErrRateLimited", PerProfilePerHour+1, err)
	}
}

func TestAskDeliverStoresSecret(t *testing.T) {
	e := newTestEnv(t)
	rec := mustCreate(t, e, secretRequest("sess-42"))
	h := seenAndHeader(t, e, rec)
	value := []byte(testSecret + "\n")
	res, err := Deliver(e.profile, h, value)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != StateStored || res.Replaced != "no" || res.AnsweredBy != "tui" {
		t.Fatalf("result %+v", res)
	}
	if !bytes.Equal(value, make([]byte, len(value))) {
		t.Fatal("Deliver did not zero the value buffer")
	}
	if v, ok := e.stored(rec.Name); !ok || v != testSecret {
		t.Fatalf("vault holds %q (exists %v), want the value without its trailing newline", v, ok)
	}
	sid, _ := os.ReadFile(filepath.Join(e.store, "conductor_GH_TOKEN_DECKBOX.session"))
	if string(sid) != "sess-42" {
		t.Fatalf("vault saw AGENTDECK_SESSION_ID=%q, want sess-42", sid)
	}
	got, err := Show(e.profile, rec.RequestID)
	if err != nil || got.State != StateStored || got.AnsweredBy != "tui" || got.Replaced != "no" {
		t.Fatalf("record after deliver %+v %v", got, err)
	}
	if bytes.Contains(recordFileBytes(t, e, rec.RequestID), []byte(testSecret)) {
		t.Fatal("record file holds the value")
	}
	argv, _ := os.ReadFile(filepath.Join(e.store, ".argv"))
	if bytes.Contains(argv, []byte(testSecret)) {
		t.Fatal("the vault's argv carried the value")
	}
	acks := busFrames(t, e.profile, events.KindMacappAskAck)
	if len(acks) != 1 {
		t.Fatalf("%d acks, want 1", len(acks))
	}
	d := frameData(t, acks[0])
	if d["request_id"] != rec.RequestID || d["status"] != "stored" || d["answered_by"] != "tui" || d["replaced"] != "no" {
		t.Fatalf("ack %v", d)
	}
	if acks[0].SessionID != "sess-42" || bytes.Contains(acks[0].Data, []byte(testSecret)) {
		t.Fatalf("ack frame %+v", acks[0])
	}
}

func TestAskVaultChildEnvIsClean(t *testing.T) {
	e := newTestEnv(t)
	t.Setenv("ASK_TEST_PARENT_ONLY", "leak-marker")
	rec := mustCreate(t, e, secretRequest("sess-env"))
	if _, err := Deliver(e.profile, seenAndHeader(t, e, rec), []byte(testSecret)); err != nil {
		t.Fatal(err)
	}
	envDump, _ := os.ReadFile(filepath.Join(e.store, ".env"))
	sets := strings.Split(string(envDump), "--\n")
	last := sets[len(sets)-2] // the set call
	if !strings.Contains(last, "AGENTDECK_SESSION_ID=sess-env\n") || !strings.Contains(last, "PATH=/usr/local/bin:/usr/bin:/bin\n") {
		t.Fatalf("vault env:\n%s", last)
	}
	for _, leak := range []string{"ASK_TEST_PARENT_ONLY", "AGENTDECK_PROFILE", "TMUX", "XDG_", testSecret} {
		if strings.Contains(last, leak) {
			t.Fatalf("vault env carries %q:\n%s", leak, last)
		}
	}
}

func TestAskDeliverHeaderMismatchRefused(t *testing.T) {
	e := newTestEnv(t)
	rec := mustCreate(t, e, secretRequest("s1"))
	good := seenAndHeader(t, e, rec)
	appNonce, err := MarkSeen(e.profile, rec.RequestID, "app")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(h *Header){
		"name":                func(h *Header) { h.Name = "conductor/OTHER" },
		"kind":                func(h *Header) { h.Kind = KindText },
		"replace":             func(h *Header) { h.Replace = true },
		"nonce":               func(h *Header) { h.Nonce = "0000000000000000" },
		"empty nonce":         func(h *Header) { h.Nonce = "" },
		"nonce of another UI": func(h *Header) { h.Nonce = appNonce }, // tui claims the app's nonce
		"answered_by":         func(h *Header) { h.AnsweredBy = "asker" },
		"request id":          func(h *Header) { h.RequestID = "ffffffffffffffff" },
	}
	for name, mutate := range cases {
		h := good
		mutate(&h)
		_, err := Deliver(e.profile, h, []byte(testSecret))
		if err == nil || !IsRefusal(err) {
			t.Errorf("%s: Deliver = %v, want a refusal", name, err)
		}
		if name != "request id" && !errors.Is(err, ErrHeaderMismatch) {
			t.Errorf("%s: Deliver = %v, want ErrHeaderMismatch", name, err)
		}
		if err != nil && strings.Contains(err.Error(), testSecret) {
			t.Errorf("%s: error carries the value", name)
		}
	}
	if _, ok := e.stored(rec.Name); ok {
		t.Fatal("a mismatched header reached the vault")
	}
	if got, _ := Show(e.profile, rec.RequestID); got.State != StatePending {
		t.Fatalf("state %s after refused delivers, want pending", got.State)
	}
	if _, err := Deliver(e.profile, good, []byte(testSecret)); err != nil {
		t.Fatalf("the matching header: %v", err)
	}
}

func TestAskDeliverReplayRefused(t *testing.T) {
	e := newTestEnv(t)
	rec := mustCreate(t, e, secretRequest("s1"))
	h := seenAndHeader(t, e, rec)
	if _, err := Deliver(e.profile, h, []byte(testSecret)); err != nil {
		t.Fatal(err)
	}
	if _, err := Deliver(e.profile, h, []byte("attacker")); !errors.Is(err, ErrNotPending) {
		t.Fatalf("replayed Deliver = %v, want ErrNotPending", err)
	}
	if v, _ := e.stored(rec.Name); v != testSecret {
		t.Fatalf("replay changed the vault: %q", v)
	}
}

func TestAskDeliverAfterExpiryRefusedEvenWithAskerDead(t *testing.T) {
	e := newTestEnv(t)
	req := secretRequest("s1")
	req.Wait = time.Minute
	req.PID = deadPID(t)
	rec := mustCreate(t, e, req)
	h := seenAndHeader(t, e, rec)
	e.clock.Advance(time.Minute + time.Second)
	if _, err := Deliver(e.profile, h, []byte(testSecret)); !errors.Is(err, ErrExpired) {
		t.Fatalf("Deliver after expiry = %v, want ErrExpired", err)
	}
	if _, ok := e.stored(rec.Name); ok {
		t.Fatal("an expired request reached the vault")
	}
	if got, _ := Show(e.profile, rec.RequestID); got.State != StateExpired {
		t.Fatalf("state %s, want expired", got.State)
	}
	acks := busFrames(t, e.profile, events.KindMacappAskAck)
	if len(acks) != 1 || frameData(t, acks[0])["status"] != "expired" {
		t.Fatalf("acks %v, want one expired ack", acks)
	}
}

func TestAskDeliverStdinCaps(t *testing.T) {
	e := newTestEnv(t)
	rec := mustCreate(t, e, secretRequest("s1"))
	h := seenAndHeader(t, e, rec)
	for name, v := range map[string][]byte{
		"empty":        {},
		"newline only": []byte("\n"),
		"over 64 KiB":  bytes.Repeat([]byte("a"), MaxSecretBytes+1),
	} {
		if _, err := Deliver(e.profile, h, v); err == nil || !IsRefusal(err) {
			t.Errorf("%s: Deliver = %v, want a refusal", name, err)
		}
	}
	if _, ok := e.stored(rec.Name); ok {
		t.Fatal("a refused value reached the vault")
	}
	if _, err := Deliver(e.profile, h, bytes.Repeat([]byte("a"), MaxSecretBytes)); err != nil {
		t.Fatalf("exactly 64 KiB: %v", err)
	}
}

func TestAskDeliverNeverEchoesVaultOutput(t *testing.T) {
	e := newTestEnv(t)
	if err := os.WriteFile(filepath.Join(e.store, ".fail"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rec := mustCreate(t, e, secretRequest("s1"))
	_, err := Deliver(e.profile, seenAndHeader(t, e, rec), []byte(testSecret))
	if !errors.Is(err, ErrVaultRefused) {
		t.Fatalf("Deliver = %v, want ErrVaultRefused", err)
	}
	if strings.Contains(err.Error(), testSecret) || !strings.Contains(err.Error(), "exit 7") {
		t.Fatalf("error %q: want the exit code only", err)
	}
	if got, _ := Show(e.profile, rec.RequestID); got.State != StatePending {
		t.Fatalf("state %s after a vault failure, want pending (the owner may retry)", got.State)
	}
}

func TestAskTextAnswerRefusesCredential(t *testing.T) {
	e := newTestEnv(t)
	rec := mustCreate(t, e, NewRequest{Kind: KindText, SessionID: "s1", Question: "Paste your token to continue", PID: os.Getpid()})
	h := seenAndHeader(t, e, rec)
	token := "ghp_" + strings.Repeat("Ab3d", 9)
	if _, err := Deliver(e.profile, h, []byte(token)); !errors.Is(err, ErrLooksLikeSecret) {
		t.Fatalf("Deliver(token) = %v, want ErrLooksLikeSecret", err)
	}
	if bytes.Contains(recordFileBytes(t, e, rec.RequestID), []byte(token)) {
		t.Fatal("the refused answer was written to the record")
	}
	for _, bad := range []string{"two\nlines", strings.Repeat("x", MaxTextAnswer+1), "nul\x00byte"} {
		if _, err := Deliver(e.profile, h, []byte(bad)); err == nil || !IsRefusal(err) {
			t.Errorf("Deliver(%q) = %v, want a refusal", bad, err)
		}
	}
	res, err := Deliver(e.profile, h, []byte("use the staging database\n"))
	if err != nil || res.Status != StateAnswered {
		t.Fatalf("plain answer: %+v %v", res, err)
	}
	full, err := readAnswered(e.profile, rec.RequestID)
	if err != nil || full.Answer != "use the staging database" {
		t.Fatalf("answer %q %v", full.Answer, err)
	}
	if shown, _ := Show(e.profile, rec.RequestID); shown.Answer != "" {
		t.Fatal("Show returned the answer")
	}
	if err := ClearAnswer(e.profile, rec.RequestID); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(recordFileBytes(t, e, rec.RequestID), []byte("staging")) {
		t.Fatal("answer kept after ClearAnswer")
	}
}

func TestAskChoiceAnswerMustBeAnOption(t *testing.T) {
	e := newTestEnv(t)
	rec := mustCreate(t, e, NewRequest{Kind: KindChoice, SessionID: "s1", Question: "Which database?", Options: []string{"staging", "prod"}, PID: os.Getpid()})
	h := seenAndHeader(t, e, rec)
	if _, err := Deliver(e.profile, h, []byte("dev")); err == nil || !IsRefusal(err) {
		t.Fatalf("Deliver(not an option) = %v, want a refusal", err)
	}
	res, err := Deliver(e.profile, h, []byte("prod"))
	if err != nil || res.Status != StateAnswered {
		t.Fatalf("Deliver(prod) = %+v %v", res, err)
	}
}

func TestAskCancel(t *testing.T) {
	e := newTestEnv(t)
	rec := mustCreate(t, e, secretRequest("s1"))
	nonce, err := MarkSeen(e.profile, rec.RequestID, "app")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Cancel(e.profile, rec.RequestID, "app", "0000000000000000"); !errors.Is(err, ErrHeaderMismatch) {
		t.Fatalf("Cancel with a wrong nonce = %v, want ErrHeaderMismatch", err)
	}
	if _, err := Cancel(e.profile, rec.RequestID, "nobody", ""); err == nil {
		t.Fatal("Cancel by an unknown UI accepted")
	}
	res, err := Cancel(e.profile, rec.RequestID, "app", nonce)
	if err != nil || res.Status != StateCancelled {
		t.Fatalf("Cancel = %+v %v", res, err)
	}
	if _, err := Deliver(e.profile, headerFor(rec, "app", nonce), []byte(testSecret)); !errors.Is(err, ErrNotPending) {
		t.Fatalf("Deliver after cancel = %v, want ErrNotPending", err)
	}
	acks := busFrames(t, e.profile, events.KindMacappAskAck)
	if len(acks) != 1 || frameData(t, acks[0])["status"] != "cancelled" || frameData(t, acks[0])["answered_by"] != "app" {
		t.Fatalf("acks %v", acks)
	}
}

func TestAskMarkSeenPublishesSeen(t *testing.T) {
	e := newTestEnv(t)
	rec := mustCreate(t, e, secretRequest("s1"))
	nonce, err := MarkSeen(e.profile, rec.RequestID, "tui")
	if err != nil || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(nonce) {
		t.Fatalf("MarkSeen = %q %v", nonce, err)
	}
	frames := busFrames(t, e.profile, events.KindMacappAskSeen)
	if len(frames) != 1 {
		t.Fatalf("%d seen frames, want 1", len(frames))
	}
	d := frameData(t, frames[0])
	if d["request_id"] != rec.RequestID || d["nonce"] != nonce || d["by"] != "tui" || frames[0].SessionID != "s1" {
		t.Fatalf("seen frame %v", d)
	}
	got, _ := Show(e.profile, rec.RequestID)
	if len(got.SeenBy) != 1 || got.SeenBy[0] != "tui" {
		t.Fatalf("seen_by %v", got.SeenBy)
	}
	if _, err := MarkSeen(e.profile, rec.RequestID, "asker"); err == nil {
		t.Fatal("MarkSeen by a non-UI accepted")
	}
}

func TestAskListShowsPendingWithoutAnswers(t *testing.T) {
	e := newTestEnv(t)
	a := mustCreate(t, e, secretRequest("s1"))
	b := mustCreate(t, e, NewRequest{Kind: KindText, SessionID: "s2", Question: "Name?", PID: os.Getpid()})
	if _, err := Deliver(e.profile, seenAndHeader(t, e, b), []byte("Ashesh")); err != nil {
		t.Fatal(err)
	}
	list, err := List(e.profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].RequestID != a.RequestID {
		t.Fatalf("List = %+v, want only the pending secret request", list)
	}
	all, err := ListAll(e.profile)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListAll = %d %v", len(all), err)
	}
	for _, r := range all {
		if r.Answer != "" {
			t.Fatal("ListAll returned an answer")
		}
	}
	// An expired request is no longer listed.
	e.clock.Advance(DefaultWait + time.Second)
	if list, _ := List(e.profile); len(list) != 0 {
		t.Fatalf("List after expiry = %+v", list)
	}
}

func TestAskDisabledAndScopesFromConfig(t *testing.T) {
	e := newTestEnv(t)
	e.writeConfig(t, "[ask]\nsecret_scopes = [\"work/\"]\n")
	if _, err := Create(e.profile, secretRequest("s1")); !errors.Is(err, ErrScope) {
		t.Fatalf("conductor/ outside secret_scopes = %v, want ErrScope", err)
	}
	req := secretRequest("s1")
	req.Name = "work/KEY"
	rec := mustCreate(t, e, req)
	h := seenAndHeader(t, e, rec)

	e.writeConfig(t, "[ask]\nenabled = false\n")
	if _, err := Create(e.profile, NewRequest{Kind: KindText, SessionID: "s2", Question: "q"}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Create with [ask] enabled = false: %v", err)
	}
	if _, err := Deliver(e.profile, h, []byte(testSecret)); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Deliver with [ask] enabled = false: %v", err)
	}
}

func TestAskDeliverRechecksScope(t *testing.T) {
	e := newTestEnv(t)
	rec := mustCreate(t, e, secretRequest("s1"))
	h := seenAndHeader(t, e, rec)
	e.writeConfig(t, "[ask]\nsecret_scopes = [\"work/\"]\n")
	if _, err := Deliver(e.profile, h, []byte(testSecret)); !errors.Is(err, ErrScope) {
		t.Fatalf("Deliver after the scope was removed = %v, want ErrScope", err)
	}
}

func TestAskRecordSymlinkRefused(t *testing.T) {
	e := newTestEnv(t)
	rec := mustCreate(t, e, secretRequest("s1"))
	h := seenAndHeader(t, e, rec)
	dir, _ := Dir(e.profile)
	path := filepath.Join(dir, rec.RequestID+".json")
	elsewhere := filepath.Join(t.TempDir(), "x.json")
	if err := os.Rename(path, elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Deliver(e.profile, h, []byte(testSecret)); err == nil {
		t.Fatal("Deliver followed a symlinked record")
	}
	if _, ok := e.stored(rec.Name); ok {
		t.Fatal("a symlinked record reached the vault")
	}
}

func TestAskDirMustBePrivate(t *testing.T) {
	e := newTestEnv(t)
	mustCreate(t, e, secretRequest("s1"))
	dir, _ := Dir(e.profile)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := List(e.profile); err == nil {
		t.Fatal("List accepted a 0755 ask dir")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	moved := dir + ".real"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := List(e.profile); err == nil {
		t.Fatal("List accepted a symlinked ask dir")
	}
}

func TestAskVaultTrustCheck(t *testing.T) {
	own := filepath.Join(t.TempDir(), "deck-secret")
	if err := os.WriteFile(own, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() != 0 {
		if err := checkVaultTrusted(own); err == nil {
			t.Fatal("a vault owned by the user was trusted")
		}
	}
	if err := checkVaultTrusted(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, ErrNoVault) {
		t.Fatalf("missing vault = %v, want ErrNoVault", err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink("/usr/bin/env", link); err != nil {
		t.Fatal(err)
	}
	if err := checkVaultTrusted(link); err == nil {
		t.Fatal("a symlinked vault was trusted")
	}
	// Root-owned dirs are not enough: the file itself must be a regular,
	// non-writable file (/dev/null is a root-owned, world-writable device).
	if info, err := os.Lstat("/dev/null"); err == nil && ownerUID(info) == 0 {
		if err := checkVaultTrusted("/dev/null"); !errors.Is(err, ErrVaultUntrusted) {
			t.Fatalf("/dev/null = %v, want ErrVaultUntrusted", err)
		}
	}
	// A root-owned binary in root-owned, non-writable dirs passes.
	if info, err := os.Lstat("/usr/bin/env"); err == nil && info.Mode().IsRegular() && ownerUID(info) == 0 {
		if err := checkVaultTrusted("/usr/bin/env"); err != nil {
			t.Fatalf("/usr/bin/env: %v", err)
		}
	}
	if vaultPathDefault != "/usr/local/bin/deck-secret" {
		t.Fatalf("vault path %q", vaultPathDefault)
	}
}

func TestAskAppSeenFrameRecordedByAsker(t *testing.T) {
	e := newTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	type result struct {
		rec Record
		err error
	}
	done := make(chan result, 1)
	go func() {
		rec, err := Ask(ctx, e.profile, secretRequest("s-app"), AwaitOptions{FallbackAfter: time.Hour})
		done <- result{rec, err}
	}()
	rec := waitPending(t, e, "s-app")
	if rec.AskEventID == "" {
		t.Fatal("record has no ask_event_id")
	}
	ann := busFrames(t, e.profile, events.KindMacappAsk)
	if len(ann) != 1 || ann[0].EventID != rec.AskEventID || frameData(t, ann[0])["request_id"] != rec.RequestID {
		t.Fatalf("macapp.ask frames %+v", ann)
	}
	for _, field := range []string{`"answer"`, `"value"`} {
		if bytes.Contains(ann[0].Data, []byte(field)) {
			t.Fatalf("macapp.ask carries %s", field)
		}
	}
	// The app announces its nonce over the bus (events publish), not in process.
	nonce := "0123456789abcdef"
	bus := events.OpenProfile(e.profile)
	bus.Publish(events.KindMacappAskSeen, "s-app", map[string]string{"request_id": rec.RequestID, "ask_event_id": rec.AskEventID, "nonce": nonce, "by": "app"})
	if !bus.Flush(5 * time.Second) {
		t.Fatal("flush")
	}
	bus.Close()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, _ := Show(e.profile, rec.RequestID)
		if len(got.SeenBy) == 1 && got.SeenBy[0] == "app" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("asker never recorded the app's seen frame: %+v", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := Deliver(e.profile, headerFor(rec, "app", nonce), []byte(testSecret)); err != nil {
		t.Fatalf("Deliver with the app's nonce: %v", err)
	}
	r := <-done
	if r.err != nil || r.rec.State != StateStored || r.rec.AnsweredBy != "app" {
		t.Fatalf("Ask = %+v %v", r.rec, r.err)
	}
}

func TestAskForgedSeenFrameForOtherRequestIgnored(t *testing.T) {
	e := newTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Record, 1)
	go func() {
		rec, _ := Ask(ctx, e.profile, secretRequest("s1"), AwaitOptions{FallbackAfter: time.Hour})
		done <- rec
	}()
	rec := waitPending(t, e, "s1")
	bus := events.OpenProfile(e.profile)
	// Wrong ask_event_id, wrong session, bad nonce: none count as seen.
	bus.Publish(events.KindMacappAskSeen, "s1", map[string]string{"request_id": rec.RequestID, "ask_event_id": "ffffffffffffffff", "nonce": "0123456789abcdef", "by": "app"})
	bus.Publish(events.KindMacappAskSeen, "other", map[string]string{"request_id": rec.RequestID, "ask_event_id": rec.AskEventID, "nonce": "0123456789abcdef", "by": "app"})
	bus.Publish(events.KindMacappAskSeen, "s1", map[string]string{"request_id": rec.RequestID, "ask_event_id": rec.AskEventID, "nonce": "not-hex", "by": "app"})
	bus.Flush(5 * time.Second)
	bus.Close()
	time.Sleep(600 * time.Millisecond)
	if got, _ := Show(e.profile, rec.RequestID); len(got.SeenBy) != 0 {
		t.Fatalf("forged seen frames recorded: %v", got.SeenBy)
	}
	cancel()
	if got := <-done; got.State != StateCancelled || got.AnsweredBy != "asker" {
		t.Fatalf("interrupted Ask = %+v, want cancelled by asker", got)
	}
}

func TestAskExpiresAtDeadline(t *testing.T) {
	e := newTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req := secretRequest("s1")
	req.Wait = time.Minute
	done := make(chan Record, 1)
	go func() {
		rec, _ := Ask(ctx, e.profile, req, AwaitOptions{FallbackAfter: time.Hour})
		done <- rec
	}()
	waitPending(t, e, "s1")
	e.clock.Advance(time.Minute)
	select {
	case got := <-done:
		if got.State != StateExpired {
			t.Fatalf("state %s, want expired", got.State)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Ask did not expire")
	}
}

func TestAskNoFallbackReturnsErrNoUI(t *testing.T) {
	e := newTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	called := 0
	rec, err := Ask(ctx, e.profile, secretRequest("s1"), AwaitOptions{FallbackAfter: 50 * time.Millisecond, NoFallback: true, OnFallback: func(Record) { called++ }})
	if !errors.Is(err, ErrNoUI) {
		t.Fatalf("Ask = %v, want ErrNoUI", err)
	}
	if called != 0 {
		t.Fatal("--no-fallback printed the fallback")
	}
	if got, _ := Show(e.profile, rec.RequestID); got.State != StateCancelled {
		t.Fatalf("state %s, want cancelled", got.State)
	}
}

func TestAskFallbackOnceThenDirectVaultSet(t *testing.T) {
	e := newTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fallbacks := make(chan Record, 4)
	done := make(chan Record, 1)
	go func() {
		rec, _ := Ask(ctx, e.profile, secretRequest("s1"), AwaitOptions{FallbackAfter: 50 * time.Millisecond, OnFallback: func(r Record) { fallbacks <- r }})
		done <- rec
	}()
	select {
	case <-fallbacks:
	case <-time.After(10 * time.Second):
		t.Fatal("no fallback")
	}
	// The owner ran `deck-secret set` himself.
	e.preset(t, "conductor/GH_TOKEN_DECKBOX", testSecret)
	select {
	case got := <-done:
		if got.State != StateStored || got.AnsweredBy != "direct" {
			t.Fatalf("Ask = %+v, want stored by direct", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("direct set not detected")
	}
	if len(fallbacks) != 0 {
		t.Fatal("fallback printed more than once")
	}
}

func waitPending(t *testing.T, e *testEnv, sessionID string) Record {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		list, _ := List(e.profile)
		for _, r := range list {
			if r.SessionID == sessionID && r.AskEventID != "" {
				return r
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no pending request for %s", sessionID)
	return Record{}
}

// deadPID returns the pid of a process that has exited and been reaped.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}
