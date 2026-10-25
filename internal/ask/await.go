package ask

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

// DefaultFallbackAfter is how long the asker waits for a UI to mark the
// request seen before it prints the terminal fallback.
const DefaultFallbackAfter = 20 * time.Second

var (
	pollEvery       = 250 * time.Millisecond
	directPollEvery = 2 * time.Second
)

// AwaitOptions tune Ask's wait.
type AwaitOptions struct {
	// FallbackAfter (default DefaultFallbackAfter, real time) is the window
	// for a UI to mark the request seen.
	FallbackAfter time.Duration
	// OnFallback is called once when nothing marked the request seen in
	// time. It must print only facts of the record (the CLI prints the
	// `ask answer` command); for a secret that did not exist, Ask then polls
	// the vault so a direct `deck-secret set` completes the request.
	OnFallback func(Record)
	// NoFallback makes Ask cancel the request and return ErrNoUI instead.
	NoFallback bool
	// OnCreated is called once the request was accepted and written (for
	// the asker's replace warning), before it is announced.
	OnCreated func(Record)
}

// Ask creates the request, announces it (macapp.ask on the profile bus; a
// disabled bus leaves the TUI and terminal routes), and waits until it ends.
// It records the seen frames UIs publish (matching request, ask_event_id and
// session), marks it expired at its deadline and cancels it (by asker) when
// ctx ends. The returned record carries the answer for choice/text; the
// caller prints it and calls ClearAnswer. Refusals come back as errors.
func Ask(ctx context.Context, profile string, req NewRequest, opts AwaitOptions) (Record, error) {
	if opts.FallbackAfter <= 0 {
		opts.FallbackAfter = DefaultFallbackAfter
	}
	bus := events.OpenProfile(profile)
	defer bus.Close()
	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	var frames <-chan events.Frame
	if before := bus.Stats(); before.Enabled {
		if sub, err := bus.Subscribe(subCtx, before.Cursor); err == nil {
			frames = sub.Frames()
		}
	}
	rec, err := Create(profile, req)
	if err != nil {
		return Record{}, err
	}
	if opts.OnCreated != nil {
		opts.OnCreated(rec.public())
	}
	if id := publish(bus, profile, events.KindMacappAsk, rec.SessionID, announceData(rec)); id != "" {
		if updated, err := update(profile, rec.RequestID, func(r *Record) error { r.AskEventID = id; return nil }); err == nil {
			rec = updated
		}
		askLog.Debug("ask_announced", slog.String("request_id", rec.RequestID), slog.String("ask_event_id", id))
	}
	return await(ctx, profile, bus, frames, rec, opts)
}

func await(ctx context.Context, profile string, bus *events.Bus, frames <-chan events.Frame, rec Record, opts AwaitOptions) (Record, error) {
	start := time.Now()
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	fallbackDone := false
	var lastDirect time.Time
	for {
		select {
		case <-ctx.Done():
			if _, err := cancelByAsker(profile, rec.RequestID); err != nil && !errors.Is(err, ErrNotPending) && !errors.Is(err, ErrExpired) {
				return rec, err
			}
			return readAnswered(profile, rec.RequestID)
		case f, ok := <-frames:
			if !ok {
				frames = nil
				continue
			}
			noteSeenFrame(profile, rec, f)
		case <-ticker.C:
		}
		cur, err := readAnswered(profile, rec.RequestID)
		if err != nil {
			return rec, err
		}
		if cur.finished() {
			return cur, nil
		}
		if !now().Before(cur.ExpiresAt) {
			if done, err := updateFinal(profile, rec.RequestID, expireIfDue); errors.Is(err, ErrExpired) {
				publishAck(bus, done)
				askLog.Debug("ask_expired", slog.String("request_id", rec.RequestID))
			}
			continue // the next pass returns the final record
		}
		// A seen holds the fallback back only for SeenHold (a forged app
		// seen must not hide the request until it times out). --no-fallback
		// gives up only when nothing ever showed the request.
		if !fallbackDone && time.Since(start) >= opts.FallbackAfter &&
			((opts.NoFallback && len(cur.Seen) == 0) || (!opts.NoFallback && !seenHolding(cur, now()))) {
			fallbackDone = true
			if opts.NoFallback {
				if _, err := cancelByAsker(profile, rec.RequestID); err != nil && !errors.Is(err, ErrNotPending) {
					return cur, err
				}
				final, _ := readAnswered(profile, rec.RequestID)
				if final.State != StateCancelled {
					return final, nil // answered meanwhile
				}
				return final, ErrNoUI
			}
			if opts.OnFallback != nil {
				opts.OnFallback(cur.public())
			}
		}
		if fallbackDone && cur.Kind == KindSecret && !cur.Replace && time.Since(lastDirect) >= directPollEvery {
			lastDirect = time.Now()
			checkDirect(profile, bus, cur)
		}
	}
}

// noteSeenFrame records a macapp.ask.seen frame from a UI (the app over
// `events publish`) when it names this request, its announcement and its
// session. Frames are hints: Deliver still checks the nonce in the record.
func noteSeenFrame(profile string, rec Record, f events.Frame) {
	if f.Kind != events.KindMacappAskSeen || f.SessionID != rec.SessionID || rec.AskEventID == "" {
		return
	}
	var d seenData
	if json.Unmarshal(f.Data, &d) != nil || d.RequestID != rec.RequestID || d.AskEventID != rec.AskEventID {
		return
	}
	if _, err := recordSeen(profile, rec.RequestID, d.By, d.Nonce); err == nil {
		askLog.Debug("ask_seen_recorded", slog.String("request_id", rec.RequestID), slog.String("by", d.By))
	}
}

// checkDirect completes a secret request the owner stored himself with
// `deck-secret set` (offered by the terminal fallback when the name did not
// exist): answered_by direct, reported as unverified.
func checkDirect(profile string, bus *events.Bus, rec Record) {
	exists, err := vaultExists(rec.Name, rec.SessionID)
	if err != nil || !exists {
		return
	}
	done, err := update(profile, rec.RequestID, func(r *Record) error {
		if r.State != StatePending {
			return ErrNotPending
		}
		r.State, r.AnsweredBy, r.Replaced, r.FinishedAt = StateStored, "direct", "no", now().UTC()
		return nil
	})
	if err == nil {
		publishAck(bus, done)
	}
}
