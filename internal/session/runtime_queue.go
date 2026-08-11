package session

import (
	"bufio"
	"bytes"
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

var (
	runtimeQueueNewID   = uuid.NewString
	runtimeQueueNow     = time.Now
	runtimeQueuePersist = writeFileDurable
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
		ID:       runtimeQueueNewID(),
		Message:  msg,
		QueuedAt: runtimeQueueNow().UTC(),
		Source:   "session-send",
	}
	line, err := jsonMarshalRuntimeMessage(queued)
	if err != nil {
		return 0, err
	}
	if len(existingBytes)+len(line) > MaxRuntimeQueueBytes {
		return 0, ErrRuntimeQueueFull
	}

	data := make([]byte, 0, len(existingBytes)+len(line))
	data = append(data, existingBytes...)
	data = append(data, line...)
	if err := runtimeQueuePersist(path, data, 0o644); err != nil {
		return 0, err
	}
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

func readRuntimeQueueLocked(path string) ([]RuntimeQueuedMessage, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if len(raw) > MaxRuntimeQueueBytes {
		return nil, raw, fmt.Errorf("runtime queue exceeds %d bytes", MaxRuntimeQueueBytes)
	}
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		return nil, raw, fmt.Errorf("runtime queue %s has an unterminated JSON line", path)
	}

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), MaxRuntimeQueueBytes)
	queued := make([]RuntimeQueuedMessage, 0)
	for scanner.Scan() {
		var msg RuntimeQueuedMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			return nil, raw, fmt.Errorf("decode runtime queue %s: %w", path, err)
		}
		if msg.ID == "" || msg.QueuedAt.IsZero() || msg.Source != "session-send" {
			return nil, raw, fmt.Errorf("decode runtime queue %s: invalid message metadata", path)
		}
		queued = append(queued, msg)
	}
	if err := scanner.Err(); err != nil {
		return nil, raw, fmt.Errorf("read runtime queue %s: %w", path, err)
	}
	return queued, raw, nil
}
