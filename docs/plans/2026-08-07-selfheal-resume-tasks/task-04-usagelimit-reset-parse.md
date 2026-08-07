# Task 04 — usage-limit reset parsing → `NotBefore`

tier: strong
depends on: nothing
parallel with: task 02 (disjoint files — task 02 touches only `internal/selfheal/`)
worktree: `/Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume` (branch `feature/selfheal-auto-resume`)

Use absolute paths under that worktree for every Read/Edit/Write, and
`git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume` for
every git command. Never run `git stash`, `git checkout`, `git switch`, or
`git reset`; never edit the root checkout at `/Users/doozyx/DoozyX/agent-deck`.

---

## Design extracts (verbatim from the approved design)

> ### 1.2 Usage limit
>
> Field evidence, same day, from the operator's transcripts:
>
> ```json
> {"type":"assistant","isSidechain":false,
>  "content":[{"type":"text","text":"You've hit your session limit · resets 6:10pm (Europe/Skopje)"}],
>  "error":"rate_limit","apiErrorStatus":429,"timestamp":"2026-08-07T14:23:13.812Z"}
> ```
>
> This is already detected. `internal/session/usagelimit.go` keys on exactly the
> `apiErrorStatus: 429` + `error: "rate_limit"` pair, and `isSidechain: false`
> places the record in the main conversation where the backward scanner looks. The
> substate `usage-limit` is published today and **has no consumer**.
>
> Note the shape: `Agent terminated early due to an API error: …` means a
> *subagent* hit the limit. Resuming the parent does not restore the subagent's
> work; the parent must re-dispatch it.

> ### D4 — Parse to schedule, probe to confirm
>
> `usagelimit.go` states the constraint directly: *"prefer a revalidation signal
> over parsing the prose"*, deferred until *"a consumer starts acting on the
> verdict"*. This design is that consumer.
>
> Blind probing alone is structurally unworkable: the per-session cap is 2 per 6
> hours, so a session would get two attempts across a five-hour window and then
> sit until the cap rolled. The caps force a scheduled wake.
>
> So the reset string sets *when to try*, and the observed outcome remains the
> authority:
>
> ```
> record 2026-08-07T14:23:13Z + "resets 6:10pm (Europe/Skopje)"
>   → NotBefore = 16:10Z          (next occurrence of that wall time in that zone,
>                                  strictly after the record timestamp)
>   → at 16:10Z: send the resume prompt
>        turn completes           → healed, substate clears itself
>        fresh 429 record appears → NotBefore += 20m, rearm
> ```
>
> Correctness never depends on the parse. If the string is absent, unparseable, or
> the wording drifts, `NotBefore` falls back to `record.timestamp + 20m` and the
> session recovers by retry — slower, never wrong. The zone is an IANA name
> (`Europe/Skopje`), so `time.LoadLocation` resolves it; a zone that fails to load
> takes the same fallback.
>
> `latestAssistantTurnIsRateLimited` currently returns a bare bool and discards
> both the text and the record timestamp. It must return them so the caller can
> compute `NotBefore`.
>
> **Secondary benefit:** a parsed reset also bounds belief better than
> `usageLimitMaxAge`. That constant is documented as clearing early for
> longer-than-5h (weekly) limits, reporting such a session as unremarkable. With a
> parsed reset in hand, belief can extend to it, closing the case where a weekly
> limit would never be resumed.

> ## 6. Verification
>
> **Reset parsing (unit, table).** `"resets 6:10pm (Europe/Skopje)"` against a
> 14:23Z record yields 16:10Z. Day rollover: a wall time earlier than the record's
> resolves to the next day. Unknown zone, absent string, and drifted wording each
> fall back to `recordTS + 20m`. A fresh 429 after an attempt rearms `NotBefore`.

---

## Scope note

This task computes and memoises `NotBefore`. It does **not** change
`usageLimitMaxAge`, and it does **not** extend belief to the parsed reset — that
is the design's "secondary benefit", explicitly a benefit rather than a
requirement, and widening the belief window is a behaviour change to a shipped
detector that this feature does not need. Leave `usageLimitMaxAge` alone.

## Acceptance criteria

1. `latestAssistantTurnIsRateLimited(path string, now time.Time) (limited bool, text string, recordTS time.Time, ok bool)`
   — the single canonical signature; the old 2-value form is gone and all seven
   existing test call sites are updated.
2. `parseUsageLimitReset(text string, recordTS time.Time) (time.Time, bool)` handles
   `resets 6:10pm (Europe/Skopje)`, `resets 6pm (UTC)`, day rollover, unknown zone.
3. `usageLimitNotBefore(text string, recordTS, prevNotBefore time.Time) time.Time`
   never returns the zero time for a real record.
4. `(*Instance).UsageLimitNotBefore() time.Time` returns the memoised schedule,
   or zero when the instance is not currently usage-limited.
5. The memo clears on session rebind exactly like `usageLimitedCached`.
6. Extracting the text does **not** break the existing detector: a record whose
   `message.content` is a plain JSON string still unmarshals and still classifies.
7. `go test ./internal/session/ -run UsageLimit -v` green.

## Edits

### 1. `internal/session/usagelimit.go` — constants

Add after `usageLimitMaxAge` (line 70):

```go
// usageLimitResetBackoff is how long to wait when the reset moment is not known
// or is not to be trusted.
//
// It is the fallback in BOTH directions. If the rejection carries no reset
// string, an unparseable one, or a zone time.LoadLocation cannot resolve, the
// schedule becomes recordTS + this — slower than a parse, never wrong, because
// the observed outcome (turn completes / fresh 429) stays the authority. And
// when a retry made at the parsed moment is itself rejected, the parse
// demonstrably described a window that did not open, so the next attempt backs
// off by this rather than trusting the same string again.
//
// 20 minutes because the per-session cap is 2 recoveries per 6 hours: a shorter
// step burns both attempts inside the first hour of a multi-hour window and then
// the session sits until the cap rolls.
const usageLimitResetBackoff = 20 * time.Minute

// usageLimitResetPattern extracts the reset moment Claude renders alongside a
// quota rejection: "You've hit your session limit · resets 6:10pm (Europe/Skopje)".
//
// Deliberately narrow. The minutes are optional ("resets 6pm (UTC)"), the zone is
// whatever sits in the parentheses, and nothing else in the sentence is relied
// on — the wording around it has drifted before and the fallback exists precisely
// so drift costs 20 minutes rather than correctness.
var usageLimitResetPattern = regexp.MustCompile(`(?i)resets\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm)\s*\(([^)]+)\)`)
```

Add `"regexp"` to the import block.

### 2. `internal/session/usagelimit.go` — record text extraction

Replace the `transcriptRecord` struct (lines 137-148) with:

```go
// transcriptRecord is the subset of a transcript line this detector reads.
type transcriptRecord struct {
	Type              string `json:"type"`
	IsSidechain       bool   `json:"isSidechain"`
	IsAPIErrorMessage bool   `json:"isApiErrorMessage"`
	APIErrorStatus    int    `json:"apiErrorStatus"`
	Error             string `json:"error"`
	Timestamp         string `json:"timestamp"`
	// Content is held as RawMessage rather than a typed shape because the
	// transcript carries BOTH a plain string and a [{type,text}] array in this
	// position depending on the record. Decoding into a concrete type would fail
	// the whole record's Unmarshal on the other shape and silently un-detect
	// every rejection written that way — the same dual-shape reality the Role
	// fallback below already accommodates.
	Content json.RawMessage `json:"content"`
	Message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// contentBlock is the array element shape of a transcript content field.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// text returns the human-facing text of this record, joining the text blocks
// when the content is an array. Empty when there is nothing readable — an absent
// string is one of the three cases the reset fallback exists for, so it is not an
// error condition.
//
// Top-level content is checked before message.content: the 2026-08-07 field
// sample carries the rejection prose at the top level, next to apiErrorStatus.
func (r transcriptRecord) text() string {
	if s := decodeTranscriptText(r.Content); s != "" {
		return s
	}
	return decodeTranscriptText(r.Message.Content)
}

// decodeTranscriptText reads either shape a content field takes.
func decodeTranscriptText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if t := strings.TrimSpace(b.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

// recordTime returns the record's parsed timestamp. The zero time means absent or
// unparseable — the same condition `fresh` already refuses to trust.
func (r transcriptRecord) recordTime() time.Time {
	ts, err := time.Parse(time.RFC3339, strings.TrimSpace(r.Timestamp))
	if err != nil {
		return time.Time{}
	}
	return ts
}
```

### 3. `internal/session/usagelimit.go` — the reset parser

Add immediately after `recordTime`:

```go
// parseUsageLimitReset resolves the rejection's rendered reset string to an
// absolute UTC moment: the NEXT occurrence of that wall time in that zone,
// strictly after the record's own timestamp.
//
// "Strictly after" is what makes the day rollover correct without a date in the
// string: a rejection stamped 22:00 local that says "resets 6:10pm" is talking
// about tomorrow evening, not twenty-two hours ago.
//
// ok is false for an absent string, a shape the pattern does not match, or a zone
// time.LoadLocation cannot resolve. Every one of those takes the caller's
// recordTS + usageLimitResetBackoff path instead — slower, never wrong.
func parseUsageLimitReset(text string, recordTS time.Time) (time.Time, bool) {
	if recordTS.IsZero() {
		return time.Time{}, false
	}
	m := usageLimitResetPattern.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}, false
	}
	hour, err := strconv.Atoi(m[1])
	if err != nil || hour < 1 || hour > 12 {
		return time.Time{}, false
	}
	minute := 0
	if m[2] != "" {
		if minute, err = strconv.Atoi(m[2]); err != nil || minute > 59 {
			return time.Time{}, false
		}
	}
	// 12-hour clock: 12am is hour 0, 12pm is hour 12, everything else adds 12 in
	// the afternoon.
	if strings.EqualFold(m[3], "pm") {
		if hour != 12 {
			hour += 12
		}
	} else if hour == 12 {
		hour = 0
	}
	loc, err := time.LoadLocation(strings.TrimSpace(m[4]))
	if err != nil {
		return time.Time{}, false
	}
	local := recordTS.In(loc)
	reset := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	if !reset.After(recordTS) {
		reset = reset.AddDate(0, 0, 1)
	}
	return reset.UTC(), true
}

// usageLimitNotBefore is when a resume may next be attempted for this rejection.
//
// prevNotBefore is the schedule already in force (zero when there is none). A
// rejection stamped at or after it means the scheduled retry ALREADY HAPPENED and
// was rejected again: the reset string in that record describes a window that
// demonstrably did not open, so back off by a fixed step rather than trusting the
// same prose a second time. That is the rearm the design calls for, and it is
// what stops a stale "resets 6:10pm" from resolving to tomorrow evening and
// parking the session for 24 hours.
//
// Otherwise the parse sets the schedule, with recordTS + backoff whenever the
// string is absent, unparseable, or carries a zone that will not load.
func usageLimitNotBefore(text string, recordTS, prevNotBefore time.Time) time.Time {
	if recordTS.IsZero() {
		return time.Time{}
	}
	if !prevNotBefore.IsZero() && !recordTS.Before(prevNotBefore) {
		return recordTS.Add(usageLimitResetBackoff)
	}
	if reset, ok := parseUsageLimitReset(text, recordTS); ok {
		return reset
	}
	return recordTS.Add(usageLimitResetBackoff)
}
```

Add `"strconv"` to the import block.

### 4. `internal/session/usagelimit.go` — widen the scanner's return

Replace the doc comment and signature of `latestAssistantTurnIsRateLimited`
(lines 186-193) with:

```go
// latestAssistantTurnIsRateLimited reports whether the MOST RECENT
// main-conversation assistant turn at path is a quota rejection recent enough to
// still be believed, plus whether a verdict could be formed at all.
//
// text and recordTS carry the deciding record's rendered prose and its own
// timestamp. They used to be discarded; the resume scheduler needs both, because
// the reset moment is only derivable from the prose and only meaningful relative
// to the record that carried it. Both are zero-valued when limited is false.
//
// ok is false when no assistant turn was found within the escalation bound.
// Callers must treat that as "unknown", never as "fine" — silence being mistaken
// for health is the failure this whole detector exists to stop.
func latestAssistantTurnIsRateLimited(path string, now time.Time) (limited bool, text string, recordTS time.Time, ok bool) {
```

Every bare `return false, false` inside that function becomes
`return false, "", time.Time{}, false`. There are five of them, at (original
line numbers) 196, 201, 208, 281 and 299.

Replace the `consider` closure (lines 233-269) with:

```go
	// consider returns (limited, text, recordTS, ok, decided).
	consider := func(start, end int64) (bool, string, time.Time, bool, bool) {
		if end <= start {
			return false, "", time.Time{}, false, false
		}
		if end-start > usageLimitMaxLineBytes {
			if usageLimitScanObserver != nil {
				usageLimitScanObserver(usageLimitScanRead{Kind: "line-skipped", Start: start, End: end, Bytes: 0})
			}
			// A line this large is a compaction/file-history snapshot, not a turn
			// this detector can act on. Skipped WITHOUT reading it: the whole point
			// of the bound is not to pull megabytes onto a status path.
			return false, "", time.Time{}, false, false
		}
		if usageLimitScanObserver != nil {
			usageLimitScanObserver(usageLimitScanRead{Kind: "line", Start: start, End: end, Bytes: end - start})
		}
		line := make([]byte, end-start)
		if _, err := f.ReadAt(line, start); err != nil && err != io.EOF {
			return false, "", time.Time{}, false, false
		}
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" {
			return false, "", time.Time{}, false, false
		}
		var rec transcriptRecord
		if err := json.Unmarshal([]byte(trimmed), &rec); err != nil {
			return false, "", time.Time{}, false, false
		}
		if !rec.isAssistantTurn() {
			return false, "", time.Time{}, false, false
		}
		if !rec.isRateLimitRejection() {
			return false, "", time.Time{}, true, true
		}
		// A rejection older than the window is no longer evidence about now.
		if !rec.fresh(now, usageLimitMaxAge) {
			return false, "", time.Time{}, true, true
		}
		return true, rec.text(), rec.recordTime(), true, true
	}
```

Replace both `consider` call sites (original lines 287 and 295) with:

```go
			if lim, txt, ts, o, decided := consider(start+i+1, lineEnd); decided {
				return lim, txt, ts, o
			}
```
and
```go
	if lim, txt, ts, o, decided := consider(0, lineEnd); decided {
		return lim, txt, ts, o
	}
```

Replace the final `return false, false` (line 299) with:

```go
	// The whole file was read and holds no main-conversation assistant turn.
	return false, "", time.Time{}, false
```

### 5. `internal/session/usagelimit.go` — memo the schedule

In `usageLimited()`, the rebind-discard branch (lines 402-406) becomes:

```go
	if i.usageLimitSessionID != sessionID {
		i.usageLimitSessionID = sessionID
		i.usageLimitedCached = false
		i.usageLimitNotBeforeCached = time.Time{}
		i.lastUsageLimitScanAt = time.Time{}
	}
```

The scan call (line 434) becomes:

```go
	limited, text, recordTS, ok := latestAssistantTurnIsRateLimited(path, time.Now())
```

The publish switch (lines 451-465) becomes:

```go
	live := strings.TrimSpace(i.ClaudeSessionID)
	switch {
	case live != sessionID:
		// Rebound while this read was in flight. There is no verdict for the new
		// session, and inheriting one is the bug.
		limited = false
	case usageLimitPublishable(i.usageLimitScanGen, gen, live, sessionID):
		i.usageLimitedCached = limited
		if limited {
			// Read the PREVIOUS schedule under the same lock that publishes the new
			// one: the rearm rule is "did the retry we scheduled already happen and
			// get rejected again", which is only answerable against the value in
			// force when this record was written.
			i.usageLimitNotBeforeCached = usageLimitNotBefore(text, recordTS, i.usageLimitNotBeforeCached)
		} else {
			i.usageLimitNotBeforeCached = time.Time{}
		}
	default:
		// A newer claim for the same live session landed while this read was in
		// flight, so this result is stale. Report the memo through the SAME
		// three-way invariant: reading usageLimitedCached directly here was
		// identity-blind under A→B→A, where live and scan both read "A" while the
		// memo holds B's verdict.
		limited = i.usageLimitVerdictForLocked(sessionID)
	}
	i.mu.Unlock()
```

Append the accessor at the end of the file:

```go
// UsageLimitNotBefore returns the moment a resume may next be attempted for this
// instance's CURRENT usage-limit verdict, or the zero time when the instance is
// not usage-limited.
//
// It reads the memo only. Call usageLimited() first (which the substate path
// already does) so the memo reflects the current transcript; calling this alone
// on a cold instance correctly reports "no schedule" rather than scheduling a
// resume from nothing.
func (i *Instance) UsageLimitNotBefore() time.Time {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if !i.usageLimitedCached {
		return time.Time{}
	}
	return i.usageLimitNotBeforeCached
}
```

### 6. `internal/session/instance.go` — the memo field

In the `Instance` struct, immediately after `usageLimitScanGen    uint64`
(line 275), add:

```go
	// usageLimitNotBeforeCached is when a resume may next be attempted for the
	// memoised rejection: the reset moment parsed out of the rejection's own
	// prose, or a fixed backoff when that prose is absent, unparseable or
	// describes a window that demonstrably did not open. Keyed by the same
	// usageLimitSessionID as the verdict and discarded with it on a rebind, so a
	// new conversation can never inherit the old one's schedule.
	usageLimitNotBeforeCached time.Time
```

### 7. `internal/session/usagelimit_test.go` — update the seven call sites

Mechanical. Apply exactly (`grep -c 'latestAssistantTurnIsRateLimited('
internal/session/usagelimit_test.go` returns 7 — the list below is complete):

- line 153: `limited, ok := latestAssistantTurnIsRateLimited(...)` → `limited, _, _, ok := latestAssistantTurnIsRateLimited(...)`
- line 163: same shape → `limited, _, _, ok := ...`
- line 211: `if limited, ok := latestAssistantTurnIsRateLimited(path, refNow); !limited || !ok {` → `if limited, _, _, ok := latestAssistantTurnIsRateLimited(path, refNow); !limited || !ok {`
- line 282: `limited, ok := ...` → `limited, _, _, ok := ...`
- line 314: `limited, ok := ...` → `limited, _, _, ok := ...`
- line 331: `_, _ = latestAssistantTurnIsRateLimited(path, refNow)` → `_, _, _, _ = latestAssistantTurnIsRateLimited(path, refNow)`
- line 378: `limited, ok := ...` → `limited, _, _, ok := ...`

(Line 155 and 411 are message/comment text, not call sites — leave them.)

Find any the line numbers missed with:
```sh
grep -n 'latestAssistantTurnIsRateLimited(' internal/session/usagelimit_test.go
```
and confirm every hit destructures four values.

## Tests — new file `internal/session/usagelimit_reset_test.go`

```go
package session

import (
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad fixture time %q: %v", s, err)
	}
	return ts
}

// §6: "resets 6:10pm (Europe/Skopje)" against a 14:23Z record yields 16:10Z.
// Europe/Skopje is UTC+2 in August, so 18:10 local is 16:10Z the same day.
func TestParseUsageLimitReset_Table(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		record  string
		want    string
		wantOK  bool
	}{
		{
			name:   "field sample resolves same day",
			text:   "You've hit your session limit · resets 6:10pm (Europe/Skopje)",
			record: "2026-08-07T14:23:13Z",
			want:   "2026-08-07T16:10:00Z",
			wantOK: true,
		},
		{
			name:   "day rollover: wall time earlier than the record's",
			text:   "You've hit your session limit · resets 6:10pm (Europe/Skopje)",
			record: "2026-08-07T20:00:00Z", // 22:00 local — 18:10 already passed
			want:   "2026-08-08T16:10:00Z",
			wantOK: true,
		},
		{
			name:   "no minutes",
			text:   "resets 6pm (UTC)",
			record: "2026-08-07T14:23:13Z",
			want:   "2026-08-07T18:00:00Z",
			wantOK: true,
		},
		{
			name:   "midnight is 12am",
			text:   "resets 12am (UTC)",
			record: "2026-08-07T14:23:13Z",
			want:   "2026-08-08T00:00:00Z",
			wantOK: true,
		},
		{
			name:   "noon is 12pm",
			text:   "resets 12pm (UTC)",
			record: "2026-08-07T09:00:00Z",
			want:   "2026-08-07T12:00:00Z",
			wantOK: true,
		},
		{
			name:   "unknown zone does not parse",
			text:   "resets 6:10pm (Middle/Earth)",
			record: "2026-08-07T14:23:13Z",
			wantOK: false,
		},
		{
			name:   "absent reset string",
			text:   "You've hit your session limit.",
			record: "2026-08-07T14:23:13Z",
			wantOK: false,
		},
		{
			name:   "drifted wording without a zone",
			text:   "Your limit will lift at 6:10pm",
			record: "2026-08-07T14:23:13Z",
			wantOK: false,
		},
		{
			name:   "empty text",
			text:   "",
			record: "2026-08-07T14:23:13Z",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseUsageLimitReset(tt.text, mustTime(t, tt.record))
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (got %s)", ok, tt.wantOK, got)
			}
			if !tt.wantOK {
				return
			}
			want := mustTime(t, tt.want)
			if !got.Equal(want) {
				t.Fatalf("reset = %s, want %s", got.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
			}
		})
	}
}

// §6: unknown zone, absent string and drifted wording each fall back to
// recordTS + 20m.
func TestUsageLimitNotBefore_FallsBackToBackoff(t *testing.T) {
	record := mustTime(t, "2026-08-07T14:23:13Z")
	want := record.Add(20 * time.Minute)
	for _, text := range []string{
		"resets 6:10pm (Middle/Earth)",
		"You've hit your session limit.",
		"Your limit will lift at 6:10pm",
		"",
	} {
		got := usageLimitNotBefore(text, record, time.Time{})
		if !got.Equal(want) {
			t.Fatalf("text %q: NotBefore = %s, want %s", text, got.Format(time.RFC3339), want.Format(time.RFC3339))
		}
	}
}

// A parseable string schedules the parsed moment, not the backoff.
func TestUsageLimitNotBefore_UsesParsedReset(t *testing.T) {
	record := mustTime(t, "2026-08-07T14:23:13Z")
	got := usageLimitNotBefore("You've hit your session limit · resets 6:10pm (Europe/Skopje)", record, time.Time{})
	want := mustTime(t, "2026-08-07T16:10:00Z")
	if !got.Equal(want) {
		t.Fatalf("NotBefore = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// §6: a fresh 429 after an attempt rearms NotBefore.
//
// The retry fired at 16:10Z and was rejected again at 16:10:30Z. The reset string
// in that new record still reads "6:10pm", which as a bare parse would resolve to
// TOMORROW evening and park the session for a day. The rearm rule catches it: the
// record post-dates the schedule, so the schedule becomes record + 20m.
func TestUsageLimitNotBefore_FreshRejectionAfterAttempt_Rearms(t *testing.T) {
	prev := mustTime(t, "2026-08-07T16:10:00Z")
	record := mustTime(t, "2026-08-07T16:10:30Z")
	got := usageLimitNotBefore("You've hit your session limit · resets 6:10pm (Europe/Skopje)", record, prev)
	want := record.Add(20 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("rearm: NotBefore = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// A rejection that PREDATES the standing schedule is the same incident being
// re-read, not a new one: the schedule must not drift forward every poll.
func TestUsageLimitNotBefore_SameRecordReread_DoesNotDrift(t *testing.T) {
	record := mustTime(t, "2026-08-07T14:23:13Z")
	first := usageLimitNotBefore("resets 6:10pm (Europe/Skopje)", record, time.Time{})
	second := usageLimitNotBefore("resets 6:10pm (Europe/Skopje)", record, first)
	if !second.Equal(first) {
		t.Fatalf("re-reading the same record moved the schedule: %s -> %s",
			first.Format(time.RFC3339), second.Format(time.RFC3339))
	}
}

// A record with no usable timestamp yields no schedule at all rather than one
// anchored on the zero time (which would be permanently in the past).
func TestUsageLimitNotBefore_ZeroRecordTS_NoSchedule(t *testing.T) {
	if got := usageLimitNotBefore("resets 6:10pm (UTC)", time.Time{}, time.Time{}); !got.IsZero() {
		t.Fatalf("want the zero time, got %s", got.Format(time.RFC3339))
	}
}

// The text extractor must read BOTH transcript content shapes, and must not
// break the record's own unmarshal on either.
func TestTranscriptRecord_Text_BothContentShapes(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "top-level content array (2026-08-07 field sample)",
			line: `{"type":"assistant","isSidechain":false,"content":[{"type":"text","text":"You've hit your session limit · resets 6:10pm (Europe/Skopje)"}],"error":"rate_limit","apiErrorStatus":429,"isApiErrorMessage":true,"timestamp":"2026-08-07T14:23:13.812Z"}`,
			want: "You've hit your session limit · resets 6:10pm (Europe/Skopje)",
		},
		{
			name: "message.content as a plain string",
			line: `{"type":"assistant","message":{"role":"assistant","content":"resets 8:50pm (UTC)"},"error":"rate_limit","apiErrorStatus":429,"isApiErrorMessage":true,"timestamp":"2026-08-07T14:23:13.812Z"}`,
			want: "resets 8:50pm (UTC)",
		},
		{
			name: "message.content as an array",
			line: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"resets 9pm (UTC)"}]},"error":"rate_limit","apiErrorStatus":429,"isApiErrorMessage":true,"timestamp":"2026-08-07T14:23:13.812Z"}`,
			want: "resets 9pm (UTC)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rec transcriptRecord
			if err := jsonUnmarshalLine(t, tt.line, &rec); err != nil {
				t.Fatalf("the record must still unmarshal: %v", err)
			}
			if !rec.isAssistantTurn() {
				t.Fatal("the record must still classify as a main-conversation assistant turn")
			}
			if !rec.isRateLimitRejection() {
				t.Fatal("the record must still classify as a quota rejection")
			}
			if got := rec.text(); got != tt.want {
				t.Fatalf("text() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A tool-result content array with no text blocks reads as empty, not as garbage.
func TestTranscriptRecord_Text_NoTextBlocks(t *testing.T) {
	var rec transcriptRecord
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"x"}]},"timestamp":"2026-08-07T14:23:13.812Z"}`
	if err := jsonUnmarshalLine(t, line, &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := rec.text(); got != "" {
		t.Fatalf("want empty text, got %q", got)
	}
}
```

Add this helper at the bottom of the same new file:

```go
func jsonUnmarshalLine(t *testing.T, line string, v any) error {
	t.Helper()
	return json.Unmarshal([]byte(line), v)
}
```

and add `"encoding/json"` to the file's imports.

## Verification

```sh
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume
gofmt -l internal/session/usagelimit.go internal/session/usagelimit_test.go internal/session/usagelimit_reset_test.go internal/session/instance.go
```
Expected: **nothing** (empty).

```sh
go build ./... && go vet ./internal/session/
```
Expected: no output, exit 0.

```sh
go test ./internal/session/ -run 'UsageLimit|TranscriptRecord' -count=1 -v
```
Expected: `ok  	github.com/asheshgoplani/agent-deck/internal/session`. Every
pre-existing `TestUsageLimit*` still PASSes (the signature change must not alter
any verdict), plus the eight new ones. Run-specific sentinel:
`TestUsageLimitNotBefore_FreshRejectionAfterAttempt_Rearms` must appear as `--- PASS`.

Explicit non-regression check on the shipped detector — the pre-existing table
must be byte-for-byte unaffected:
```sh
go test ./internal/session/ -run 'TestLatestAssistantTurn|TestUsageLimited' -count=1 -v
```
Expected: all PASS.

Known sandbox flakes in `internal/session`: tests requiring `python3` or JSONL
fixture generation, and anything spawning tmux. The `-run` patterns above avoid
them. If the broad suite is run and something outside `UsageLimit`/
`TranscriptRecord` fails, confirm it also fails on the base commit before
attributing it to this change; record the finding rather than "fixing" it.

## Commit

```sh
git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume add \
  internal/session/usagelimit.go internal/session/usagelimit_test.go \
  internal/session/usagelimit_reset_test.go internal/session/instance.go
git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume commit -m "feat(session): derive a usage-limit resume schedule from the rejection

usagelimit.go deferred parsing the reset prose until a consumer acted on the
verdict. Self-heal resume is that consumer, and blind probing cannot substitute:
the per-session cap is 2 recoveries per 6 hours, so a session would get two
attempts across a five-hour window and then sit until the cap rolled.

latestAssistantTurnIsRateLimited stops discarding the deciding record's prose and
timestamp, and NotBefore resolves the rendered reset to the next occurrence of
that wall time in that zone strictly after the record. Correctness never depends
on the parse: an absent, unparseable or unloadable-zone string falls back to
record + 20m, and a retry that is itself rejected backs off by the same step
instead of trusting the same prose twice.

Content is held as RawMessage because the transcript carries the prose as both a
string and a block array; decoding into either concrete shape would have failed
the whole record's unmarshal on the other and silently un-detected it."
```

## Interfaces

### consumes
- `internal/session/usagelimit.go`: `transcriptRecord`, `(transcriptRecord).isAssistantTurn()`, `(transcriptRecord).isRateLimitRejection()`, `(transcriptRecord).fresh(now, maxAge)`, `usageLimitMaxAge`, `usageLimitScanChunkBytes`, `usageLimitMaxLineBytes`, `usageLimitScanObserver`, `usageLimitScanRead`, `usageLimitPublishable`, `(*Instance).usageLimitVerdictForLocked`, `(*Instance).usageLimited()`, `locateHandoffTranscript`, `transcriptBelongsToSession`
- `internal/session/instance.go`: `Instance` struct fields `mu sync.RWMutex`, `ClaudeSessionID`, `usageLimitedCached`, `usageLimitSessionID`, `usageLimitScanGen`, `lastUsageLimitScanAt`

### produces
- `internal/session/usagelimit.go`: **changed signature** `func latestAssistantTurnIsRateLimited(path string, now time.Time) (limited bool, text string, recordTS time.Time, ok bool)`
- `internal/session/usagelimit.go`: `func parseUsageLimitReset(text string, recordTS time.Time) (time.Time, bool)`
- `internal/session/usagelimit.go`: `func usageLimitNotBefore(text string, recordTS, prevNotBefore time.Time) time.Time`
- `internal/session/usagelimit.go`: `const usageLimitResetBackoff = 20 * time.Minute`
- `internal/session/usagelimit.go`: `func (i *Instance) UsageLimitNotBefore() time.Time` — **task 06 calls this immediately after `inst.usageLimited()` returns true**; it returns the zero time when the instance is not currently usage-limited
- `internal/session/usagelimit.go`: `func (r transcriptRecord) text() string`, `func (r transcriptRecord) recordTime() time.Time`
- `internal/session/instance.go`: `Instance.usageLimitNotBeforeCached time.Time` (unexported; only `UsageLimitNotBefore()` reads it)

## Record (append-only)

### 2026-08-07 — implemented (COMPLETE)

- Files touched: `internal/session/usagelimit.go`,
  `internal/session/usagelimit_reset_test.go` (new),
  `internal/session/usagelimit_test.go`, `internal/session/instance.go`.
- Implemented exactly as written; no deviations. All seven edits applied:
  constants + regexp, `transcriptRecord` dual-shape `Content json.RawMessage` +
  `contentBlock` + `text()` + `decodeTranscriptText()` + `recordTime()`,
  `parseUsageLimitReset`, `usageLimitNotBefore`, the widened 4-value
  `latestAssistantTurnIsRateLimited` (all five bare returns + the `consider`
  closure + both call sites + the final return), the memo wiring in
  `usageLimited()` (rebind discard, scan destructure, publish switch),
  `(*Instance).UsageLimitNotBefore()`, and the
  `Instance.usageLimitNotBeforeCached` field.
- All seven `usagelimit_test.go` call sites updated to destructure four values
  (lines 153, 163, 211, 282, 314, 331, 378); `grep -n` confirms no 2-value form
  remains.
- Verification:
  `gofmt -l internal/session/usagelimit.go internal/session/usagelimit_test.go
  internal/session/usagelimit_reset_test.go internal/session/instance.go` → empty.
  `go build ./...` → exit 0.
  `go vet ./internal/session/` → clean apart from the pre-existing
  `issue1225_wake_nudge_wiring_test.go:217 range var c copies lock` noted in
  task 01's Record (untouched file, not introduced here).
  `go test ./internal/session/ -run 'UsageLimit|TranscriptRecord' -count=1 -v`
  → EXIT=0, `ok`, 37 `--- PASS`, 0 FAIL; sentinel
  `TestUsageLimitNotBefore_FreshRejectionAfterAttempt_Rearms` PASS.
  `go test ./internal/session/ -run 'TestLatestAssistantTurn|TestUsageLimited'
  -count=1 -v` → 29 `--- PASS`, `ok` (the signature change altered no verdict).
- Scope note honoured: `usageLimitMaxAge` is unchanged and belief was NOT
  extended to the parsed reset.
- No concerns.
