package trace

import (
	"encoding/json"
	"math"
	"sort"
	"time"
)

// TraceRow is one trace as listed by the CLI.
type TraceRow struct {
	TraceID   string
	SessionID string
	Tool      string
	Instance  string
	StartedAt int64
	EndedAt   int64
	Spans     int
}

// Span is one span as read back.
type Span struct {
	SpanID     string
	TraceID    string
	ParentID   string
	Kind       string
	Name       string
	StartedAt  int64
	EndedAt    int64 // 0 = open
	DurationMS int64 // -1 = unknown
	Status     string
	Inferred   bool // end inferred (no PostToolUse/Stop seen)
	Summary    string
	Children   []*Span
}

// Traces lists traces, newest first. match filters by trace id, harness
// session id or agent-deck instance id (exact or prefix); "" lists all.
func (s *Store) Traces(match string, limit int) ([]TraceRow, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT t.trace_id, COALESCE(t.session_id,''), t.tool, COALESCE(t.instance_id,''),
			COALESCE(t.started_at,0), COALESCE(t.ended_at,0),
			(SELECT COUNT(*) FROM spans s WHERE s.trace_id = t.trace_id)
		FROM traces t`
	var args []any
	if match != "" {
		q += ` WHERE t.trace_id = ? OR t.session_id LIKE ? OR t.instance_id LIKE ?`
		args = append(args, match, match+"%", match+"%")
	}
	q += ` ORDER BY t.started_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TraceRow
	for rows.Next() {
		var t TraceRow
		if err := rows.Scan(&t.TraceID, &t.SessionID, &t.Tool, &t.Instance, &t.StartedAt, &t.EndedAt, &t.Spans); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Tree returns the spans of a trace as a forest: turns at the root with
// their tool calls and waits as children, ordered by start.
func (s *Store) Tree(traceID string) ([]*Span, error) {
	rows, err := s.db.Query(`SELECT span_id, trace_id, COALESCE(parent_span_id,''), kind, COALESCE(name,''),
			COALESCE(started_at,0), COALESCE(ended_at,0), COALESCE(duration_ms,-1), status, COALESCE(attrs,'')
		FROM spans WHERE trace_id = ? ORDER BY started_at ASC, rowid ASC`, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []*Span
	byID := map[string]*Span{}
	for rows.Next() {
		sp := &Span{}
		var attrs string
		if err := rows.Scan(&sp.SpanID, &sp.TraceID, &sp.ParentID, &sp.Kind, &sp.Name, &sp.StartedAt, &sp.EndedAt, &sp.DurationMS, &sp.Status, &attrs); err != nil {
			return nil, err
		}
		if attrs != "" {
			var a map[string]any
			if json.Unmarshal([]byte(attrs), &a) == nil {
				sp.Inferred = a["end"] == "inferred"
				if v, ok := a["summary"].(string); ok {
					sp.Summary = v
				}
			}
		}
		all = append(all, sp)
		byID[sp.SpanID] = sp
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var roots []*Span
	for _, sp := range all {
		if p, ok := byID[sp.ParentID]; ok && sp.ParentID != "" {
			p.Children = append(p.Children, sp)
		} else {
			roots = append(roots, sp)
		}
	}
	return roots, nil
}

// ToolStat aggregates tool_call spans per tool name.
type ToolStat struct {
	Tool      string
	Count     int
	Ended     int // calls with a known end (paired or inferred)
	Inferred  int // ends inferred, not observed
	Errors    int
	TotalMS   int64 // sum over observed (paired) durations
	P95MS     int64 // over observed (paired) durations
	ErrorRate float64
	MaxMS     int64
	durations []int64
}

// StatsOptions filters Stats.
type StatsOptions struct {
	Since time.Time // zero = all time
	Tool  string    // agent tool filter (claude, codex); "" = all
}

// Stats returns per-tool-name counts, total and p95 duration, and error
// rate, slowest total first. Durations use only observed (Pre+Post paired)
// ends; inferred ends are counted separately.
func (s *Store) Stats(opts StatsOptions) ([]ToolStat, error) {
	q := `SELECT COALESCE(s.name,''), COALESCE(s.duration_ms,-1), s.status, COALESCE(json_extract(s.attrs,'$.end'),''),
			s.ended_at IS NOT NULL
		FROM spans s JOIN traces t ON t.trace_id = s.trace_id
		WHERE s.kind = 'tool_call'`
	var args []any
	if !opts.Since.IsZero() {
		q += ` AND s.started_at >= ?`
		args = append(args, opts.Since.UnixMilli())
	}
	if opts.Tool != "" {
		q += ` AND t.tool = ?`
		args = append(args, opts.Tool)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	by := map[string]*ToolStat{}
	for rows.Next() {
		var name, status, end string
		var dur int64
		var ended bool
		if err := rows.Scan(&name, &dur, &status, &end, &ended); err != nil {
			return nil, err
		}
		st := by[name]
		if st == nil {
			st = &ToolStat{Tool: name}
			by[name] = st
		}
		st.Count++
		if ended {
			st.Ended++
		}
		if status == StatusError {
			st.Errors++
		}
		if end == "inferred" {
			st.Inferred++
			continue
		}
		if ended && dur >= 0 {
			st.durations = append(st.durations, dur)
			st.TotalMS += dur
			if dur > st.MaxMS {
				st.MaxMS = dur
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ToolStat, 0, len(by))
	for _, st := range by {
		st.P95MS = percentile(st.durations, 0.95)
		if st.Count > 0 {
			st.ErrorRate = float64(st.Errors) / float64(st.Count)
		}
		st.durations = nil
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalMS != out[j].TotalMS {
			return out[i].TotalMS > out[j].TotalMS
		}
		return out[i].Tool < out[j].Tool
	})
	return out, nil
}

// percentile is the nearest-rank percentile (0 for no samples).
func percentile(vals []int64, p float64) int64 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]int64(nil), vals...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	rank := int(math.Ceil(p*float64(len(s)))) - 1
	if rank < 0 {
		rank = 0
	}
	return s[rank]
}
