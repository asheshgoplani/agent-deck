package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

type RuntimeQueuedMessage struct {
	ID       string    `json:"id"`
	Message  string    `json:"message"`
	QueuedAt time.Time `json:"queued_at"`
	Source   string    `json:"source"`
}

type RuntimeQueueBatch struct {
	Token    string
	Messages []RuntimeQueuedMessage
}

type runtimeQueueWAL struct {
	Token      string   `json:"token"`
	MessageIDs []string `json:"message_ids"`
}

type runtimeQueueCompletion struct {
	Token string `json:"token"`
}

func StageRuntimeQueue(id string) (RuntimeQueueBatch, error) {
	runtimeQueueStageEnter()
	runtimeQueueMu.Lock()
	defer runtimeQueueMu.Unlock()

	queued, _, err := readRuntimeQueueLocked(RuntimeQueuePathFor(id))
	if err != nil {
		return RuntimeQueueBatch{}, err
	}
	wal, exists, err := readRuntimeQueueWALLocked(id)
	if err != nil {
		return RuntimeQueueBatch{}, err
	}
	if exists {
		messages, err := runtimeQueuePrefixForWAL(queued, wal)
		if err == nil {
			return RuntimeQueueBatch{Token: wal.Token, Messages: messages}, nil
		}
		completed, completionErr := runtimeQueueWALCompletedLocked(id, wal.Token)
		if completionErr != nil {
			return RuntimeQueueBatch{}, completionErr
		}
		if !completed {
			return RuntimeQueueBatch{}, err
		}
		if err := removeRuntimeQueueWALLocked(id); err != nil {
			return RuntimeQueueBatch{}, err
		}
	}
	if len(queued) == 0 {
		return RuntimeQueueBatch{}, nil
	}

	wal.Token = runtimeQueueNewID()
	if wal.Token == "" {
		return RuntimeQueueBatch{}, errors.New("generate runtime queue batch token: empty token")
	}
	wal.MessageIDs = make([]string, len(queued))
	for i := range queued {
		wal.MessageIDs[i] = queued[i].ID
	}
	if err := writeRuntimeQueueJSONLocked(runtimeQueueInflightPathFor(id), wal); err != nil {
		return RuntimeQueueBatch{}, err
	}
	return RuntimeQueueBatch{Token: wal.Token, Messages: queued}, nil
}

func AcknowledgeRuntimeQueue(id, token string) error {
	runtimeQueueMu.Lock()
	defer runtimeQueueMu.Unlock()

	wal, exists, err := readRuntimeQueueWALLocked(id)
	if err != nil {
		return err
	}
	if !exists {
		completion, completed, err := readRuntimeQueueCompletionLocked(id)
		if err != nil {
			return err
		}
		if completed && token != "" && completion.Token == token {
			return nil
		}
		return errors.New("runtime queue acknowledgment has no matching staged batch")
	}
	if token == "" || token != wal.Token {
		return errors.New("runtime queue acknowledgment token mismatch")
	}
	queued, _, err := readRuntimeQueueLocked(RuntimeQueuePathFor(id))
	if err != nil {
		return err
	}
	if _, prefixErr := runtimeQueuePrefixForWAL(queued, wal); prefixErr != nil {
		completed, completionErr := runtimeQueueWALCompletedLocked(id, wal.Token)
		if completionErr != nil {
			return completionErr
		}
		if !completed {
			return prefixErr
		}
		return removeRuntimeQueueWALLocked(id)
	}
	remaining := queued[len(wal.MessageIDs):]
	var data []byte
	for _, msg := range remaining {
		line, err := jsonMarshalRuntimeMessage(msg)
		if err != nil {
			return err
		}
		data = append(data, line...)
	}
	if err := writeRuntimeQueueJSONLocked(runtimeQueueCompletionPathFor(id), runtimeQueueCompletion{Token: token}); err != nil {
		return err
	}
	if err := runtimeQueuePersist(RuntimeQueuePathFor(id), data, 0o644); err != nil {
		return err
	}
	return removeRuntimeQueueWALLocked(id)
}

func FormatRuntimeMessagesForInjection(msgs []RuntimeQueuedMessage) string {
	if len(msgs) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString("## Queued runtime messages\n\n")
	for i, msg := range msgs {
		if i > 0 {
			out.WriteByte('\n')
		}
		fmt.Fprintf(&out, "%d. %s", i+1, strings.ReplaceAll(msg.Message, "\n", "\n   "))
	}
	return out.String()
}

var ErrRuntimeQueueFull = errors.New("runtime message queue is full")
var ErrRuntimeQueueDeliveryInProgress = errors.New("runtime queue delivery in progress")
var ErrRuntimeQueueTransactionReleased = errors.New("runtime queue transaction released")

const (
	MaxRuntimeQueueMessages = 100
	MaxRuntimeQueueBytes    = 16 << 20
)

var (
	runtimeQueueMu         sync.Mutex
	runtimeQueueRegistryMu sync.Mutex
	runtimeQueueDeliveryMu = make(map[string]*runtimeQueueDeliveryEntry)
	runtimeQueueNewID      = uuid.NewString
	runtimeQueueNow        = time.Now
	runtimeQueuePersist    = writeFileDurable
	runtimeQueueRemove     = os.Remove
	runtimeQueueOpen       = func(path string) (runtimeQueueSidecarFile, error) { return os.Open(path) }
	runtimeQueueStageEnter = func() {}
)

type runtimeQueueSidecarFile interface {
	io.Reader
	Stat() (fs.FileInfo, error)
	Close() error
}

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
	tx, err := BeginRuntimeQueueTransaction(id)
	if err != nil {
		return 0, err
	}
	defer tx.Release()
	return tx.Enqueue(msg)
}

type RuntimeQueueTransaction struct {
	id             string
	release        func()
	stateMu        sync.Mutex
	released       bool
	beforeMutation func()
}

func BeginRuntimeQueueTransaction(id string) (*RuntimeQueueTransaction, error) {
	release, err := lockRuntimeQueueDelivery(id)
	if err != nil {
		return nil, err
	}
	return &RuntimeQueueTransaction{id: id, release: release}, nil
}

func (tx *RuntimeQueueTransaction) Release() {
	if tx == nil {
		return
	}
	tx.stateMu.Lock()
	defer tx.stateMu.Unlock()
	if tx.release != nil {
		tx.release()
		tx.release = nil
		tx.released = true
	}
}

func (tx *RuntimeQueueTransaction) Enqueue(msg string) (depth int, err error) {
	if tx == nil {
		return 0, ErrRuntimeQueueTransactionReleased
	}
	tx.stateMu.Lock()
	defer tx.stateMu.Unlock()
	if tx.released || tx.release == nil {
		return 0, ErrRuntimeQueueTransactionReleased
	}
	if tx.beforeMutation != nil {
		tx.beforeMutation()
	}
	path := RuntimeQueuePathFor(tx.id)

	runtimeQueueMu.Lock()
	defer runtimeQueueMu.Unlock()

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

func (tx *RuntimeQueueTransaction) Discard() error {
	if tx == nil {
		return ErrRuntimeQueueTransactionReleased
	}
	tx.stateMu.Lock()
	defer tx.stateMu.Unlock()
	if tx.released || tx.release == nil {
		return ErrRuntimeQueueTransactionReleased
	}
	if tx.beforeMutation != nil {
		tx.beforeMutation()
	}
	runtimeQueueMu.Lock()
	defer runtimeQueueMu.Unlock()
	return discardRuntimeQueueFilesLocked(tx.id)
}

func RuntimeQueueHasPending(id string) bool {
	return fileHasContent(RuntimeQueuePathFor(id))
}

func PeekRuntimeQueue(id string) ([]RuntimeQueuedMessage, error) {
	runtimeQueueMu.Lock()
	defer runtimeQueueMu.Unlock()
	queued, _, err := readRuntimeQueueLocked(RuntimeQueuePathFor(id))
	return queued, err
}

func DiscardRuntimeQueue(id string) error {
	tx, err := BeginRuntimeQueueDiscard(id)
	if err != nil {
		return err
	}
	defer tx.Release()
	return nil
}

func BeginRuntimeQueueDiscard(id string) (*RuntimeQueueTransaction, error) {
	tx, err := BeginRuntimeQueueTransaction(id)
	if err != nil {
		return nil, err
	}
	if err := tx.Discard(); err != nil {
		tx.Release()
		return nil, err
	}
	return tx, nil
}

func discardRuntimeQueueFilesLocked(id string) error {
	dirsToSync := make(map[string]struct{})
	syncRemovedDirs := func() {
		for dir := range dirsToSync {
			fsyncDir(dir)
		}
	}
	for _, path := range []string{RuntimeQueuePathFor(id), runtimeQueueInflightPathFor(id), runtimeQueueCompletionPathFor(id)} {
		err := os.Remove(path)
		if err == nil {
			dirsToSync[filepath.Dir(path)] = struct{}{}
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			syncRemovedDirs()
			return err
		}
	}
	syncRemovedDirs()
	return nil
}

func TryDiscardRuntimeQueue(id string) error {
	release, acquired, err := tryRuntimeQueueDeliveryLock(id)
	if err != nil {
		return err
	}
	if !acquired {
		return ErrRuntimeQueueDeliveryInProgress
	}
	defer release()
	return discardRuntimeQueueLocked(id)
}

func discardRuntimeQueueLocked(id string) error {
	runtimeQueueMu.Lock()
	defer runtimeQueueMu.Unlock()
	return discardRuntimeQueueFilesLocked(id)
}

type runtimeQueueDeliveryEntry struct {
	mu   sync.Mutex
	refs int
}

func retainRuntimeQueueDeliveryEntry(id string) (string, *runtimeQueueDeliveryEntry) {
	key := sanitizeInboxName(id)
	runtimeQueueRegistryMu.Lock()
	defer runtimeQueueRegistryMu.Unlock()
	entry := runtimeQueueDeliveryMu[key]
	if entry == nil {
		entry = &runtimeQueueDeliveryEntry{}
		runtimeQueueDeliveryMu[key] = entry
	}
	entry.refs++
	return key, entry
}

func releaseRuntimeQueueDeliveryEntry(key string, entry *runtimeQueueDeliveryEntry) {
	runtimeQueueRegistryMu.Lock()
	defer runtimeQueueRegistryMu.Unlock()
	entry.refs--
	if entry.refs == 0 {
		delete(runtimeQueueDeliveryMu, key)
	}
}

func runtimeQueueDeliveryLockPath(id string) string {
	dir, err := runtimeDataPath("runtime-queue-locks")
	if err != nil {
		dir = tempAgentDeckPath("runtime", "runtime-queue-locks")
	}
	return filepath.Join(dir, sanitizeInboxName(id)+".lock")
}

func openRuntimeQueueProcessLock(id string, nonblocking bool) (*os.File, error) {
	path := runtimeQueueDeliveryLockPath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	op := unix.LOCK_EX
	if nonblocking {
		op |= unix.LOCK_NB
	}
	if err := unix.Flock(int(file.Fd()), op); err != nil {
		_ = file.Close()
		if nonblocking && (errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)) {
			return nil, ErrRuntimeQueueDeliveryInProgress
		}
		return nil, err
	}
	return file, nil
}

func lockRuntimeQueueDelivery(id string) (func(), error) {
	key, entry := retainRuntimeQueueDeliveryEntry(id)
	entry.mu.Lock()
	file, err := openRuntimeQueueProcessLock(id, false)
	if err != nil {
		entry.mu.Unlock()
		releaseRuntimeQueueDeliveryEntry(key, entry)
		return nil, err
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
		entry.mu.Unlock()
		releaseRuntimeQueueDeliveryEntry(key, entry)
	}, nil
}

func tryRuntimeQueueDeliveryLock(id string) (func(), bool, error) {
	key, entry := retainRuntimeQueueDeliveryEntry(id)
	if !entry.mu.TryLock() {
		releaseRuntimeQueueDeliveryEntry(key, entry)
		return nil, false, nil
	}
	file, err := openRuntimeQueueProcessLock(id, true)
	if err != nil {
		entry.mu.Unlock()
		releaseRuntimeQueueDeliveryEntry(key, entry)
		if errors.Is(err, ErrRuntimeQueueDeliveryInProgress) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
		entry.mu.Unlock()
		releaseRuntimeQueueDeliveryEntry(key, entry)
	}, true, nil
}

type RuntimeQueueSubmission struct {
	id      string
	token   string
	release func()
	once    sync.Once
}

// BeginRuntimeQueueSubmission validates a staged batch and returns a lease
// that protects the external write through acknowledgment. The caller must
// call Acknowledge after a complete write or Release on every failure path.
func BeginRuntimeQueueSubmission(id, token string) (*RuntimeQueueSubmission, bool, error) {
	release, err := lockRuntimeQueueDelivery(id)
	if err != nil {
		return nil, false, err
	}

	if token == "" {
		return &RuntimeQueueSubmission{id: id, release: release}, true, nil
	}

	batch, err := StageRuntimeQueue(id)
	if err != nil {
		release()
		return nil, false, err
	}
	if batch.Token == "" {
		release()
		return nil, false, nil
	}
	if batch.Token != token {
		release()
		return nil, false, nil
	}
	return &RuntimeQueueSubmission{id: id, token: token, release: release}, true, nil
}

func (s *RuntimeQueueSubmission) Release() {
	if s == nil {
		return
	}
	s.once.Do(s.release)
}

func (s *RuntimeQueueSubmission) Acknowledge() error {
	if s == nil {
		return errors.New("runtime queue submission lease is nil")
	}
	defer s.Release()
	if s.token == "" {
		return nil
	}
	if err := AcknowledgeRuntimeQueue(s.id, s.token); err != nil {
		return fmt.Errorf("acknowledge runtime queue: %w", err)
	}
	return nil
}

func runtimeQueueInflightPathFor(id string) string {
	dir, err := runtimeDataPath("runtime-queue-inflight")
	if err != nil {
		dir = tempAgentDeckPath("runtime", "runtime-queue-inflight")
	}
	return filepath.Join(dir, sanitizeInboxName(id)+".jsonl")
}

func runtimeQueueCompletionPathFor(id string) string {
	dir, err := runtimeDataPath("runtime-queue-completed")
	if err != nil {
		dir = tempAgentDeckPath("runtime", "runtime-queue-completed")
	}
	return filepath.Join(dir, sanitizeInboxName(id)+".json")
}

func readRuntimeQueueWALLocked(id string) (runtimeQueueWAL, bool, error) {
	var wal runtimeQueueWAL
	exists, err := readRuntimeQueueJSONLocked(runtimeQueueInflightPathFor(id), &wal)
	if err != nil {
		return wal, exists, fmt.Errorf("read runtime queue WAL: %w", err)
	}
	if exists && (wal.Token == "" || len(wal.MessageIDs) == 0) {
		return wal, true, errors.New("read runtime queue WAL: invalid token or message IDs")
	}
	for _, messageID := range wal.MessageIDs {
		if messageID == "" {
			return wal, true, errors.New("read runtime queue WAL: empty message ID")
		}
	}
	return wal, exists, nil
}

func readRuntimeQueueCompletionLocked(id string) (runtimeQueueCompletion, bool, error) {
	var completion runtimeQueueCompletion
	exists, err := readRuntimeQueueJSONLocked(runtimeQueueCompletionPathFor(id), &completion)
	if err != nil {
		return completion, exists, fmt.Errorf("read runtime queue completion: %w", err)
	}
	if exists && completion.Token == "" {
		return completion, true, errors.New("read runtime queue completion: empty token")
	}
	return completion, exists, nil
}

func runtimeQueueWALCompletedLocked(id, token string) (bool, error) {
	completion, exists, err := readRuntimeQueueCompletionLocked(id)
	if err != nil {
		return false, err
	}
	return exists && completion.Token == token, nil
}

func removeRuntimeQueueWALLocked(id string) error {
	path := runtimeQueueInflightPathFor(id)
	if err := runtimeQueueRemove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	fsyncDir(filepath.Dir(path))
	return nil
}

func readRuntimeQueueJSONLocked(path string, value any) (bool, error) {
	f, err := runtimeQueueOpen(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return true, err
	}
	if info.Size() > MaxRuntimeQueueBytes {
		return true, fmt.Errorf("runtime queue sidecar %s exceeds %d bytes", path, MaxRuntimeQueueBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(f, MaxRuntimeQueueBytes+1))
	if err != nil {
		return true, err
	}
	if len(raw) > MaxRuntimeQueueBytes {
		return true, fmt.Errorf("runtime queue sidecar %s exceeds %d bytes", path, MaxRuntimeQueueBytes)
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return true, errors.New("unterminated JSON record")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return true, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return true, errors.New("multiple JSON records")
	}
	return true, nil
}

func writeRuntimeQueueJSONLocked(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return runtimeQueuePersist(path, append(raw, '\n'), 0o644)
}

func runtimeQueuePrefixForWAL(queued []RuntimeQueuedMessage, wal runtimeQueueWAL) ([]RuntimeQueuedMessage, error) {
	if len(queued) < len(wal.MessageIDs) {
		return nil, errors.New("active runtime queue is shorter than staged prefix")
	}
	for i, messageID := range wal.MessageIDs {
		if queued[i].ID != messageID {
			return nil, fmt.Errorf("active runtime queue prefix mismatch at message %d", i+1)
		}
	}
	return append([]RuntimeQueuedMessage(nil), queued[:len(wal.MessageIDs)]...), nil
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
