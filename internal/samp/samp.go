// Package samp implements the read-side of SAMP v1 (Simple Agent
// Message Protocol) needed to drive agent-deck's per-session unread
// badge. Reference implementation: agent-message
// (https://github.com/slima4/agent-message). agent-deck never writes
// SAMP messages — it only scans log-*.jsonl files and respects each
// agent's .seen-<alias> watermark.
//
// Cross-implementation parity is load-bearing: the id field is the
// SHA-256 of a canonical JSON encoding whose every detail (key order,
// separators, non-ASCII handling, NFC normalization of body) must
// match the Python reference byte-for-byte. See canonical and
// ComputeID — used here only to recompute legacy ids for records
// emitted before the id field existed.
package samp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"

	"golang.org/x/text/unicode/norm"
)

// Message is a SAMP v1 record. See SPEC.md §2.
type Message struct {
	ID     string `json:"id"`
	TS     int64  `json:"ts"`
	From   string `json:"from"`
	To     string `json:"to"`
	Thread string `json:"thread"`
	Body   string `json:"body"`
}

var aliasRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidateAlias returns nil if alias matches SPEC.md §1 regex.
func ValidateAlias(alias string) error {
	if !aliasRe.MatchString(alias) {
		return fmt.Errorf("invalid alias %q: must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$", alias)
	}
	return nil
}

// canonical returns the canonical JSON encoding used as input to ComputeID.
// Per SPEC.md §3:
//   - sort_keys (alphabetical body, from, thread, to, ts) — Go's
//     encoding/json sorts map[string]interface{} keys by default.
//   - separators (",", ":") — Go default emits no whitespace between tokens.
//   - ensure_ascii=False — raw UTF-8 bytes; HTML escape disabled via
//     Encoder.SetEscapeHTML(false), otherwise '<', '>', '&' would be
//     emitted as < etc., diverging from Python.
//   - body NFC-normalized before serialization.
func canonical(ts int64, from, to, thread, body string) ([]byte, error) {
	m := map[string]interface{}{
		"body":   norm.NFC.String(body),
		"from":   from,
		"thread": thread,
		"to":     to,
		"ts":     ts,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, fmt.Errorf("canonical encode: %w", err)
	}
	out := buf.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}

// ComputeID returns the 16-char lowercase hex id per SPEC.md §3.
// Used to recompute ids for legacy records that predate the id field.
func ComputeID(ts int64, from, to, thread, body string) (string, error) {
	c, err := canonical(ts, from, to, thread, body)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(c)
	return hex.EncodeToString(sum[:])[:16], nil
}
