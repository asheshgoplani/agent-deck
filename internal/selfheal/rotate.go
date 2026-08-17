package selfheal

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Audit rotation sizing.
//
// MEASURED RATE (2026-08-12 → 2026-08-17, one normal single-user machine):
// 2.55 GB / 7,402,108 records in 5 days = ~480 MB/day, ~345 bytes/record. The
// pass emits one record per instance per poll, so the rate scales with fleet
// size × poll cadence and has no natural ceiling — before this, the file simply
// grew forever.
//
// The audit is the dataset a ≥1-week observe window is reconstructed from, so
// the retained window has to cover ≥7 days at that rate: ≥3.4 GB of records.
//
//	defaultMaxSegmentBytes = 128 MiB  ≈ 400k records ≈ 6.4 h at the measured rate.
//	                         Small enough that compressing one takes ~1s and that
//	                         a `zcat | jq` over a single segment is workable;
//	                         large enough that a roll happens ~4×/day, not hourly.
//	defaultRetainedSegments = 28      28 × 128 MiB = 3.5 GB of records ≈ 7.5 days,
//	                         plus the live segment. The window survives the
//	                         requirement with margin at the measured rate, and
//	                         degrades to "fewer days" rather than "no history"
//	                         if the rate rises.
//
// DISK COST: rolled segments are gzipped. Measured on representative records
// (unique per-record signatures, i.e. the pessimistic case) gzip -6 gives 11×,
// so 28 rolled segments cost ~330 MB, plus up to 128 MiB for the uncompressed
// live segment: ~460 MB worst case, bounded, versus unbounded before.
const (
	defaultMaxSegmentBytes  = 128 << 20
	defaultRetainedSegments = 28
)

// segmentStampPattern matches the suffix appended to a rolled segment:
// <live>.20260817T131901Z[-NNN][.gz]. It is validated rather than globbed
// loosely on purpose — the default audit paths are siblings
// (selfheal-audit.ndjson and selfheal-audit-<profile>.ndjson), and a sloppy
// pattern would let one profile's retention delete another profile's LIVE file.
// The -NNN discriminator (zero-padded so it sorts) is only used when two rolls
// land in the same second.
var segmentStampPattern = regexp.MustCompile(`^\d{8}T\d{6}Z(-\d{3})?$`)

const segmentStampLayout = "20060102T150405Z"

// auditSegment is one rolled segment of the audit, identified by its stamp. Both
// forms can exist at once for a moment: compression writes the .gz beside the
// plain file and only removes the plain one after the .gz is renamed into place,
// so an interrupted compression leaves readable history rather than a hole.
type auditSegment struct {
	stamp string
	plain string // "" once compression finished
	gz    string // "" until compression finished
}

// readPath returns the file to read this segment from, preferring the compressed
// form when both exist.
func (s auditSegment) readPath() string {
	if s.gz != "" {
		return s.gz
	}
	return s.plain
}

// listAuditSegments returns the rolled segments beside live, oldest first.
//
// It scans the directory rather than globbing: audit_path is operator-
// configurable, and a path containing a glob metacharacter would make
// filepath.Glob quietly match nothing — which reads as "no history" instead of
// an error and silently shortens the window.
func listAuditSegments(live string) ([]auditSegment, error) {
	dir := filepath.Dir(live)
	prefix := filepath.Base(live) + "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("selfheal: list audit segments: %w", err)
	}
	byStamp := map[string]*auditSegment{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		m := filepath.Join(dir, entry.Name())
		suffix := strings.TrimPrefix(entry.Name(), prefix)
		stamp := strings.TrimSuffix(suffix, ".gz")
		compressed := stamp != suffix
		if !segmentStampPattern.MatchString(stamp) {
			continue // .gz.tmp from an in-flight compression, or a foreign file
		}
		seg := byStamp[stamp]
		if seg == nil {
			seg = &auditSegment{stamp: stamp}
			byStamp[stamp] = seg
		}
		if compressed {
			seg.gz = m
		} else {
			seg.plain = m
		}
	}
	out := make([]auditSegment, 0, len(byStamp))
	for _, seg := range byStamp {
		out = append(out, *seg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].stamp < out[j].stamp })
	return out, nil
}

// AuditSegmentPaths returns every file making up the retained audit window for
// live, OLDEST FIRST, with the live file last. Rotation would otherwise silently
// shorten the observe window for anything that only opens the live path.
//
// A segment whose compression was interrupted is returned in its uncompressed
// form; a still-being-written .gz.tmp is skipped.
func AuditSegmentPaths(live string) ([]string, error) {
	segs, err := listAuditSegments(live)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(segs)+1)
	for _, seg := range segs {
		paths = append(paths, seg.readPath())
	}
	if _, err := os.Stat(live); err == nil {
		paths = append(paths, live)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("selfheal: stat live audit: %w", err)
	}
	return paths, nil
}

// maxAuditLineBytes bounds one record for the scanner. Records are ~345 bytes;
// 1 MiB is headroom of three orders of magnitude and still refuses to buffer a
// corrupt file without a newline into memory.
const maxAuditLineBytes = 1 << 20

// ForEachAuditEvent decodes every record in the retained window — rolled
// segments (compressed or not) then the live file — in chronological order, and
// calls fn for each. This is the reader to use for the ≥1-week observe window;
// reading only the live path sees just the current segment.
//
// Each segment is scanned separately, so a segment that was cut mid-line by a
// crash cannot glue its tail onto the next segment's head. An unparseable line
// is skipped rather than aborting the scan: a truncated tail must not cost the
// operator the whole window. fn's error stops the scan and is returned.
func ForEachAuditEvent(live string, fn func(Event) error) error {
	paths, err := AuditSegmentPaths(live)
	if err != nil {
		return err
	}
	for _, p := range paths {
		if err := forEachEventInFile(p, fn); err != nil {
			return err
		}
	}
	return nil
}

func forEachEventInFile(path string, fn func(Event) error) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // evicted by retention between listing and reading
		}
		return fmt.Errorf("selfheal: open audit segment %s: %w", path, err)
	}
	defer f.Close()

	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		zr, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("selfheal: read audit segment %s: %w", path, err)
		}
		defer zr.Close()
		r = zr
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxAuditLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			continue // truncated or corrupt line: skip it, keep the window
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("selfheal: scan audit segment %s: %w", path, err)
	}
	return nil
}

// rotateIfNeededLocked rolls the live file aside when appending incoming bytes
// would push it past the segment threshold. Callers must hold s.mu.
//
// It NEVER truncates and never rewrites in place (§3.5): the live file is
// RENAMED to a stamped sibling, which preserves every record byte-for-byte, and
// a fresh live file is created by the next append. Compression of the rolled
// segment runs off the lock so a poll cycle is never blocked on gzip.
//
// A rotation that cannot be performed is deliberately NOT an append error: the
// audit record matters more than the size bound, so the write proceeds into the
// oversized file and the next append retries the roll.
func (s *NDJSONSink) rotateIfNeededLocked(incoming int64) {
	if s.maxSegmentBytes <= 0 {
		// A sink not built through NewNDJSONSink carries no dials. Rotating on a
		// zero threshold would roll on EVERY append; append-only is the safer
		// reading of an unconfigured sink.
		return
	}
	fi, err := os.Stat(s.path)
	if err != nil || fi.Size() == 0 {
		return
	}
	if fi.Size()+incoming <= s.maxSegmentBytes {
		return
	}
	rolled := s.path + "." + s.nextStampLocked()
	if err := os.Rename(s.path, rolled); err != nil {
		return
	}
	s.rotations.Add(1)
	go func() {
		defer s.rotations.Done()
		s.compressSegment(rolled)
		s.pruneSegments()
	}()
}

// nextStampLocked returns a unique, lexicographically ordered segment stamp.
// Callers must hold s.mu.
func (s *NDJSONSink) nextStampLocked() string {
	stamp := s.now().UTC().Format(segmentStampLayout)
	if stamp != s.lastStamp {
		s.lastStamp = stamp
		s.stampSeq = 0
		return stamp
	}
	// Second roll within the same second: keep the stamps distinct AND ordered.
	s.stampSeq++
	return fmt.Sprintf("%s-%03d", stamp, s.stampSeq)
}

// compressSegment gzips a rolled segment in place, leaving the uncompressed file
// untouched if anything fails — a readable segment is worth more than a small
// one, and the reader handles both forms.
func (s *NDJSONSink) compressSegment(rolled string) {
	tmp := rolled + ".gz.tmp"
	in, err := os.Open(rolled)
	if err != nil {
		return
	}
	defer in.Close()
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	zw := gzip.NewWriter(out)
	if _, err := io.Copy(zw, in); err != nil {
		zw.Close()
		out.Close()
		os.Remove(tmp)
		return
	}
	if err := zw.Close(); err != nil {
		out.Close()
		os.Remove(tmp)
		return
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, rolled+".gz"); err != nil {
		os.Remove(tmp)
		return
	}
	// Only now is the compressed copy durable enough to drop the plain one.
	os.Remove(rolled)
}

// pruneSegments enforces the retention count, deleting the OLDEST rolled
// segments first. This is the one place the audit loses history, and it is the
// explicit, documented retention policy rather than an in-place truncation: the
// live file and every retained segment are still only ever appended to or
// renamed.
func (s *NDJSONSink) pruneSegments() {
	s.mu.Lock()
	defer s.mu.Unlock()
	segs, err := listAuditSegments(s.path)
	if err != nil || len(segs) <= s.retainedSegments {
		return
	}
	for _, seg := range segs[:len(segs)-s.retainedSegments] {
		if seg.gz != "" {
			os.Remove(seg.gz)
		}
		if seg.plain != "" {
			os.Remove(seg.plain)
		}
	}
}

// segmentStampNow is the production clock for rolled-segment stamps.
func segmentStampNow() time.Time { return time.Now() }
