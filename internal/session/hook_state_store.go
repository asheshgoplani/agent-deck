package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var errHookStateDecode = errors.New("decode hook state")

// WriteHookState applies one event under a per-instance file lock and replaces
// the status document atomically with a randomized same-directory temp file.
func WriteHookState(instanceID string, event HookStateEvent) error {
	return writeHookStateAt(GetHooksDir(), instanceID, event)
}

func writeHookStateAt(hooksDir, instanceID string, event HookStateEvent) error {
	if err := validateHookInstanceID(instanceID); err != nil {
		return err
	}
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		return fmt.Errorf("create hooks directory: %w", err)
	}

	lockPath := filepath.Join(hooksDir, "."+instanceID+".lock")
	lockFD, err := unix.Open(lockPath, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("open hook state lock: %w", err)
	}
	lockFile := os.NewFile(uintptr(lockFD), lockPath)
	defer lockFile.Close()
	if err := unix.Flock(lockFD, unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock hook state: %w", err)
	}
	defer unix.Flock(lockFD, unix.LOCK_UN) //nolint:errcheck

	statePath := filepath.Join(hooksDir, instanceID+".json")
	previous, err := readHookStateAt(statePath)
	if err != nil && !os.IsNotExist(err) {
		if !errors.Is(err, errHookStateDecode) {
			return fmt.Errorf("read hook state: %w", err)
		}
		previous = HookStateDocument{}
	}
	next, err := AdvanceHookState(previous, event)
	if err != nil {
		return err
	}
	data, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("marshal hook state: %w", err)
	}

	tmp, err := os.CreateTemp(hooksDir, "."+instanceID+".*.tmp")
	if err != nil {
		return fmt.Errorf("create hook state temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("secure hook state temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write hook state temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync hook state temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close hook state temp: %w", err)
	}
	if err := os.Rename(tmpPath, statePath); err != nil {
		return fmt.Errorf("replace hook state: %w", err)
	}
	cleanup = false

	if dir, err := os.Open(hooksDir); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func readHookStateAt(path string) (HookStateDocument, error) {
	data, err := readStatusFileNoFollow(path)
	if err != nil {
		return HookStateDocument{}, err
	}
	var state HookStateDocument
	if err := json.Unmarshal(data, &state); err != nil {
		return HookStateDocument{}, fmt.Errorf("%w: %w", errHookStateDecode, err)
	}
	return state, nil
}
