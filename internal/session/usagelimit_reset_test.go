package session

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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
		name   string
		text   string
		record string
		want   string
		wantOK bool
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

// The excerpt this logs is a Claude-rendered rejection string, which routinely
// carries "…" and "·" — multi-byte runes. A byte-index cut lands mid-rune and
// writes invalid UTF-8 into the log line, so the truncation must respect rune
// boundaries.
func TestTruncateForLog_CutsOnARuneBoundary(t *testing.T) {
	// "·" is 2 bytes; a 40-char run of it puts a rune boundary at every EVEN byte
	// index, so every odd max lands mid-rune.
	multi := strings.Repeat("·", 40)
	for max := 1; max <= 60; max++ {
		got := truncateForLog(multi, max)
		if !utf8.ValidString(got) {
			t.Fatalf("max=%d produced invalid UTF-8: %q", max, got)
		}
		if len(got) > max+len("…") {
			t.Fatalf("max=%d: result is %d bytes, over the bound", max, len(got))
		}
	}

	// The same for a 3-byte rune, and for a string that needs no cut at all.
	if got := truncateForLog(strings.Repeat("…", 30), 20); !utf8.ValidString(got) {
		t.Fatalf("3-byte runes produced invalid UTF-8: %q", got)
	}
	if got := truncateForLog("  short · text  ", 200); got != "short · text" {
		t.Fatalf("an under-bound string must only be trimmed, got %q", got)
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

func jsonUnmarshalLine(t *testing.T, line string, v any) error {
	t.Helper()
	return json.Unmarshal([]byte(line), v)
}
