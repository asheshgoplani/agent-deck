package openclaw

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DeviceIdentity holds the Ed25519 keypair and derived device ID.
type DeviceIdentity struct {
	Version       int    `json:"version"`
	DeviceID      string `json:"deviceId"`
	PublicKeyPEM  string `json:"publicKeyPem"`
	PrivateKeyPEM string `json:"privateKeyPem"`
	CreatedAtMs   int64  `json:"createdAtMs"`
}

// DeviceTokenStore persists device tokens received from pairing.
type DeviceTokenStore struct {
	Version  int                       `json:"version"`
	DeviceID string                    `json:"deviceId"`
	Tokens   map[string]DeviceTokenRef `json:"tokens"`
}

// DeviceTokenRef is a stored token for a specific role.
type DeviceTokenRef struct {
	Token       string `json:"token"`
	Role        string `json:"role"`
	Scopes      []string `json:"scopes"`
	UpdatedAtMs int64  `json:"updatedAtMs"`
}

// DeviceConnect is the device identity field sent in the connect request.
type DeviceConnect struct {
	ID        string `json:"id"`
	PublicKey string `json:"publicKey"`
	Signature string `json:"signature"`
	SignedAt  int64  `json:"signedAt"`
	Nonce     string `json:"nonce"`
}

// DeviceDir returns the directory for device identity files.
func DeviceDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agent-deck", "openclaw")
}

// LoadOrCreateIdentity loads an existing device identity or generates a new one.
func LoadOrCreateIdentity() (*DeviceIdentity, error) {
	dir := DeviceDir()
	path := filepath.Join(dir, "device.json")

	data, err := os.ReadFile(path)
	if err == nil {
		var id DeviceIdentity
		if err := json.Unmarshal(data, &id); err == nil && id.Version == 1 {
			// Verify deviceId matches public key
			derived, err := deriveDeviceID(id.PublicKeyPEM)
			if err == nil && derived == id.DeviceID {
				return &id, nil
			}
			// Fix stale deviceId
			id.DeviceID = derived
			_ = saveIdentity(&id)
			return &id, nil
		}
	}

	// Generate new identity
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 key: %w", err)
	}

	pubPEM, err := marshalPublicKeyPEM(pub)
	if err != nil {
		return nil, err
	}
	privPEM, err := marshalPrivateKeyPEM(priv)
	if err != nil {
		return nil, err
	}

	deviceID, err := deriveDeviceID(pubPEM)
	if err != nil {
		return nil, err
	}

	identity := &DeviceIdentity{
		Version:       1,
		DeviceID:      deviceID,
		PublicKeyPEM:  pubPEM,
		PrivateKeyPEM: privPEM,
		CreatedAtMs:   time.Now().UnixMilli(),
	}

	if err := saveIdentity(identity); err != nil {
		return nil, err
	}

	return identity, nil
}

// LoadDeviceTokens loads stored device tokens.
func LoadDeviceTokens() (*DeviceTokenStore, error) {
	path := filepath.Join(DeviceDir(), "device-auth.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var store DeviceTokenStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, err
	}
	return &store, nil
}

// SaveDeviceToken stores a device token for a role.
func SaveDeviceToken(deviceID, role, token string, scopes []string) error {
	store := &DeviceTokenStore{
		Version:  1,
		DeviceID: deviceID,
		Tokens:   make(map[string]DeviceTokenRef),
	}

	// Load existing
	existing, err := LoadDeviceTokens()
	if err == nil && existing.DeviceID == deviceID {
		store = existing
	}

	store.Tokens[role] = DeviceTokenRef{
		Token:       token,
		Role:        role,
		Scopes:      scopes,
		UpdatedAtMs: time.Now().UnixMilli(),
	}

	dir := DeviceDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "device-auth.json"), data, 0600)
}

// BuildDeviceConnect creates the device field for the connect request.
// platform and deviceFamily must match the values sent in ClientInfo.
// authToken is the gateway auth token (from connectParams.auth.token), used in the signature.
// scopes must be in the exact order sent in ConnectParams.Scopes.
func BuildDeviceConnect(identity *DeviceIdentity, nonce string, scopes []string, platform, deviceFamily, authToken string) (*DeviceConnect, error) {
	signedAt := time.Now().UnixMilli()

	rawPub, err := RawPublicKeyBase64URL(identity.PublicKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("encode public key: %w", err)
	}

	// The "token" in the payload is the gateway auth token (resolveSignatureToken
	// returns auth.token ?? auth.deviceToken ?? auth.bootstrapToken).
	signatureToken := authToken

	// Build v3 payload — must match server-side reconstruction exactly.
	// Server uses normalizeDeviceMetadataForAuth() which lowercases and trims.

	normPlatform := strings.ToLower(strings.TrimSpace(platform))
	normDeviceFamily := strings.ToLower(strings.TrimSpace(deviceFamily))

	payload := strings.Join([]string{
		"v3",
		identity.DeviceID,
		clientID,
		clientMode,
		"operator",
		strings.Join(scopes, ","),
		fmt.Sprintf("%d", signedAt),
		signatureToken,
		nonce,
		normPlatform,
		normDeviceFamily,
	}, "|")

	signature, err := signPayload(identity.PrivateKeyPEM, payload)
	if err != nil {
		return nil, fmt.Errorf("sign payload: %w", err)
	}

	return &DeviceConnect{
		ID:        identity.DeviceID,
		PublicKey: rawPub,
		Signature: signature,
		SignedAt:  signedAt,
		Nonce:     nonce,
	}, nil
}

// --- internal helpers ---

func deriveDeviceID(publicKeyPEM string) (string, error) {
	raw, err := rawPublicKeyBytes(publicKeyPEM)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}

func rawPublicKeyBytes(publicKeyPEM string) ([]byte, error) {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	edPub, ok := pub.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not an ed25519 public key")
	}
	return []byte(edPub), nil
}

// RawPublicKeyBase64URL returns the raw 32-byte Ed25519 public key as base64url.
func RawPublicKeyBase64URL(publicKeyPEM string) (string, error) {
	raw, err := rawPublicKeyBytes(publicKeyPEM)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func signPayload(privateKeyPEM, payload string) (string, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return "", fmt.Errorf("failed to decode private key PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse private key: %w", err)
	}
	edKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		return "", fmt.Errorf("not an ed25519 private key")
	}
	sig := ed25519.Sign(edKey, []byte(payload))
	return base64.RawURLEncoding.EncodeToString(sig), nil
}

func marshalPublicKeyPEM(pub ed25519.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("marshal public key: %w", err)
	}
	block := &pem.Block{Type: "PUBLIC KEY", Bytes: der}
	return string(pem.EncodeToMemory(block)), nil
}

func marshalPrivateKeyPEM(priv ed25519.PrivateKey) (string, error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", fmt.Errorf("marshal private key: %w", err)
	}
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	return string(pem.EncodeToMemory(block)), nil
}

func saveIdentity(identity *DeviceIdentity) error {
	dir := DeviceDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create device dir: %w", err)
	}
	data, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "device.json"), data, 0600)
}
