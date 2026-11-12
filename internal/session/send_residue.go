package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	sendResidueVersion = 1
	sendResidueMaxSize = 4 << 10
	// SendResidueMaxAge is how long a verified residue record may AUTHORISE
	// a later send to remove the stuck composer text it describes.
	SendResidueMaxAge = 24 * time.Hour
	// SendResidueKeepAge is how long a record is kept to NAME a stuck
	// composer text's origin and age in a refusal. Older records are
	// ignored and removed.
	SendResidueKeepAge = 7 * 24 * time.Hour
)

// SendResidue records that a `session send` typed its message, could not
// submit it and could not take it back: the text is still in the target's
// composer. It holds hashes only, never the message or the draft text. A
// later send uses it to recognise that exact draft as agent-deck's own
// (draft_sha256 must match the composer's normalized draft exactly), so a
// human's draft can never match.
type SendResidue struct {
	Version    int    `json:"version"`
	InstanceID string `json:"instance_id"`
	// DraftSHA256 is the hex sha256 of the normalized composer draft at the
	// moment the failed send stopped.
	DraftSHA256 string `json:"draft_sha256"`
	DraftLen    int    `json:"draft_len"`
	// MessageSHA256 is the hex sha256 of the message the send typed.
	MessageSHA256 string `json:"message_sha256"`
	// Verified means the draft was positively verified as only the failed
	// send's own text, typed into a composer verified empty before it.
	Verified bool `json:"verified"`
	// Sender is the sending session's id when known, else "".
	Sender    string    `json:"sender,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// SendResidueHash is the hex sha256 residue records use for drafts and
// messages.
func SendResidueHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// AuthorizesClear reports whether r is young enough (SendResidueMaxAge) to
// authorise removing the text it describes at now.
func (r SendResidue) AuthorizesClear(now time.Time) bool {
	age := now.Sub(r.CreatedAt)
	return age >= 0 && age <= SendResidueMaxAge
}

// Retained reports whether r is still kept (SendResidueKeepAge) to name the
// text's origin and age at now.
func (r SendResidue) Retained(now time.Time) bool {
	age := now.Sub(r.CreatedAt)
	return age >= 0 && age <= SendResidueKeepAge
}

// FileSendResidueStore keeps one residue record per target instance under
// the locks directory (send-residue/<sha256(instance id)>.json, directory
// 0700, files 0600), next to the Codex submission markers.
type FileSendResidueStore struct{}

func sendResidueDir() (string, error) {
	locks, err := resolveLocksDirForSpawnLock()
	if err != nil {
		return "", fmt.Errorf("resolve send residue directory: %w", err)
	}
	dir := filepath.Join(locks, "send-residue")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create send residue directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("stat send residue directory: %w", err)
	}
	if err := validatePrivateCodexMarkerPath(info, true); err != nil {
		return "", fmt.Errorf("unsafe send residue directory: %w", err)
	}
	return dir, nil
}

func sendResiduePath(instanceID string) (string, error) {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return "", fmt.Errorf("send residue: empty instance id")
	}
	dir, err := sendResidueDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, SendResidueHash(instanceID)+".json"), nil
}

// Load returns the record for instanceID. A missing, dropped (older than
// SendResidueKeepAge, then also removed) or foreign record reports false.
// A returned record may still be too old to authorise a clear
// (AuthorizesClear).
func (FileSendResidueStore) Load(instanceID string) (SendResidue, bool, error) {
	path, err := sendResiduePath(instanceID)
	if err != nil {
		return SendResidue{}, false, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if os.IsNotExist(err) {
		return SendResidue{}, false, nil
	}
	if err != nil {
		return SendResidue{}, false, fmt.Errorf("open send residue record: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return SendResidue{}, false, fmt.Errorf("stat send residue record: %w", err)
	}
	if err := validatePrivateCodexMarkerPath(info, false); err != nil {
		return SendResidue{}, false, fmt.Errorf("unsafe send residue record: %w", err)
	}
	var r SendResidue
	if err := json.NewDecoder(io.LimitReader(f, sendResidueMaxSize)).Decode(&r); err != nil {
		return SendResidue{}, false, fmt.Errorf("decode send residue record: %w", err)
	}
	if r.Version != sendResidueVersion || r.InstanceID != strings.TrimSpace(instanceID) {
		return SendResidue{}, false, nil
	}
	if !r.Retained(time.Now()) {
		_ = os.Remove(path)
		return SendResidue{}, false, nil
	}
	return r, true, nil
}

// Save writes r as instanceID's record, replacing any earlier one.
func (FileSendResidueStore) Save(instanceID string, r SendResidue) error {
	path, err := sendResiduePath(instanceID)
	if err != nil {
		return err
	}
	r.Version = sendResidueVersion
	r.InstanceID = strings.TrimSpace(instanceID)
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encode send residue record: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".send-residue-*.tmp")
	if err != nil {
		return fmt.Errorf("create send residue temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod send residue temp file: %w", err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write send residue record: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close send residue record: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("publish send residue record: %w", err)
	}
	return nil
}

// Clear removes instanceID's record; a missing record is not an error.
func (FileSendResidueStore) Clear(instanceID string) error {
	path, err := sendResiduePath(instanceID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove send residue record: %w", err)
	}
	return nil
}
