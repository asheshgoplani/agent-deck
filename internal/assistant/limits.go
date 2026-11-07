package assistant

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limits are the per-token call budgets.
type Limits struct {
	ReadPerMinute  int `json:"read_per_minute"`
	WritePerMinute int `json:"write_per_minute"`
}

// DefaultLimits: 120 read calls and 10 write calls (send, approve, launch)
// per minute per token.
var DefaultLimits = Limits{ReadPerMinute: 120, WritePerMinute: 10}

// rateLimiter keeps one read and one write bucket per token name. Buckets
// start full and refill evenly over a minute.
type rateLimiter struct {
	limits Limits
	mu     sync.Mutex
	read   map[string]*rate.Limiter
	write  map[string]*rate.Limiter
}

func newRateLimiter(l Limits) *rateLimiter {
	return &rateLimiter{limits: l, read: map[string]*rate.Limiter{}, write: map[string]*rate.Limiter{}}
}

func perMinute(n int) *rate.Limiter {
	if n <= 0 {
		return rate.NewLimiter(0, 0)
	}
	return rate.NewLimiter(rate.Every(time.Minute/time.Duration(n)), n)
}

// allow takes one call from name's read or write bucket.
func (r *rateLimiter) allow(name string, write bool) bool {
	r.mu.Lock()
	m, n := r.read, r.limits.ReadPerMinute
	if write {
		m, n = r.write, r.limits.WritePerMinute
	}
	l, ok := m[name]
	if !ok {
		l = perMinute(n)
		m[name] = l
	}
	r.mu.Unlock()
	return l.Allow()
}

// AuditEntry is one line of assistants-audit.jsonl. It never carries a
// message body, a launch message or a token.
type AuditEntry struct {
	TS        time.Time `json:"ts"`
	Name      string    `json:"name"`
	Tool      string    `json:"tool"`
	Session   string    `json:"session,omitempty"`
	Allowed   bool      `json:"allowed"`
	Reason    string    `json:"reason,omitempty"`
	Transport string    `json:"transport"`
}

// auditLog appends AuditEntry lines to a 0600 JSONL file.
type auditLog struct {
	path string
	mu   sync.Mutex
}

func (a *auditLog) write(e AuditEntry) error {
	if a == nil || a.path == "" {
		return nil
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return fmt.Errorf("creating audit dir: %w", err)
	}
	f, err := os.OpenFile(a.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening audit log: %w", err)
	}
	_, werr := f.Write(append(b, '\n'))
	cerr := f.Close()
	if werr != nil {
		return fmt.Errorf("writing audit log: %w", werr)
	}
	return cerr
}

// ReadAudit returns the last n entries of the audit log at path (all when
// n <= 0), oldest first, optionally only those for name. A missing file is
// an empty log; unparsable lines are skipped.
func ReadAudit(path, name string, n int) ([]AuditEntry, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening audit log: %w", err)
	}
	defer f.Close()
	var out []AuditEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e AuditEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if name != "" && e.Name != name {
			continue
		}
		out = append(out, e)
		if n > 0 && len(out) > 4*n {
			out = append([]AuditEntry(nil), out[len(out)-n:]...)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading audit log: %w", err)
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}
