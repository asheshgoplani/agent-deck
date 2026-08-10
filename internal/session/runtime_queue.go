package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

type RuntimeQueuedMessage struct {
	ID       string    `json:"id"`
	Message  string    `json:"message"`
	QueuedAt time.Time `json:"queued_at"`
	Source   string    `json:"source"`
}

var ErrRuntimeQueueFull = errors.New("runtime message queue is full")

const (
	MaxRuntimeQueueMessages = 100
	MaxRuntimeQueueBytes    = 16 << 20
)

func RuntimeQueueDir() string {
	dir, err := dataPath("runtime-queues", "runtime-queues")
	if err != nil {
		return tempAgentDeckPath("runtime-queues")
	}
	return dir
}

func RuntimeQueuePathFor(id string) string {
	return filepath.Join(RuntimeQueueDir(), sanitizeInboxName(id)+".jsonl")
}

func EnqueueRuntimeMessage(id, msg string) (depth int, err error) {
	path := RuntimeQueuePathFor(id)

	inboxWriteMu.Lock()
	defer inboxWriteMu.Unlock()

	existing, existingBytes, err := readRuntimeQueueLocked(path)
	if err != nil {
		return 0, err
	}
	if len(existing) >= MaxRuntimeQueueMessages {
		return 0, ErrRuntimeQueueFull
	}

	queued := RuntimeQueuedMessage{
		ID:       uuid.NewString(),
		Message:  msg,
		QueuedAt: time.Now().UTC(),
		Source:   "session-send",
	}
	line, err := jsonMarshalRuntimeMessage(queued)
	if err != nil {
		return 0, err
	}
	if existingBytes+int64(len(line)) > MaxRuntimeQueueBytes {
		return 0, ErrRuntimeQueueFull
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return 0, err
	}
	if err := fsyncFile(f); err != nil {
		_ = f.Close()
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	fsyncDir(dir)
	return len(existing) + 1, nil
}

func RuntimeQueueHasPending(id string) bool {
	return fileHasContent(RuntimeQueuePathFor(id))
}

func PeekRuntimeQueue(id string) ([]RuntimeQueuedMessage, error) {
	inboxWriteMu.Lock()
	defer inboxWriteMu.Unlock()
	queued, _, err := readRuntimeQueueLocked(RuntimeQueuePathFor(id))
	return queued, err
}

func DiscardRuntimeQueue(id string) error {
	inboxWriteMu.Lock()
	defer inboxWriteMu.Unlock()

	dir := RuntimeQueueDir()
	for _, path := range []string{RuntimeQueuePathFor(id), runtimeQueueInflightPathFor(id)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	fsyncDir(dir)
	return nil
}

func runtimeQueueInflightPathFor(id string) string {
	return RuntimeQueuePathFor(id) + ".inflight"
}

func jsonMarshalRuntimeMessage(msg RuntimeQueuedMessage) ([]byte, error) {
	line, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return append(line, '\n'), nil
}

func readRuntimeQueueLocked(path string) ([]RuntimeQueuedMessage, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	if info.Size() > MaxRuntimeQueueBytes {
		return nil, info.Size(), fmt.Errorf("runtime queue exceeds %d bytes", MaxRuntimeQueueBytes)
	}
	if info.Size() > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], info.Size()-1); err != nil {
			return nil, info.Size(), fmt.Errorf("read runtime queue terminator %s: %w", path, err)
		}
		if last[0] != '\n' {
			return nil, info.Size(), fmt.Errorf("runtime queue %s has an unterminated JSON line", path)
		}
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), MaxRuntimeQueueBytes)
	queued := make([]RuntimeQueuedMessage, 0)
	for scanner.Scan() {
		var msg RuntimeQueuedMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			return nil, info.Size(), fmt.Errorf("decode runtime queue %s: %w", path, err)
		}
		if msg.ID == "" || msg.QueuedAt.IsZero() || msg.Source != "session-send" {
			return nil, info.Size(), fmt.Errorf("decode runtime queue %s: invalid message metadata", path)
		}
		queued = append(queued, msg)
	}
	if err := scanner.Err(); err != nil {
		return nil, info.Size(), fmt.Errorf("read runtime queue %s: %w", path, err)
	}
	return queued, info.Size(), nil
}
