package agents

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CronSchedule is a parsed five-field cron expression.
//
// This exists to RENDER a next-due time for a trigger whose firing lives in a
// plist or a systemd timer. It is a display calculation over a declared
// schedule. Nothing in this package acts on the result.
type CronSchedule struct {
	Minute  fieldSet
	Hour    fieldSet
	Day     fieldSet
	Month   fieldSet
	Weekday fieldSet
	// Location is the timezone the expression is evaluated in.
	Location *time.Location
	// RawSpec is the original text, kept for display.
	RawSpec string
}

// fieldSet is a bitmask over the legal values of one cron field.
type fieldSet struct {
	bits uint64
	// star records that the field was "*", which cron's day/weekday rule
	// needs in order to behave correctly.
	star bool
}

func (f fieldSet) has(value int) bool {
	if value < 0 || value > 63 {
		return false
	}
	return f.bits&(1<<uint(value)) != 0
}

// ParseCron parses a five-field cron expression: minute hour day month weekday.
//
// It supports *, numbers, lists (1,2), ranges (1-5) and steps (*/5, 1-9/2) —
// the syntax the design commits to for v1. Anything else is rejected rather
// than approximated, because a wrong next-due time is worse than none.
func ParseCron(spec string, loc *time.Location) (*CronSchedule, error) {
	if loc == nil {
		loc = time.Local
	}
	fields := strings.Fields(strings.TrimSpace(spec))
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron %q: want 5 fields, got %d", spec, len(fields))
	}

	minute, err := parseCronField(fields[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("cron %q minute: %w", spec, err)
	}
	hour, err := parseCronField(fields[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("cron %q hour: %w", spec, err)
	}
	day, err := parseCronField(fields[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("cron %q day: %w", spec, err)
	}
	month, err := parseCronField(fields[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("cron %q month: %w", spec, err)
	}
	weekday, err := parseCronField(fields[4], 0, 7)
	if err != nil {
		return nil, fmt.Errorf("cron %q weekday: %w", spec, err)
	}
	// Cron accepts both 0 and 7 for Sunday.
	if weekday.has(7) {
		weekday.bits |= 1
	}

	return &CronSchedule{
		Minute: minute, Hour: hour, Day: day, Month: month, Weekday: weekday,
		Location: loc, RawSpec: strings.TrimSpace(spec),
	}, nil
}

func parseCronField(field string, min, max int) (fieldSet, error) {
	var result fieldSet
	if field == "*" {
		result.star = true
		for v := min; v <= max; v++ {
			result.bits |= 1 << uint(v)
		}
		return result, nil
	}

	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return result, fmt.Errorf("empty element in %q", field)
		}

		step := 1
		if base, stepText, found := strings.Cut(part, "/"); found {
			parsed, err := strconv.Atoi(strings.TrimSpace(stepText))
			if err != nil || parsed <= 0 {
				return result, fmt.Errorf("bad step %q", stepText)
			}
			step = parsed
			part = strings.TrimSpace(base)
		}

		rangeStart, rangeEnd := min, max
		switch {
		case part == "*":
			result.star = result.star || step == 1
		case strings.Contains(part, "-"):
			startText, endText, _ := strings.Cut(part, "-")
			start, err := strconv.Atoi(strings.TrimSpace(startText))
			if err != nil {
				return result, fmt.Errorf("bad range start %q", startText)
			}
			end, err := strconv.Atoi(strings.TrimSpace(endText))
			if err != nil {
				return result, fmt.Errorf("bad range end %q", endText)
			}
			rangeStart, rangeEnd = start, end
		default:
			value, err := strconv.Atoi(part)
			if err != nil {
				return result, fmt.Errorf("bad value %q", part)
			}
			rangeStart, rangeEnd = value, value
		}

		if rangeStart < min || rangeEnd > max || rangeStart > rangeEnd {
			return result, fmt.Errorf("range %d-%d out of bounds %d-%d", rangeStart, rangeEnd, min, max)
		}
		for v := rangeStart; v <= rangeEnd; v += step {
			result.bits |= 1 << uint(v)
		}
	}
	return result, nil
}

// maxCronSearchMinutes bounds the next-occurrence search at roughly four
// years, enough to cover a Feb-29-only schedule and to terminate on an
// expression that can never match.
const maxCronSearchMinutes = 4 * 366 * 24 * 60

// Next returns the next time the schedule fires strictly after `after`.
// The second return is false when the expression cannot fire within the
// search bound — the honest answer for "0 0 30 2 *".
func (c *CronSchedule) Next(after time.Time) (time.Time, bool) {
	if c == nil {
		return time.Time{}, false
	}
	t := after.In(c.Location).Truncate(time.Minute).Add(time.Minute)

	for i := 0; i < maxCronSearchMinutes; i++ {
		if c.matches(t) {
			return t, true
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, false
}

// matches implements cron's day-of-month / day-of-week rule: when both fields
// are restricted, a match on EITHER counts.
func (c *CronSchedule) matches(t time.Time) bool {
	if !c.Minute.has(t.Minute()) || !c.Hour.has(t.Hour()) || !c.Month.has(int(t.Month())) {
		return false
	}

	dayMatch := c.Day.has(t.Day())
	weekdayMatch := c.Weekday.has(int(t.Weekday()))

	switch {
	case c.Day.star && c.Weekday.star:
		return true
	case c.Day.star:
		return weekdayMatch
	case c.Weekday.star:
		return dayMatch
	default:
		return dayMatch || weekdayMatch
	}
}

// DescribeCron renders a schedule the way the fleet list shows it. It stays
// short and falls back to the raw expression rather than inventing prose for
// something it does not recognize.
func DescribeCron(spec string) string {
	fields := strings.Fields(strings.TrimSpace(spec))
	if len(fields) != 5 {
		return spec
	}
	minute, hour, day, month, weekday := fields[0], fields[1], fields[2], fields[3], fields[4]

	if day == "*" && month == "*" && weekday == "*" {
		if strings.HasPrefix(minute, "*/") && hour == "*" {
			return "cron " + strings.TrimPrefix(minute, "*/") + "m"
		}
		if hour == "*" && minute == "*" {
			return "cron 1m"
		}
		if hour == "*" {
			if _, err := strconv.Atoi(minute); err == nil {
				return "cron hourly"
			}
		}
		if minuteNum, err := strconv.Atoi(minute); err == nil {
			if hourNum, hourErr := strconv.Atoi(hour); hourErr == nil {
				return fmt.Sprintf("daily %02d:%02d", hourNum, minuteNum)
			}
		}
	}
	return "cron " + spec
}

// DescribeInterval renders a StartInterval-style cadence.
func DescribeInterval(seconds int) string {
	switch {
	case seconds <= 0:
		return ""
	case seconds < 60:
		return fmt.Sprintf("every %ds", seconds)
	case seconds%3600 == 0:
		return fmt.Sprintf("every %dh", seconds/3600)
	case seconds%60 == 0:
		return fmt.Sprintf("every %dm", seconds/60)
	default:
		// Not a whole number of minutes. Flooring turned a 90-second cadence
		// into "every 1m", which is a WRONG cadence rather than a coarse one.
		return fmt.Sprintf("every %dm%ds", seconds/60, seconds%60)
	}
}

// SystemdCalendarToCron converts the simple, unambiguous OnCalendar forms to a
// five-field cron expression so a next-due time can be rendered.
//
// It is deliberately conservative. systemd's calendar syntax is richer than
// cron (weekday sets, repetition inside a field, sub-minute precision), and a
// spec this function cannot convert exactly returns ok=false so the caller
// keeps the raw text and says it could not compute a time. Rendering a wrong
// next-due for a real timer would be worse than rendering none.
func SystemdCalendarToCron(spec string) (string, bool) {
	trimmed := strings.TrimSpace(spec)
	switch strings.ToLower(trimmed) {
	case "hourly":
		return "0 * * * *", true
	case "daily", "midnight":
		return "0 0 * * *", true
	case "weekly":
		return "0 0 * * 1", true
	case "monthly":
		return "0 0 1 * *", true
	case "yearly", "annually":
		return "0 0 1 1 *", true
	}

	// "*-*-* HH:MM:SS" and "*-*-* HH:MM" — every day at a fixed time.
	fields := strings.Fields(trimmed)
	if len(fields) != 2 || fields[0] != "*-*-*" {
		return "", false
	}
	timeParts := strings.Split(fields[1], ":")
	if len(timeParts) < 2 || len(timeParts) > 3 {
		return "", false
	}
	// A non-zero seconds field cannot be expressed in cron at all.
	if len(timeParts) == 3 && strings.TrimSpace(timeParts[2]) != "00" && strings.TrimSpace(timeParts[2]) != "0" {
		return "", false
	}
	hour, err := strconv.Atoi(strings.TrimSpace(timeParts[0]))
	if err != nil || hour < 0 || hour > 23 {
		return "", false
	}
	minute, err := strconv.Atoi(strings.TrimSpace(timeParts[1]))
	if err != nil || minute < 0 || minute > 59 {
		return "", false
	}
	return fmt.Sprintf("%d %d * * *", minute, hour), true
}
