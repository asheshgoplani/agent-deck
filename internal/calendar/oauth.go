package calendar

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const calendarReadonlyScope = "https://www.googleapis.com/auth/calendar.readonly"

// parseCredentials reads a Google Cloud credentials.json and returns an oauth2 config.
func parseCredentials(path string) (*oauth2.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	cfg, err := google.ConfigFromJSON(data, calendarReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	return cfg, nil
}

// loadToken reads a cached oauth2.Token from disk.
func loadToken(path string) (*oauth2.Token, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, fmt.Errorf("decode token: %w", err)
	}
	return &tok, nil
}

// saveToken writes an oauth2.Token to disk with restrictive permissions.
func saveToken(path string, tok *oauth2.Token) error {
	data, err := json.Marshal(tok)
	if err != nil {
		return fmt.Errorf("encode token: %w", err)
	}
	return os.WriteFile(path, data, 0600)
}

// persistingTokenSource wraps an oauth2.TokenSource and writes the token to
// disk whenever the access token changes, ensuring long-running processes
// survive token rotation across restarts.
type persistingTokenSource struct {
	inner     oauth2.TokenSource
	tokenPath string
	last      string // last seen access token
}

func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := p.inner.Token()
	if err != nil {
		return nil, err
	}
	if tok.AccessToken != p.last {
		p.last = tok.AccessToken
		if saveErr := saveToken(p.tokenPath, tok); saveErr != nil {
			slog.Warn("calendar: failed to persist refreshed token",
				slog.String("path", p.tokenPath), slog.String("error", saveErr.Error()))
		}
	}
	return tok, nil
}

// ParseCredentials is the exported version of parseCredentials.
func ParseCredentials(path string) (*oauth2.Config, error) {
	return parseCredentials(path)
}

// SaveToken is the exported version of saveToken.
func SaveToken(path string, tok *oauth2.Token) error {
	return saveToken(path, tok)
}
