package calendar

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestLoadToken_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token.json")

	tok := &oauth2.Token{
		AccessToken:  "access-123",
		RefreshToken: "refresh-456",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour),
	}
	data, _ := json.Marshal(tok)
	require.NoError(t, os.WriteFile(path, data, 0600))

	loaded, err := loadToken(path)
	require.NoError(t, err)
	assert.Equal(t, "access-123", loaded.AccessToken)
	assert.Equal(t, "refresh-456", loaded.RefreshToken)
}

func TestLoadToken_Missing(t *testing.T) {
	_, err := loadToken("/nonexistent/token.json")
	assert.Error(t, err)
}

func TestSaveToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token.json")

	tok := &oauth2.Token{
		AccessToken:  "access-789",
		RefreshToken: "refresh-abc",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour),
	}

	require.NoError(t, saveToken(path, tok))

	// Verify file permissions
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	// Verify round-trip
	loaded, err := loadToken(path)
	require.NoError(t, err)
	assert.Equal(t, "access-789", loaded.AccessToken)
}

func TestPersistingTokenSource_SavesOnTokenChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token.json")

	original := &oauth2.Token{AccessToken: "old-access", RefreshToken: "refresh"}
	refreshed := &oauth2.Token{AccessToken: "new-access", RefreshToken: "refresh"}

	calls := 0
	inner := &staticTokenSource{tokens: []*oauth2.Token{original, refreshed}, t: t}
	pts := &persistingTokenSource{inner: inner, tokenPath: path, last: original.AccessToken}

	// First call: token unchanged — saveToken must NOT be called.
	tok, err := pts.Token()
	require.NoError(t, err)
	assert.Equal(t, "old-access", tok.AccessToken)
	assert.NoFileExists(t, path, "saveToken should not be called when token unchanged")

	_ = calls

	// Second call: token changed — saveToken must persist the new token.
	tok, err = pts.Token()
	require.NoError(t, err)
	assert.Equal(t, "new-access", tok.AccessToken)

	loaded, err := loadToken(path)
	require.NoError(t, err, "token should have been saved to disk")
	assert.Equal(t, "new-access", loaded.AccessToken)
}

// staticTokenSource returns tokens from a fixed list in order.
type staticTokenSource struct {
	tokens []*oauth2.Token
	idx    int
	t      *testing.T
}

func (s *staticTokenSource) Token() (*oauth2.Token, error) {
	if s.idx >= len(s.tokens) {
		s.t.Fatal("Token() called more times than expected")
	}
	tok := s.tokens[s.idx]
	s.idx++
	return tok, nil
}

func TestParseCredentials(t *testing.T) {
	// Minimal credentials.json structure from Google Cloud Console
	creds := `{
		"installed": {
			"client_id": "123.apps.googleusercontent.com",
			"client_secret": "secret",
			"auth_uri": "https://accounts.google.com/o/oauth2/auth",
			"token_uri": "https://oauth2.googleapis.com/token",
			"redirect_uris": ["http://localhost"]
		}
	}`
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	require.NoError(t, os.WriteFile(path, []byte(creds), 0600))

	cfg, err := parseCredentials(path)
	require.NoError(t, err)
	assert.Equal(t, "123.apps.googleusercontent.com", cfg.ClientID)
}
