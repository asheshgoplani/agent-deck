package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// codexAcceptanceGates serializes goroutines in this process. Advisory flock
// supplies the matching exclusion across separate agent-deck processes.
var codexAcceptanceGates sync.Map // map[codex session id]chan struct{}

// CodexAcceptanceLock protects one Codex session's fence-to-generation
// acceptance window. Release is idempotent.
type CodexAcceptanceLock struct {
	gate chan struct{}
	file *os.File
	once sync.Once
}

// Release drops the host lock and then lets the next local waiter proceed.
func (l *CodexAcceptanceLock) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.file != nil {
			_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
			_ = l.file.Close()
		}
		l.gate <- struct{}{}
	})
}

// AcquireCodexAcceptanceLock obtains bounded, per-session exclusion for the
// short window from rollout fence capture through observation of the accepted
// generation. The lock file contains no request or response data.
func AcquireCodexAcceptanceLock(codexSessionID string, timeout time.Duration) (*CodexAcceptanceLock, error) {
	codexSessionID = strings.TrimSpace(codexSessionID)
	if codexSessionID == "" {
		return nil, fmt.Errorf("Codex acceptance lock: empty session id")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("Codex acceptance lock: timeout must be positive")
	}
	deadline := time.Now().Add(timeout)

	created := make(chan struct{}, 1)
	created <- struct{}{}
	gateValue, _ := codexAcceptanceGates.LoadOrStore(codexSessionID, created)
	gate := gateValue.(chan struct{})
	if err := takeCodexAcceptanceGate(gate, deadline); err != nil {
		return nil, err
	}

	path, err := codexAcceptanceLockPath(codexSessionID)
	if err != nil {
		gate <- struct{}{}
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		gate <- struct{}{}
		return nil, fmt.Errorf("open Codex acceptance lock: %w", err)
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &CodexAcceptanceLock{gate: gate, file: f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = f.Close()
			gate <- struct{}{}
			return nil, fmt.Errorf("flock Codex acceptance lock: %w", err)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			_ = f.Close()
			gate <- struct{}{}
			return nil, fmt.Errorf("Codex acceptance lock timed out after %s", timeout)
		}
		delay := 50 * time.Millisecond
		if remaining < delay {
			delay = remaining
		}
		time.Sleep(delay)
	}
}

func takeCodexAcceptanceGate(gate chan struct{}, deadline time.Time) error {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return fmt.Errorf("Codex acceptance lock timed out")
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-gate:
		return nil
	case <-timer.C:
		return fmt.Errorf("Codex acceptance lock timed out")
	}
}

func codexAcceptanceLockPath(codexSessionID string) (string, error) {
	locks, err := resolveLocksDirForSpawnLock()
	if err != nil {
		return "", fmt.Errorf("resolve locks dir for Codex acceptance lock: %w", err)
	}
	if err := os.MkdirAll(locks, 0o700); err != nil {
		return "", fmt.Errorf("create locks dir for Codex acceptance lock: %w", err)
	}
	return filepath.Join(locks, "codex-acceptance-"+spawnLockSafeID(codexSessionID)+".lock"), nil
}
