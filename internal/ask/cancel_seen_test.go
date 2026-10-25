package ask

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

// Security review 2: `ask cancel --by app` without a nonce let anyone end
// any request. A UI cancel now needs the nonce that UI announced; the
// asker's own cancel is in process only.
func TestAskCancelByUIRequiresNonce(t *testing.T) {
	e := newTestEnv(t)
	rec := mustCreate(t, e, secretRequest("s1"))
	for _, by := range []string{"app", "tui", "terminal"} {
		if _, err := Cancel(e.profile, rec.RequestID, by, ""); !errors.Is(err, ErrNonceRequired) {
			t.Fatalf("Cancel by %s without a nonce = %v, want ErrNonceRequired", by, err)
		}
	}
	if _, err := Cancel(e.profile, rec.RequestID, "asker", ""); err == nil {
		t.Fatal("Cancel by asker accepted from outside the asker")
	}
	if got, _ := Show(e.profile, rec.RequestID); got.State != StatePending {
		t.Fatalf("state %s after refused cancels, want pending", got.State)
	}
	if acks := busFrames(t, e.profile, events.KindMacappAskAck); len(acks) != 0 {
		t.Fatalf("refused cancels published %d acks", len(acks))
	}
}

// The app may cancel a request it never showed (muted with its box) with
// --unshown, only while no UI recorded a seen for it; the record says so.
func TestAskCancelUnshown(t *testing.T) {
	e := newTestEnv(t)
	rec := mustCreate(t, e, secretRequest("s1"))
	res, err := CancelUnshown(e.profile, rec.RequestID, "app")
	if err != nil || res.Status != StateCancelled {
		t.Fatalf("CancelUnshown = %+v %v", res, err)
	}
	got, _ := Show(e.profile, rec.RequestID)
	if got.State != StateCancelled || got.AnsweredBy != "app" || !got.CancelledUnshown {
		t.Fatalf("record after CancelUnshown: state %s by %s unshown %v", got.State, got.AnsweredBy, got.CancelledUnshown)
	}

	shown := mustCreate(t, e, secretRequest("s2"))
	if _, err := MarkSeen(e.profile, shown.RequestID, "tui"); err != nil {
		t.Fatal(err)
	}
	if _, err := CancelUnshown(e.profile, shown.RequestID, "app"); !errors.Is(err, ErrAlreadyShown) {
		t.Fatalf("CancelUnshown of a shown request = %v, want ErrAlreadyShown", err)
	}
	if got, _ := Show(e.profile, shown.RequestID); got.State != StatePending {
		t.Fatalf("state %s, want pending", got.State)
	}
	if _, err := CancelUnshown(e.profile, shown.RequestID, "nobody"); err == nil {
		t.Fatal("CancelUnshown by a non-UI accepted")
	}
}

// A seen (forged over the ungated events publish, or a dialog that never
// answers) holds the terminal fallback back only for SeenHold: then the
// asker prints it anyway, so the request does not time out silently.
func TestAskForgedAppSeenHoldsFallbackOnlyForSeenHold(t *testing.T) {
	e := newTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fallbacks := make(chan Record, 4)
	done := make(chan Record, 1)
	req := secretRequest("s1")
	req.Replace = false
	go func() {
		rec, _ := Ask(ctx, e.profile, req, AwaitOptions{FallbackAfter: 100 * time.Millisecond, OnFallback: func(r Record) { fallbacks <- r }})
		done <- rec
	}()
	rec := waitPending(t, e, "s1")
	bus := events.OpenProfile(e.profile)
	bus.Publish(events.KindMacappAskSeen, "s1", map[string]string{"request_id": rec.RequestID, "ask_event_id": rec.AskEventID, "nonce": "0123456789abcdef", "by": "app"})
	bus.Flush(5 * time.Second)
	bus.Close()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got, _ := Show(e.profile, rec.RequestID); len(got.SeenBy) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the asker never recorded the seen frame")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-fallbacks:
		t.Fatal("fallback printed while a fresh seen holds it")
	case <-time.After(600 * time.Millisecond):
	}
	e.clock.Advance(SeenHold + time.Second)
	select {
	case <-fallbacks:
	case <-time.After(10 * time.Second):
		t.Fatal("no fallback after the seen went stale with no deliver or cancel: the request would time out silently")
	}
	cancel()
	<-done
}

func TestAskAppShowing(t *testing.T) {
	at := time.Date(2026, 10, 24, 15, 0, 0, 0, time.UTC)
	r := Record{State: StatePending, Seen: []Seen{{By: "app", At: at}}}
	if !AppShowing(r, at.Add(SeenHold-time.Second)) {
		t.Fatal("a fresh app seen does not count")
	}
	if AppShowing(r, at.Add(SeenHold)) {
		t.Fatal("an app seen older than SeenHold still hides the request")
	}
	if AppShowing(Record{State: StatePending, Seen: []Seen{{By: "tui", At: at}}}, at) {
		t.Fatal("a tui seen counts as the app")
	}
}
