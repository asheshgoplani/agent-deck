package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

const pushVAPIDKeysFileName = "web_push_vapid_keys.json"

type pushVAPIDKeysFile struct {
	PublicKey  string    `json:"publicKey"`
	PrivateKey string    `json:"privateKey"`
	Subject    string    `json:"subject,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// EnsurePushVAPIDKeys returns a persisted VAPID keypair for the given profile.
// If no key file exists yet, it generates one via webpush.GenerateVAPIDKeys().
func EnsurePushVAPIDKeys(profile, subject string) (publicKey, privateKey string, generated bool, err error) {
	profileDir, err := session.GetProfileDir(session.GetEffectiveProfile(profile))
	if err != nil {
		return "", "", false, fmt.Errorf("resolve profile dir: %w", err)
	}

	keysPath := filepath.Join(profileDir, pushVAPIDKeysFileName)
	subject = strings.TrimSpace(subject)

	if file, loadedErr := loadPushVAPIDKeysFile(keysPath); loadedErr == nil {
		changed := false
		if subject != "" && strings.TrimSpace(file.Subject) != subject {
			file.Subject = subject
			file.UpdatedAt = time.Now().UTC()
			changed = true
		}
		if changed {
			if writeErr := writePushVAPIDKeysFile(keysPath, file); writeErr != nil {
				return "", "", false, writeErr
			}
		}
		return file.PublicKey, file.PrivateKey, false, nil
	} else if !errors.Is(loadedErr, os.ErrNotExist) {
		return "", "", false, loadedErr
	}

	privateKey, publicKey, err = webpush.GenerateVAPIDKeys()
	if err != nil {
		return "", "", false, fmt.Errorf("generate vapid keypair: %w", err)
	}

	now := time.Now().UTC()
	file := &pushVAPIDKeysFile{
		PublicKey:  strings.TrimSpace(publicKey),
		PrivateKey: strings.TrimSpace(privateKey),
		Subject:    subject,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := writePushVAPIDKeysFile(keysPath, file); err != nil {
		return "", "", false, err
	}

	return file.PublicKey, file.PrivateKey, true, nil
}

func loadPushVAPIDKeysFile(path string) (*pushVAPIDKeysFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("read vapid keys file: %w", err)
	}

	var file pushVAPIDKeysFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse vapid keys file: %w", err)
	}
	file.PublicKey = strings.TrimSpace(file.PublicKey)
	file.PrivateKey = strings.TrimSpace(file.PrivateKey)
	file.Subject = strings.TrimSpace(file.Subject)
	if file.PublicKey == "" || file.PrivateKey == "" {
		return nil, fmt.Errorf("vapid keys file is missing required keys")
	}
	return &file, nil
}

func writePushVAPIDKeysFile(path string, file *pushVAPIDKeysFile) error {
	if file == nil {
		return fmt.Errorf("vapid keys payload is nil")
	}
	if strings.TrimSpace(file.PublicKey) == "" || strings.TrimSpace(file.PrivateKey) == "" {
		return fmt.Errorf("vapid keys payload is missing key values")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir vapid dir: %w", err)
	}

	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal vapid keys: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write temp vapid keys: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename vapid keys file: %w", err)
	}
	return nil
}
