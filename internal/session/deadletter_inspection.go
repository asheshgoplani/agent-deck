package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"
)

// DeadLetterRecord describes one physical nonblank record. Ref identifies its
// exact source snapshot and offset; an append or rewrite invalidates old refs.
// Raw is retained separately so malformed bytes and unknown fields remain
// available to inspection without routing, consuming or repairing anything.
type DeadLetterRecord struct {
	Ref            string `json:"ref"`
	Store          string `json:"store"`
	Source         string `json:"source"`
	Offset         int    `json:"offset"`
	ChildSessionID string `json:"child_session_id,omitempty"`
	Profile        string `json:"profile,omitempty"`
	Problem        string `json:"problem,omitempty"`
	Raw            []byte `json:"-"`
}

const (
	maxDeadLetterInspectionBytes   = maxInboxLineBytes
	maxDeadLetterInspectionRecords = 10000
)

type deadLetterInspectionError struct{ cause error }

func (e *deadLetterInspectionError) Error() string {
	return fmt.Sprintf("dead-letter inspection failed: %q", e.cause.Error())
}
func (e *deadLetterInspectionError) Unwrap() error { return e.cause }

// InspectDeadLetters reads both host-level stores, independently of profile
// registry availability. It returns no partial list on an unreadable source.
// Store must be all, dead-letter or unowned. No directories or locks are created.
func InspectDeadLetters(store string) (result []DeadLetterRecord, retErr error) {
	defer func() {
		if retErr != nil {
			retErr = &deadLetterInspectionError{cause: retErr}
		}
	}()
	if store != "all" && store != "dead-letter" && store != "unowned" {
		return nil, fmt.Errorf("unknown dead-letter store %q", store)
	}
	records := make([]DeadLetterRecord, 0)
	remainingBytes := int64(maxDeadLetterInspectionBytes)
	read := func(path, kind, name string) error {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("inspection source %q is not a regular file", name)
		}
		f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		opened, statErr := f.Stat()
		if statErr != nil || !os.SameFile(info, opened) {
			_ = f.Close()
			return fmt.Errorf("inspection source %q changed while opening", name)
		}
		raw, readErr := io.ReadAll(io.LimitReader(f, remainingBytes+1))
		closeErr := f.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if int64(len(raw)) > remainingBytes {
			return fmt.Errorf("inspection exceeds total limit of %d bytes", maxDeadLetterInspectionBytes)
		}
		remainingBytes -= int64(len(raw))
		generation := sha256.Sum256(raw)
		offset := 0
		for offset < len(raw) {
			length := bytes.IndexByte(raw[offset:], '\n')
			if length < 0 {
				length = len(raw) - offset
			} else {
				length++
			}
			line := raw[offset : offset+length]
			if len(bytes.TrimSpace(line)) > 0 {
				if len(records) >= maxDeadLetterInspectionRecords {
					return fmt.Errorf("inspection exceeds %d records", maxDeadLetterInspectionRecords)
				}
				digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%x\x00%d", kind, name, generation, offset)))
				rec := DeadLetterRecord{Ref: hex.EncodeToString(digest[:]), Store: kind, Source: name, Offset: offset, Raw: append([]byte(nil), line...)}
				var event *TransitionNotificationEvent
				switch {
				case !utf8.Valid(line):
					rec.Problem = "invalid UTF-8"
				case json.Unmarshal(line, &event) != nil:
					rec.Problem = "invalid event JSON"
				case event == nil:
					rec.Problem = "event must be an object"
				default:
					rec.ChildSessionID, rec.Profile = event.ChildSessionID, event.Profile
					if strings.TrimSpace(event.ChildSessionID) == "" {
						rec.Problem = "missing child_session_id"
					}
				}
				records = append(records, rec)
			}
			offset += length
		}
		return nil
	}
	if store == "all" || store == "dead-letter" {
		entries, err := os.ReadDir(DeadLetterDir())
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
				if err := read(filepath.Join(DeadLetterDir(), entry.Name()), "dead-letter", entry.Name()); err != nil {
					return nil, err
				}
			}
		}
	}
	if store == "all" || store == "unowned" {
		if err := read(InboxPathFor(UnownedInboxID), "unowned", UnownedInboxID+".jsonl"); err != nil {
			return nil, err
		}
	}
	return records, nil
}
