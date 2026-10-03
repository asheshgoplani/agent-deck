package comms

import (
	"fmt"
	"strings"
	"time"
)

// Names resolves a session id to a short display name; nil falls back to
// the id. Consumers pass the registry's id -> title map.
type Names map[string]string

func (n Names) of(id string) string {
	if n != nil {
		if t, ok := n[id]; ok && t != "" {
			return t
		}
	}
	return id
}

// ShortID is the tail of a ULID a human can quote: the last 6 characters
// (the random part), which is what a consumer prints next to each record so
// a repeat is skippable at a glance.
func ShortID(id string) string {
	if len(id) <= 6 {
		return id
	}
	return id[len(id)-6:]
}

// Line renders one record as the single line a consumer injects or types:
//
//	[urgent] worker-a (codex) #ABC123: <text>
//	[done ok] worker-b (claude) #DEF456: <summary>
//	[info] worker-c (gemini) #GHI789: <text>
//
// Text is flattened to one line; the record id tail is always present so a
// redelivered record is recognisable.
func Line(r Record, names Names) string {
	var tag string
	switch {
	case r.Done != "":
		tag = "done " + r.Done
	case r.Kind == KindError, r.Kind == KindStatus, r.Kind == KindSend, r.Kind == KindWake, r.Kind == KindHuman:
		tag = r.Kind
	case r.Tier != "":
		tag = r.Tier
	default:
		tag = "urgent"
	}
	who := names.of(r.From)
	if r.Tool != "" {
		who += " (" + r.Tool + ")"
	}
	if r.Origin != "" {
		who = r.Origin + ":" + who
	}
	body := r.Text
	switch {
	case r.Done != "" && r.Summary != "":
		body = r.Summary
	case r.Kind == KindError && r.Err != "":
		body = r.Err
	case r.Kind == KindStatus && body == "":
		body = r.State
	}
	body = strings.Join(strings.Fields(stripControl(body)), " ")
	if r.Q {
		tag += " ?"
	}
	line := fmt.Sprintf("[%s] %s #%s", tag, who, ShortID(r.ID))
	if body != "" {
		line += ": " + body
	}
	return line
}

// stripControl drops C0/C1 control characters (keeping whitespace, which
// Fields folds) so a line typed into a pane carries no escape sequences.
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r == ' ' {
			return r
		}
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}

// Digest renders several records as the block a consumer injects at prompt
// time or bundles into one typed nudge: a header with counts, one Line per
// record, oldest first. Empty input renders "".
func Digest(records []Record, names Names) string {
	if len(records) == 0 {
		return ""
	}
	urgent := 0
	for _, r := range records {
		if r.IsUrgent() {
			urgent++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[agent-deck msg] %d pending (%d urgent); ack with `agent-deck msg ack`\n", len(records), urgent)
	for _, r := range records {
		b.WriteString(Line(r, names))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// Age renders how long ago a record was committed, for human listings.
func Age(r Record, now time.Time) string {
	t := time.UnixMilli(r.TRecord)
	if r.TRecord == 0 {
		t = IDTime(r.ID)
	}
	if t.IsZero() {
		return "?"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
