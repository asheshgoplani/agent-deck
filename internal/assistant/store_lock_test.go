package assistant

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

// holdStoreLockLikeAnotherProcess takes an exclusive flock on the store's
// sibling lock file through its own open file description, which is what a
// second agent-deck process (the CLI next to a running web server) does.
func holdStoreLockLikeAnotherProcess(t *testing.T, s *Store) (release func()) {
	t.Helper()
	if err := os.MkdirAll(s.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(s.Path()+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
}

// Create and Revoke wait for another process that holds the store lock, so
// their reload-change-save cycle never interleaves with that process's.
func TestStore_WritesWaitForCrossProcessLock(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if _, _, err := s.Create("victim", "mcp", nil, ""); err != nil {
		t.Fatal(err)
	}

	for _, op := range []struct {
		name string
		run  func() error
	}{
		{"create", func() error { _, _, err := s.Create("other", "mcp", nil, ""); return err }},
		{"revoke", func() error { _, err := s.Revoke("victim"); return err }},
	} {
		release := holdStoreLockLikeAnotherProcess(t, s)
		done := make(chan error, 1)
		go func() { done <- op.run() }()
		select {
		case err := <-done:
			release()
			t.Fatalf("%s finished (err=%v) while another process held the store lock", op.name, err)
		case <-time.After(300 * time.Millisecond):
		}
		release()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s after the lock was released: %v", op.name, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s still blocked after the lock was released", op.name)
		}
	}
}

// Two stores on one directory stand in for two processes (a running web
// server answering /connect and `agent-deck connect revoke`). A revoke must
// never be undone by a create that loaded the file before the revoke saved.
func TestStore_RevokeIsNotUndoneByConcurrentCreate(t *testing.T) {
	dir := t.TempDir()
	web, cli := NewStore(dir), NewStore(dir)
	for i := 0; i < 40; i++ {
		if _, _, err := cli.Create("victim", "mcp", nil, ""); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := cli.Revoke("victim"); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, _, err := web.Create(fmt.Sprintf("bot-%d", i), "mcp", nil, ""); err != nil {
				t.Error(err)
			}
		}()
		wg.Wait()
		if _, ok, err := NewStore(dir).Get("victim"); err != nil || ok {
			t.Fatalf("round %d: revoked token is back in the store (ok=%v err=%v)", i, ok, err)
		}
		if _, ok, err := NewStore(dir).Get(fmt.Sprintf("bot-%d", i)); err != nil || !ok {
			t.Fatalf("round %d: concurrent create was lost (ok=%v err=%v)", i, ok, err)
		}
	}
}
