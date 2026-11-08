// Package orders implements standing orders for a conductor: ORDERS.md holds
// one "## name" section per order, each with a cheap shell check: that runs
// on a schedule with no AI. When the check's output changes, the conductor is
// woken once with the change, the order's do: text and its ask-first list.
// When it does not change, nothing is sent and no tokens are spent.
//
// The file format is deliberately plain so the owner can edit it in vim and
// the conductor can write it when asked to "watch X":
//
//	## watch-main-ci
//	every: 15m
//	active: 08:00-22:00 Europe/Berlin
//	check: gh run list -b main -L 1 --json conclusion -q '.[0].conclusion'
//	do: if main is red, start one worker to fix it; tell me in one line
//	ask_first: merge, push, posting anywhere
//	max_wakes_per_day: 6
//	expires: 2026-12-31
//
// Any other line is a free note and is kept as written.
package orders

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // active: zones must resolve on hosts without zoneinfo
)

// FileName is the orders file in a conductor's directory.
const FileName = "ORDERS.md"

// Defaults for keys an order leaves out.
const (
	DefaultEvery          = 15 * time.Minute
	MinEvery              = time.Minute
	DefaultMaxWakesPerDay = 6
	CheckTimeout          = 60 * time.Second
	StatusActive          = "active"
	StatusPaused          = "paused"
)

// Order is one parsed "## name" section.
type Order struct {
	Name           string
	Every          time.Duration
	Active         string // "HH:MM-HH:MM [Zone]", "" = always
	Check          string
	Do             string
	AskFirst       string
	MaxWakesPerDay int
	Expires        string // YYYY-MM-DD, last day the order runs; "" = never
	Status         string // active | paused
}

var (
	headingRe = regexp.MustCompile(`^##\s+(\S+)\s*$`)
	keyRe     = regexp.MustCompile(`^([a-z_]+):\s*(.*)$`)
	nameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	everyRe   = regexp.MustCompile(`^(\d+)\s*([mhd])$`)
	activeRe  = regexp.MustCompile(`^(\d{1,2}):(\d{2})-(\d{1,2}):(\d{2})(?:\s+(\S+))?$`)
)

// knownKeys are the keys an order understands; anything else is a note.
var knownKeys = map[string]bool{
	"every": true, "active": true, "check": true, "do": true, "ask_first": true,
	"max_wakes_per_day": true, "expires": true, "status": true,
}

// ValidName reports whether s is a usable order name.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// Parse reads ORDERS.md. A section that does not parse is returned as an
// error naming it; the caller decides whether one bad order stops the rest.
func Parse(text string) ([]Order, []error) {
	var out []Order
	var errs []error
	for _, sec := range splitSections(text) {
		if sec.name == "" {
			continue
		}
		o, err := parseSection(sec)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, o)
	}
	return out, errs
}

func parseSection(sec section) (Order, error) {
	o := Order{Name: sec.name, Every: DefaultEvery, MaxWakesPerDay: DefaultMaxWakesPerDay, Status: StatusActive}
	if !ValidName(sec.name) {
		return o, fmt.Errorf("order %q: name must be lowercase letters, digits, - or _", sec.name)
	}
	for _, line := range sec.lines[1:] {
		m := keyRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || !knownKeys[m[1]] {
			continue
		}
		v := strings.TrimSpace(m[2])
		switch m[1] {
		case "every":
			d, err := ParseEvery(v)
			if err != nil {
				return o, fmt.Errorf("order %q: %w", sec.name, err)
			}
			o.Every = d
		case "active":
			if _, err := parseActive(v); err != nil {
				return o, fmt.Errorf("order %q: %w", sec.name, err)
			}
			o.Active = v
		case "check":
			o.Check = v
		case "do":
			o.Do = v
		case "ask_first":
			o.AskFirst = v
		case "max_wakes_per_day":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return o, fmt.Errorf("order %q: max_wakes_per_day must be a whole number of at least 1, got %q", sec.name, v)
			}
			o.MaxWakesPerDay = n
		case "expires":
			if _, err := time.Parse("2006-01-02", v); err != nil {
				return o, fmt.Errorf("order %q: expires must be YYYY-MM-DD, got %q", sec.name, v)
			}
			o.Expires = v
		case "status":
			if v != StatusActive && v != StatusPaused {
				return o, fmt.Errorf("order %q: status must be active or paused, got %q", sec.name, v)
			}
			o.Status = v
		}
	}
	if o.Check == "" {
		return o, fmt.Errorf("order %q: check: is required (a shell command whose output is watched)", sec.name)
	}
	return o, nil
}

// ParseEvery reads "15m", "2h" or "1d". The minimum is one minute, the tick's
// own period.
func ParseEvery(v string) (time.Duration, error) {
	m := everyRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0, fmt.Errorf("every must look like 15m, 2h or 1d, got %q", v)
	}
	n, _ := strconv.Atoi(m[1])
	unit := map[string]time.Duration{"m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}[m[2]]
	d := time.Duration(n) * unit
	if d < MinEvery {
		return 0, fmt.Errorf("every must be at least 1m, got %q", v)
	}
	return d, nil
}

type activeWindow struct {
	from, to int // minutes since midnight
	loc      *time.Location
}

func parseActive(v string) (activeWindow, error) {
	m := activeRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return activeWindow{}, fmt.Errorf("active must look like 08:00-22:00 [Europe/Berlin], got %q", v)
	}
	h1, _ := strconv.Atoi(m[1])
	m1, _ := strconv.Atoi(m[2])
	h2, _ := strconv.Atoi(m[3])
	m2, _ := strconv.Atoi(m[4])
	if h1 > 23 || h2 > 24 || m1 > 59 || m2 > 59 || (h2 == 24 && m2 != 0) {
		return activeWindow{}, fmt.Errorf("active has an invalid time: %q", v)
	}
	loc := time.Local
	if m[5] != "" {
		l, err := time.LoadLocation(m[5])
		if err != nil {
			return activeWindow{}, fmt.Errorf("active has an unknown time zone %q", m[5])
		}
		loc = l
	}
	return activeWindow{from: h1*60 + m1, to: h2*60 + m2, loc: loc}, nil
}

// InActiveHours reports whether now falls in the order's active window. A
// window that wraps midnight (22:00-06:00) is handled.
func (o Order) InActiveHours(now time.Time) bool {
	if o.Active == "" {
		return true
	}
	w, err := parseActive(o.Active)
	if err != nil {
		return true
	}
	t := now.In(w.loc)
	cur := t.Hour()*60 + t.Minute()
	if w.from <= w.to {
		return cur >= w.from && cur < w.to
	}
	return cur >= w.from || cur < w.to
}

// Expired reports whether the order's last day has passed, by the date in
// loc (the tick passes the zone the owner's days are counted in).
func (o Order) Expired(now time.Time, loc *time.Location) bool {
	return o.Expires != "" && now.In(loc).Format("2006-01-02") > o.Expires
}

// --- editing ORDERS.md in place (notes and unknown lines are kept) ---

type section struct {
	name  string // "" for the preamble before the first heading
	lines []string
}

func splitSections(text string) []section {
	lines := strings.Split(text, "\n")
	secs := []section{{}}
	for _, l := range lines {
		if m := headingRe.FindStringSubmatch(strings.TrimRight(l, " \t\r")); m != nil {
			secs = append(secs, section{name: m[1], lines: []string{l}})
			continue
		}
		cur := &secs[len(secs)-1]
		cur.lines = append(cur.lines, l)
	}
	return secs
}

func joinSections(secs []section) string {
	var parts []string
	for _, s := range secs {
		parts = append(parts, s.lines...)
	}
	return strings.Join(parts, "\n")
}

// Add appends a new order section. The order is validated first and a name
// already in the file is refused.
func Add(text string, o Order) (string, error) {
	if !ValidName(o.Name) {
		return "", fmt.Errorf("order name %q must be lowercase letters, digits, - or _", o.Name)
	}
	for _, s := range splitSections(text) {
		if s.name == o.Name {
			return "", fmt.Errorf("order %q already exists", o.Name)
		}
	}
	block := Format(o)
	if _, err := parseSection(section{name: o.Name, lines: strings.Split(block, "\n")}); err != nil {
		return "", err
	}
	text = strings.TrimRight(text, "\n")
	if text == "" {
		text = header
	}
	return text + "\n\n" + block + "\n", nil
}

const header = "# Standing orders\n\n" +
	"One `## name` section per order. `agent-deck orders tick` runs each due `check:`\n" +
	"(shell, 60 s timeout, no AI). Same output as last time: nothing is sent.\n" +
	"Changed: the conductor gets one message with the change, `do:` and `ask_first:`."

// Format renders one order as an ORDERS.md section.
func Format(o Order) string {
	lines := []string{"## " + o.Name}
	if o.Every > 0 {
		lines = append(lines, "every: "+formatEvery(o.Every))
	}
	if o.Active != "" {
		lines = append(lines, "active: "+o.Active)
	}
	lines = append(lines, "check: "+oneLine(o.Check))
	if o.Do != "" {
		lines = append(lines, "do: "+oneLine(o.Do))
	}
	if o.AskFirst != "" {
		lines = append(lines, "ask_first: "+oneLine(o.AskFirst))
	}
	if o.MaxWakesPerDay > 0 {
		lines = append(lines, "max_wakes_per_day: "+strconv.Itoa(o.MaxWakesPerDay))
	}
	if o.Expires != "" {
		lines = append(lines, "expires: "+o.Expires)
	}
	if o.Status == StatusPaused {
		lines = append(lines, "status: "+StatusPaused)
	}
	return strings.Join(lines, "\n")
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
}

func formatEvery(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	case d%time.Hour == 0:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	default:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	}
}

// SetStatus sets (or replaces) the status: line of one order.
func SetStatus(text, name, status string) (string, error) {
	secs := splitSections(text)
	for i, s := range secs {
		if s.name != name {
			continue
		}
		var kept []string
		insertAt := -1
		for j, l := range s.lines {
			if m := keyRe.FindStringSubmatch(strings.TrimSpace(l)); j > 0 && m != nil && m[1] == "status" {
				continue
			}
			kept = append(kept, l)
			if j == 0 || strings.TrimSpace(l) != "" {
				insertAt = len(kept)
			}
		}
		line := "status: " + status
		kept = append(kept[:insertAt], append([]string{line}, kept[insertAt:]...)...)
		secs[i].lines = kept
		return joinSections(secs), nil
	}
	return "", fmt.Errorf("no order named %q", name)
}

// Remove drops one order's section.
func Remove(text, name string) (string, error) {
	secs := splitSections(text)
	for i, s := range secs {
		if s.name == name {
			secs = append(secs[:i], secs[i+1:]...)
			return strings.TrimRight(joinSections(secs), "\n") + "\n", nil
		}
	}
	return "", fmt.Errorf("no order named %q", name)
}
