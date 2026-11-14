package assistant

import (
	"errors"
	"os"
	"testing"
)

// failRecordsWrite makes writes of the records file fail until the returned
// func is called, like a full disk or a read-only state dir would.
func failRecordsWrite(t *testing.T, s *Store) (restore func()) {
	t.Helper()
	orig := writeFileAtomic
	writeFileAtomic = func(path string, data []byte, perm os.FileMode) error {
		if path == s.Path() {
			return errors.New("injected: no space left on device")
		}
		return orig(path, data, perm)
	}
	restore = func() { writeFileAtomic = orig }
	t.Cleanup(restore)
	return restore
}

// A revoke whose save failed must not count as done: the retry persists
// it, so other processes stop accepting the token.
func TestStore_FailedRevokeIsRetried(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	tok, _, err := s.Create("victim", "mcp", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	restore := failRecordsWrite(t, s)
	if _, err := s.Revoke("victim"); err == nil {
		t.Fatal("revoke with a failing save reported success")
	}
	restore()
	existed, err := s.Revoke("victim")
	if err != nil || !existed {
		t.Fatalf("retried revoke = %v, %v; want true, nil (the first attempt never reached disk)", existed, err)
	}
	if _, err := NewStore(dir).Authenticate(tok); !errors.Is(err, ErrUnknownToken) {
		t.Fatalf("another process still accepts the revoked token: %v", err)
	}
}

// A create whose save failed must not be written later by an unrelated
// save from the same store.
func TestStore_FailedCreateIsNotPersistedLater(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	// The records file exists, so the next reload trusts the cache.
	if _, _, err := s.Create("seed", "mcp", nil, ""); err != nil {
		t.Fatal(err)
	}
	restore := failRecordsWrite(t, s)
	if _, _, err := s.Create("ghost", "mcp", AllScopes, ""); err == nil {
		t.Fatal("create with a failing save reported success")
	}
	restore()
	if _, _, err := s.Create("other", "mcp", nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := NewStore(dir).Get("ghost"); err != nil || ok {
		t.Fatalf("the failed create was persisted by a later save (ok=%v err=%v)", ok, err)
	}
}
