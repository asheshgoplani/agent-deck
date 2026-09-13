package session

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

var spawnOwnerSignal = syscall.Kill

type spawnGuardOwner struct {
	Version int    `json:"version"`
	PID     int    `json:"pid"`
	Nonce   string `json:"nonce"`
}

func decodeSpawnProtocol(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing protocol data")
	}
	return nil
}

// This declaration is an operator-established quiescent upgrade boundary,
// never proof that no legacy executable can be invoked later.
func completionProtocolActive() bool {
	dir, err := resolveLocksDirForSpawnLock()
	if err != nil {
		return false
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	data, _, err := readSpawnProtocolFile(filepath.Join(dir, "completion-protocol.json"))
	if err != nil {
		return false
	}
	var declaration struct {
		Version          int    `json:"version"`
		QuiescentUpgrade bool   `json:"quiescent_upgrade_declared"`
		Namespace        string `json:"locks_namespace"`
	}
	return decodeSpawnProtocol(data, &declaration) == nil && declaration.Version == 1 &&
		declaration.QuiescentUpgrade && declaration.Namespace == canonical
}

func readSpawnProtocolFile(path string) ([]byte, os.FileInfo, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("nonregular spawn protocol")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return nil, nil, fmt.Errorf("invalid spawn protocol size")
	}
	return data, info, nil
}

func readSpawnGuardOwner(path string) (spawnGuardOwner, os.FileInfo, error) {
	data, info, err := readSpawnProtocolFile(path)
	var owner spawnGuardOwner
	if err != nil {
		return owner, info, err
	}
	if decodeSpawnProtocol(data, &owner) != nil || owner.Version != 1 || owner.PID <= 0 || len(owner.Nonce) != 32 {
		return owner, info, fmt.Errorf("legacy or ambiguous spawn ownership")
	}
	if _, err := hex.DecodeString(owner.Nonce); err != nil {
		return owner, info, err
	}
	return owner, info, nil
}

// Every upgraded acquisition, reclaim and release holds this permanent inode.
// Legacy controllers that ignore it must be quiesced before activation.
func tryGuardedSpawnOwnership(instanceID string) (func(), bool, error) {
	path, err := instanceSpawnLockPath(instanceID)
	if err != nil {
		return nil, false, err
	}
	guard, err := os.OpenFile(path+".guard", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, false, err
	}
	closeGuard := func() { _ = syscall.Flock(int(guard.Fd()), syscall.LOCK_UN); _ = guard.Close() }
	info, err := guard.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = guard.Close()
		return nil, false, fmt.Errorf("invalid spawn guard")
	}
	if err := syscall.Flock(int(guard.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = guard.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, err
	}
	owner, markerInfo, readErr := readSpawnGuardOwner(path)
	if readErr == nil {
		if !completionProtocolActive() {
			closeGuard()
			return nil, false, nil
		}
		// Age, EPERM, PID reuse and every unknown outcome retain ownership.
		if !errors.Is(spawnOwnerSignal(owner.PID, 0), syscall.ESRCH) {
			closeGuard()
			return nil, false, nil
		}
		current, err := os.Lstat(path)
		if err != nil || !os.SameFile(markerInfo, current) {
			closeGuard()
			return nil, false, nil
		}
		if err := os.Remove(path); err != nil {
			closeGuard()
			return nil, false, err
		}
	} else if !os.IsNotExist(readErr) {
		closeGuard()
		return nil, false, nil
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		closeGuard()
		return nil, false, err
	}
	owned := spawnGuardOwner{Version: 1, PID: os.Getpid(), Nonce: hex.EncodeToString(token[:])}
	data, err := json.Marshal(owned)
	if err != nil {
		closeGuard()
		return nil, false, err
	}
	marker, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		closeGuard()
		return nil, false, err
	}
	ownedInfo, statErr := marker.Stat()
	_, writeErr := marker.Write(data)
	syncErr := marker.Sync()
	closeErr := marker.Close()
	if statErr != nil || writeErr != nil || syncErr != nil || closeErr != nil {
		// Preserve an ambiguous partial marker for operator inspection.
		closeGuard()
		return nil, false, fmt.Errorf("persist spawn ownership failed")
	}
	released := false
	return func() {
		if released {
			return
		}
		released = true
		current, currentInfo, err := readSpawnGuardOwner(path)
		if err == nil && current == owned && os.SameFile(ownedInfo, currentInfo) {
			_ = os.Remove(path)
		}
		closeGuard()
	}, true, nil
}
