package session

import (
	"crypto/rand"
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
	"time"
)

const (
	codexSubmissionMarkerVersion = 1
	codexSubmissionMarkerMaxSize = 8 << 10

	CodexSubmissionPhasePrepared  = "prepared"
	CodexSubmissionPhaseSubmitted = "submitted"
	codexSubmissionPhaseAmbiguous = "transport_ambiguous"
)

// CodexSubmissionMarker is the content-free durable handoff between
// cooperating session-send processes. It records only which acceptance fence
// a send owns; prompts, responses, and their hashes never enter this file.
type CodexSubmissionMarker struct {
	Version             int       `json:"version"`
	InstanceID          string    `json:"instance_id"`
	CodexSessionID      string    `json:"codex_session_id"`
	AttemptID           string    `json:"attempt_id"`
	PriorTurnGeneration string    `json:"prior_turn_generation"`
	Phase               string    `json:"phase"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// PrepareCodexSubmissionMarker durably publishes an unresolved attempt before
// any bytes are sent to the pane. The caller must hold the matching acceptance
// lock from before reconciliation until this write completes.
func PrepareCodexSubmissionMarker(instanceID, codexSessionID, priorGeneration string, now time.Time) (*CodexSubmissionMarker, error) {
	instanceID = strings.TrimSpace(instanceID)
	codexSessionID = strings.TrimSpace(codexSessionID)
	if instanceID == "" || len(instanceID) > 512 {
		return nil, fmt.Errorf("Codex submission marker: invalid instance id")
	}
	if err := validateExactSessionID(codexSessionID); err != nil {
		return nil, fmt.Errorf("Codex submission marker: invalid session identity: %w", err)
	}
	if err := validateCodexMarkerGeneration(codexSessionID, priorGeneration, true); err != nil {
		return nil, err
	}
	attemptID, err := newCodexSubmissionAttemptID()
	if err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	marker := &CodexSubmissionMarker{
		Version:             codexSubmissionMarkerVersion,
		InstanceID:          instanceID,
		CodexSessionID:      codexSessionID,
		AttemptID:           attemptID,
		PriorTurnGeneration: priorGeneration,
		Phase:               CodexSubmissionPhasePrepared,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if err := writeCodexSubmissionMarker(marker, true); err != nil {
		return nil, err
	}
	return marker, nil
}

// MarkSubmitted records positive transport-level submission evidence. A crash
// before this update safely leaves the more conservative prepared phase.
func (m *CodexSubmissionMarker) MarkSubmitted(now time.Time) error {
	return m.updatePhase(CodexSubmissionPhaseSubmitted, now)
}

// MarkTransportAmbiguous records that transport was attempted without exact
// submission evidence. It deliberately remains unresolved until a new rollout
// generation appears or an operator proves transport never occurred.
func (m *CodexSubmissionMarker) MarkTransportAmbiguous(now time.Time) error {
	return m.updatePhase(codexSubmissionPhaseAmbiguous, now)
}

func (m *CodexSubmissionMarker) updatePhase(phase string, now time.Time) error {
	if m == nil {
		return fmt.Errorf("Codex submission marker: nil marker")
	}
	if now.IsZero() {
		now = time.Now()
	}
	next := *m
	next.Phase = phase
	next.UpdatedAt = now.UTC()
	if err := writeCodexSubmissionMarker(&next, false); err != nil {
		return err
	}
	*m = next
	return nil
}

// ReconcileCodexSubmissionMarker resolves a crashed or completed prior sender
// while the matching acceptance lock is held. A different exact generation is
// assigned to that prior attempt and makes its marker safe to remove. With no
// new generation, delivery remains unknowable and all later sends fail closed.
func ReconcileCodexSubmissionMarker(instanceID, codexSessionID, currentGeneration string) (string, error) {
	marker, found, err := readCodexSubmissionMarker(codexSessionID)
	if err != nil || !found {
		return "", err
	}
	if marker.InstanceID != strings.TrimSpace(instanceID) || marker.CodexSessionID != strings.TrimSpace(codexSessionID) {
		return "", fmt.Errorf("Codex submission marker ownership does not match this instance")
	}
	if currentGeneration == marker.PriorTurnGeneration {
		path, pathErr := codexSubmissionMarkerPath(codexSessionID)
		if pathErr != nil {
			return "", pathErr
		}
		return "", fmt.Errorf(
			"unresolved Codex submission %s at %s: no newer rollout generation is durable; inspect the exact Codex rollout and target pane, then manually remove this marker only if no turn was submitted; refusing a second send",
			marker.AttemptID, path,
		)
	}
	if err := validateCodexMarkerGeneration(marker.CodexSessionID, currentGeneration, false); err != nil {
		return "", fmt.Errorf("cannot reconcile Codex submission marker: %w", err)
	}
	if err := ClearCodexSubmissionMarker(marker); err != nil {
		return "", err
	}
	return currentGeneration, nil
}

// ClearCodexSubmissionMarker removes only the exact attempt supplied by the
// caller. Callers use this after an exact accepted generation or definitive
// proof that transport never occurred, never after an ambiguous failure.
func ClearCodexSubmissionMarker(marker *CodexSubmissionMarker) error {
	if err := validateCodexSubmissionMarker(marker); err != nil {
		return err
	}
	current, found, err := readCodexSubmissionMarker(marker.CodexSessionID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("Codex submission marker %s is missing", marker.AttemptID)
	}
	if current.InstanceID != marker.InstanceID || current.CodexSessionID != marker.CodexSessionID || current.AttemptID != marker.AttemptID {
		return fmt.Errorf("Codex submission marker ownership changed; refusing removal")
	}
	path, err := codexSubmissionMarkerPath(marker.CodexSessionID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove Codex submission marker: %w", err)
	}
	if err := fsyncDirStrict(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync Codex submission marker directory: %w", err)
	}
	return nil
}

func newCodexSubmissionAttemptID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create Codex submission attempt id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func codexSubmissionMarkerDir() (string, error) {
	locks, err := resolveLocksDirForSpawnLock()
	if err != nil {
		return "", fmt.Errorf("resolve Codex submission marker directory: %w", err)
	}
	dir := filepath.Join(locks, "codex-submissions")
	_, statErr := os.Lstat(dir)
	created := os.IsNotExist(statErr)
	if statErr != nil && !created {
		return "", statErr
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create Codex submission marker directory: %w", err)
	}
	if created {
		if err := fsyncDirStrict(filepath.Dir(dir)); err != nil {
			return "", fmt.Errorf("sync Codex submission marker parent directory: %w", err)
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if err := validatePrivateCodexMarkerPath(info, true); err != nil {
		return "", fmt.Errorf("unsafe Codex submission marker directory: %w", err)
	}
	return dir, nil
}

func codexSubmissionMarkerPath(codexSessionID string) (string, error) {
	codexSessionID = strings.TrimSpace(codexSessionID)
	if err := validateExactSessionID(codexSessionID); err != nil {
		return "", fmt.Errorf("Codex submission marker: invalid session identity: %w", err)
	}
	dir, err := codexSubmissionMarkerDir()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(codexSessionID))
	return filepath.Join(dir, hex.EncodeToString(digest[:])+".json"), nil
}

func readCodexSubmissionMarker(codexSessionID string) (*CodexSubmissionMarker, bool, error) {
	path, err := codexSubmissionMarkerPath(codexSessionID)
	if err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("open Codex submission marker: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if err := validatePrivateCodexMarkerPath(info, false); err != nil {
		return nil, false, fmt.Errorf("unsafe Codex submission marker: %w", err)
	}
	if info.Size() > codexSubmissionMarkerMaxSize {
		return nil, false, fmt.Errorf("Codex submission marker exceeds %d bytes", codexSubmissionMarkerMaxSize)
	}
	decoder := json.NewDecoder(io.LimitReader(f, codexSubmissionMarkerMaxSize+1))
	decoder.DisallowUnknownFields()
	var marker CodexSubmissionMarker
	if err := decoder.Decode(&marker); err != nil {
		return nil, false, fmt.Errorf("parse Codex submission marker: %w", err)
	}
	var extra interface{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, false, fmt.Errorf("parse Codex submission marker: trailing data")
	}
	if err := validateCodexSubmissionMarker(&marker); err != nil {
		return nil, false, err
	}
	if marker.CodexSessionID != strings.TrimSpace(codexSessionID) {
		return nil, false, fmt.Errorf("Codex submission marker session identity does not match its path")
	}
	return &marker, true, nil
}

func writeCodexSubmissionMarker(marker *CodexSubmissionMarker, create bool) error {
	if err := validateCodexSubmissionMarker(marker); err != nil {
		return err
	}
	path, err := codexSubmissionMarkerPath(marker.CodexSessionID)
	if err != nil {
		return err
	}
	current, found, err := readCodexSubmissionMarker(marker.CodexSessionID)
	if err != nil {
		return err
	}
	if create && found {
		return fmt.Errorf("unresolved Codex submission marker already exists")
	}
	if !create && (!found || current.InstanceID != marker.InstanceID || current.AttemptID != marker.AttemptID) {
		return fmt.Errorf("Codex submission marker ownership changed; refusing update")
	}
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".codex-submission-*.tmp")
	if err != nil {
		return fmt.Errorf("create Codex submission marker temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync Codex submission marker: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("publish Codex submission marker: %w", err)
	}
	if err := fsyncDirStrict(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync Codex submission marker directory: %w", err)
	}
	return nil
}

func validateCodexSubmissionMarker(marker *CodexSubmissionMarker) error {
	if marker == nil || marker.Version != codexSubmissionMarkerVersion {
		return fmt.Errorf("unsupported Codex submission marker version")
	}
	if strings.TrimSpace(marker.InstanceID) == "" || marker.InstanceID != strings.TrimSpace(marker.InstanceID) || len(marker.InstanceID) > 512 {
		return fmt.Errorf("invalid Codex submission marker instance identity")
	}
	if err := validateExactSessionID(marker.CodexSessionID); err != nil || marker.CodexSessionID != strings.TrimSpace(marker.CodexSessionID) {
		return fmt.Errorf("invalid Codex submission marker session identity")
	}
	attempt, err := hex.DecodeString(marker.AttemptID)
	if err != nil || len(attempt) != 16 {
		return fmt.Errorf("invalid Codex submission marker attempt identity")
	}
	if err := validateCodexMarkerGeneration(marker.CodexSessionID, marker.PriorTurnGeneration, true); err != nil {
		return err
	}
	switch marker.Phase {
	case CodexSubmissionPhasePrepared, CodexSubmissionPhaseSubmitted, codexSubmissionPhaseAmbiguous:
	default:
		return fmt.Errorf("invalid Codex submission marker lifecycle phase")
	}
	if marker.CreatedAt.IsZero() || marker.UpdatedAt.IsZero() || marker.UpdatedAt.Before(marker.CreatedAt) {
		return fmt.Errorf("invalid Codex submission marker timestamps")
	}
	return nil
}

func validateCodexMarkerGeneration(codexSessionID, generation string, allowEmpty bool) error {
	if generation == "" && allowEmpty {
		return nil
	}
	if generation == "" || len(generation) > 2048 || strings.ContainsRune(generation, '\x00') ||
		!strings.HasPrefix(generation, codexSessionID+":") || len(generation) == len(codexSessionID)+1 {
		return fmt.Errorf("invalid Codex submission marker turn generation")
	}
	return nil
}

func validatePrivateCodexMarkerPath(info os.FileInfo, wantDir bool) error {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() != wantDir || (!wantDir && !info.Mode().IsRegular()) {
		return fmt.Errorf("path has the wrong file type")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("path permissions %o are not private", info.Mode().Perm())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("path is not owned by the current user")
	}
	return nil
}
