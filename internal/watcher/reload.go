package watcher

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// `agent-deck watcher reload` asks the profile's engine owner to apply the
// watchers and routes now (#2739). The owner applies changes on its own
// every DefaultEngineRetryInterval anyway; a reload also retries watchers
// whose adapter is not running, and is acknowledged. The request and the
// acknowledgement are files in the profile runtime dir, next to the engine
// owner lock: no signal reaches a process, so an owner from an agent-deck
// older than reload ignores the request and the CLI reports that no engine
// answered.
const (
	ReloadRequestFile = "watcher-reload.request"
	ReloadAckFile     = "watcher-reload.ack"
)

// ReloadReport is what an engine owner applied for one reload request: the
// acknowledgement `watcher reload` waits for.
type ReloadReport struct {
	// Request is the token of the request this answers.
	Request string `json:"request"`
	PID     int    `json:"pid"`
	// StartedAt is when the pass began, before it read the request: a
	// request made before it is covered. AppliedAt is when it ended.
	StartedAt time.Time `json:"started_at"`
	AppliedAt time.Time `json:"applied_at"`
	// Started, Stopped and Restarted name the watchers whose adapter the
	// engine added, removed and replaced. Failed is "<name>: <error>" for an
	// adapter whose Setup failed; the watcher stays listed, unhealthy.
	Started   []string `json:"started"`
	Stopped   []string `json:"stopped"`
	Restarted []string `json:"restarted"`
	Failed    []string `json:"failed"`
	Unchanged int      `json:"unchanged"`
	// RoutesReloaded reports that clients.json changed and the router took
	// it; RoutesError that it did not parse, so the routes stayed.
	RoutesReloaded bool   `json:"routes_reloaded"`
	RoutesError    string `json:"routes_error,omitempty"`
	// Error is set when the engine could not read the watchers to apply.
	Error string `json:"error,omitempty"`
}

// Changed reports whether the report changed anything.
func (r *ReloadReport) Changed() bool {
	return len(r.Started)+len(r.Stopped)+len(r.Restarted)+len(r.Failed) > 0 || r.RoutesReloaded
}

func reloadPath(profile, name string) (string, error) {
	lock, err := EngineLockPath(profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(lock), name), nil
}

// RequestReload records a new reload request for profile's engine owner
// and returns its token, which the owner's ReloadReport carries.
func RequestReload(profile string) (string, error) {
	path, err := reloadPath(profile, ReloadRequestFile)
	if err != nil {
		return "", err
	}
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	if err := writeFileAtomic(path, []byte(token+"\n")); err != nil {
		return "", fmt.Errorf("write reload request: %w", err)
	}
	return token, nil
}

// readReloadRequest returns the token of the latest reload request, "" when
// there is none.
func readReloadRequest(profile string) string {
	path, err := reloadPath(profile, ReloadRequestFile)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ReadReloadReport returns the owner's latest acknowledgement, nil when there
// is none.
func ReadReloadReport(profile string) (*ReloadReport, error) {
	path, err := reloadPath(profile, ReloadAckFile)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r ReloadReport
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("parse reload acknowledgement: %w", err)
	}
	return &r, nil
}

func writeReloadReport(profile string, r *ReloadReport) error {
	path, err := reloadPath(profile, ReloadAckFile)
	if err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b)
}

// writeFileAtomic writes data to path through a temporary file in the same
// directory and a rename, so a reader never sees half of it.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// EngineOwnerPID reports whether a process holds profile's engine owner
// lock, and its pid when the lock file records one. It only probes the lock
// (a shared flock, released at once) and never writes it.
func EngineOwnerPID(profile string) (pid int, running bool, err error) {
	path, err := EngineLockPath(profile)
	if err != nil {
		return 0, false, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return readEngineOwnerPID(path), true, nil
		}
		return 0, false, err
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return 0, false, nil
}
