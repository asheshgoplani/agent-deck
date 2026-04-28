package samp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ScanOptions controls Scan.
type ScanOptions struct {
	// Me filters to records where to == Me. Empty matches all recipients.
	Me string
	// IncludeAll, when true, ignores the watermark — returns every record
	// addressed to Me. Maps to SAMP "all" mode (SPEC.md §6).
	IncludeAll bool
}

// ScanResult is the parsed output of Scan plus the inputs a caller needs
// to update the on-disk watermark per SPEC.md §6 step 6.
type ScanResult struct {
	// Messages is the new records, sorted ascending by (TS, ID).
	Messages []Message
	// MaxTS is the largest TS observed across new + prior watermark.
	MaxTS int64
	// IDsAtMaxTS lists ids of records at MaxTS, including any that were
	// already in the prior watermark when MaxTS == prior.TS. Caller can
	// persist this directly via SaveWatermark.
	IDsAtMaxTS []string
}

// Scan reads every $DIR/log-*.jsonl, dedups by id, filters by recipient,
// applies the watermark (unless opts.IncludeAll), sorts by ts, and returns
// new messages plus updated watermark inputs.
//
// Pass wm=nil to scan from the beginning of time.
func Scan(dir string, opts ScanOptions, wm *Watermark) (*ScanResult, error) {
	logs, err := filepath.Glob(filepath.Join(dir, "log-*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("Scan: glob: %w", err)
	}

	seen := make(map[string]struct{})
	var msgs []Message
	for _, p := range logs {
		base := filepath.Base(p)
		if !strings.HasPrefix(base, "log-") || !strings.HasSuffix(base, ".jsonl") {
			continue
		}
		if err := scanFile(p, opts, wm, seen, &msgs); err != nil {
			return nil, err
		}
	}

	sort.Slice(msgs, func(i, j int) bool {
		if msgs[i].TS != msgs[j].TS {
			return msgs[i].TS < msgs[j].TS
		}
		return msgs[i].ID < msgs[j].ID
	})

	res := &ScanResult{Messages: msgs}
	if n := len(msgs); n > 0 {
		res.MaxTS = msgs[n-1].TS
		for i := n - 1; i >= 0 && msgs[i].TS == res.MaxTS; i-- {
			res.IDsAtMaxTS = append(res.IDsAtMaxTS, msgs[i].ID)
		}
		// Same-second-burst rule (SPEC.md §6 step 6): if max ts equals the
		// prior watermark's ts, retain its ids alongside the newly-seen ones.
		if wm != nil && wm.TS == res.MaxTS {
			for _, prev := range wm.IDs {
				if !contains(res.IDsAtMaxTS, prev) {
					res.IDsAtMaxTS = append(res.IDsAtMaxTS, prev)
				}
			}
		}
	} else if wm != nil {
		res.MaxTS = wm.TS
		res.IDsAtMaxTS = append(res.IDsAtMaxTS, wm.IDs...)
	}
	return res, nil
}

func scanFile(path string, opts ScanOptions, wm *Watermark, seen map[string]struct{}, out *[]Message) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("scanFile: open %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			// Forward-compatible: skip records this version can't parse.
			continue
		}
		if m.ID == "" {
			id, err := ComputeID(m.TS, m.From, m.To, m.Thread, m.Body)
			if err != nil {
				continue
			}
			m.ID = id
		}
		if _, dup := seen[m.ID]; dup {
			continue
		}
		seen[m.ID] = struct{}{}

		if opts.Me != "" && m.To != opts.Me {
			continue
		}
		if !opts.IncludeAll && wm != nil {
			if m.TS < wm.TS {
				continue
			}
			if m.TS == wm.TS && contains(wm.IDs, m.ID) {
				continue
			}
		}
		*out = append(*out, m)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("scanFile: read %s: %w", path, err)
	}
	return nil
}

// UnreadCount returns the number of messages addressed to alias that
// the agent's own SAMP reader has not yet acknowledged (i.e., not yet
// covered by $DIR/.seen-<alias>).
//
// READ-ONLY: does not write .seen-<alias>. agent-deck uses this to
// render a per-session badge — the agent itself owns the watermark
// file via its /message-inbox slash command. Mutating the watermark
// from agent-deck would race with the agent's reader and silently
// hide messages.
//
// alias is the SAMP identifier for the session (validated). Returns
// 0 with nil error when the directory or log files are absent.
func UnreadCount(dir, alias string) (int, error) {
	if err := ValidateAlias(alias); err != nil {
		return 0, err
	}
	wm, err := LoadWatermark(dir, alias)
	if err != nil {
		return 0, err
	}
	res, err := Scan(dir, ScanOptions{Me: alias}, wm)
	if err != nil {
		return 0, err
	}
	return len(res.Messages), nil
}

// UnreadCache short-circuits UnreadCount calls using the SPEC.md §6
// mtime cache. Hold one per process; safe for concurrent use only with
// external synchronization (callers typically poll from one goroutine).
type UnreadCache struct {
	mtime int64
	files int
	count int
	valid bool
}

// Get returns the cached unread count when no log file in dir has
// changed since the last call; otherwise re-scans.
func (c *UnreadCache) Get(dir, alias string) (int, error) {
	mt, n, err := CurrentMtime(dir)
	if err != nil {
		return 0, err
	}
	if c.valid && mt == c.mtime && n == c.files {
		return c.count, nil
	}
	cnt, err := UnreadCount(dir, alias)
	if err != nil {
		return 0, err
	}
	c.mtime, c.files, c.count, c.valid = mt, n, cnt, true
	return cnt, nil
}

// LatestTo returns the most recent record addressed to me across all log
// files, or (nil, nil) when no such record exists. Used by reply paths
// (SPEC.md §7).
func LatestTo(dir, me string) (*Message, error) {
	if err := ValidateAlias(me); err != nil {
		return nil, err
	}
	res, err := Scan(dir, ScanOptions{Me: me, IncludeAll: true}, nil)
	if err != nil {
		return nil, err
	}
	if len(res.Messages) == 0 {
		return nil, nil
	}
	last := res.Messages[len(res.Messages)-1]
	return &last, nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
