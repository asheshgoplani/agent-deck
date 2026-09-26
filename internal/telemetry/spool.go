package telemetry

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

// SpoolFileName is the local event queue, next to the state file.
const SpoolFileName = "telemetry-spool.ndjson"

const (
	maxSpoolBytes   = 512 << 10
	maxSpoolLines   = 5000
	maxLineBytes    = 1 << 10
	spoolExpiryDays = 14
)

// spoolLine is one recorded event. Envelope fields that depend only on
// state (install id, first-seen age, OS) are added at upload time.
type spoolLine struct {
	E  string         `json:"e"`
	U  string         `json:"u"`
	D  string         `json:"d"`
	H  *int           `json:"h,omitempty"`
	W  *int           `json:"w,omitempty"`
	S  int            `json:"s"`
	V  string         `json:"v"`
	A  string         `json:"a"`
	SF string         `json:"sf"`
	L  string         `json:"l"`
	P  map[string]any `json:"p"`
}

func spoolPath() (string, error) { return siblingPath(SpoolFileName) }

// appendSpool writes one line with O_APPEND. Callers hold the state lock.
func appendSpool(line spoolLine) error {
	data, err := json.Marshal(line)
	if err != nil {
		return err
	}
	if len(data) > maxLineBytes {
		return errors.New("telemetry: spool line too long")
	}
	path, err := spoolPath()
	if err != nil {
		return err
	}
	// Retention is enforced here as well as in the uploader: a build with no
	// project key never uploads, and its spool must still expire and stay
	// under the cap.
	now := nowFn()
	if spoolNeedsTrim(path, now) {
		if lines, err := readSpool(); err == nil {
			_ = writeSpool(trimSpool(lines, now))
		}
	}
	// Hard guard, in case the trim above could not rewrite the file.
	if fi, err := os.Stat(path); err == nil && fi.Size() >= 2*maxSpoolBytes {
		return errors.New("telemetry: spool full")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(data, '\n'))
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// spoolNeedsTrim reports whether the spool is over the byte cap or its first
// (oldest) line is past the expiry. Only the first line is read.
func spoolNeedsTrim(path string, now time.Time) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > maxSpoolBytes {
		return true
	}
	first, err := bufio.NewReaderSize(f, maxLineBytes+1).ReadSlice('\n')
	if err != nil && len(first) == 0 {
		return false
	}
	var l struct {
		D string `json:"d"`
	}
	if json.Unmarshal(bytes.TrimSpace(first), &l) != nil {
		return true // a torn or forged first line: the rewrite drops it
	}
	return l.D < dayOf(now.AddDate(0, 0, -spoolExpiryDays))
}

// readSpool returns every well-formed line; a torn or invalid line (for
// example the tail of a crash mid-write) is skipped.
func readSpool() ([]spoolLine, error) {
	path, err := spoolPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []spoolLine
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		raw := sc.Bytes()
		if len(raw) == 0 || len(raw) > maxLineBytes {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var l spoolLine
		if dec.Decode(&l) != nil || !l.valid() {
			continue
		}
		out = append(out, l)
	}
	return out, nil
}

// valid re-checks a line read back from disk against the schema, so a
// hand-edited spool cannot smuggle anything into an upload.
func (l *spoolLine) valid() bool {
	for k, v := range l.P {
		if n, ok := v.(json.Number); ok {
			i, err := n.Int64()
			if err != nil {
				return false
			}
			l.P[k] = int(i)
		}
	}
	if Validate(l.E, l.P) != nil || !reDay.MatchString(l.D) || !reVersion.MatchString(l.V) {
		return false
	}
	if l.H != nil && (*l.H < 0 || *l.H > 23) || l.W != nil && (*l.W < 0 || *l.W > 6) {
		return false
	}
	return uuidPattern.MatchString(l.U) && contains(actorValues, l.A) &&
		contains(surfaceValues, l.SF) && contains(levelValues, l.L)
}

// writeSpool atomically replaces the spool; an empty slice deletes it.
func writeSpool(lines []spoolLine) error {
	path, err := spoolPath()
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		return deleteSpoolFile(path)
	}
	var buf bytes.Buffer
	for _, l := range lines {
		data, err := json.Marshal(l)
		if err != nil {
			return err
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	return atomicfile.WriteFile(path, buf.Bytes(), 0600)
}

func deleteSpoolFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("telemetry: delete spool: %w", err)
	}
	return nil
}

// DeleteSpool removes the local event queue. Callers hold the state lock or
// accept a racing append (disable holds it).
func DeleteSpool() error {
	path, err := spoolPath()
	if err != nil {
		return err
	}
	return deleteSpoolFile(path)
}

// trimSpool applies the retention rules: days older than 14 are dropped,
// and on overflow only the newest 80% of lines are kept.
func trimSpool(lines []spoolLine, now time.Time) []spoolLine {
	cutoff := dayOf(now.AddDate(0, 0, -spoolExpiryDays))
	kept := lines[:0:0]
	size := 0
	for _, l := range lines {
		if l.D < cutoff {
			continue
		}
		kept = append(kept, l)
		size += approxLineSize(l)
	}
	if len(kept) > maxSpoolLines || size > maxSpoolBytes {
		drop := len(kept) - len(kept)*8/10
		kept = kept[drop:]
	}
	return kept
}

func approxLineSize(l spoolLine) int {
	data, _ := json.Marshal(l)
	return len(data) + 1
}

// SpoolStats summarises the local queue for `telemetry status`.
type SpoolStats struct {
	Events    int    `json:"events"`
	Bytes     int64  `json:"bytes"`
	OldestDay string `json:"oldest_day,omitempty"`
}

// ReadSpoolStats reports the size of the local queue.
func ReadSpoolStats() SpoolStats {
	var st SpoolStats
	path, err := spoolPath()
	if err != nil {
		return st
	}
	if fi, err := os.Stat(path); err == nil {
		st.Bytes = fi.Size()
	}
	lines, _ := readSpool()
	st.Events = len(lines)
	for _, l := range lines {
		if st.OldestDay == "" || l.D < st.OldestDay {
			st.OldestDay = l.D
		}
	}
	return st
}
