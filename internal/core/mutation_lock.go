package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// RunWithMutationLock serializes registry mutations across daemon and direct
// CLI processes for one profile. The lock covers the command's load,
// decision, external effects, and save.
func RunWithMutationLock(ctx context.Context, reg *Registry, id, profile string, in any) *Result {
	if ctx == nil {
		ctx = context.Background()
	}
	def, ok := reg.Lookup(id)
	if !ok || def.Class != Mutate {
		return reg.Run(ctx, id, in)
	}
	resolved, err := session.ResolveProfileForStorage(profile)
	if err != nil {
		return &Result{ID: id, Err: Errorf(CodeStorage, "resolve mutation profile: %v", err)}
	}
	dir, err := agentpaths.ProfileRuntimeDir(resolved)
	if err != nil {
		return &Result{ID: id, Err: Errorf(CodeStorage, "mutation runtime dir: %v", err)}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return &Result{ID: id, Err: Errorf(CodeStorage, "create mutation runtime dir: %v", err)}
	}
	f, err := os.OpenFile(filepath.Join(dir, "mutation.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return &Result{ID: id, Err: Errorf(CodeStorage, "open mutation lock: %v", err)}
	}
	defer f.Close()
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return &Result{ID: id, Err: Errorf(CodeStorage, "lock mutation: %v", err)}
		}
		select {
		case <-ctx.Done():
			return &Result{ID: id, Err: Errorf(CodeInternal, "mutation cancelled: %v", ctx.Err())}
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer func() {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
			// Closing the descriptor releases the lock even if explicit unlock
			// failed; retain a visible diagnostic for the rare failure.
			fmt.Fprintln(os.Stderr, "agent-deck: unlock mutation:", err)
		}
	}()
	return reg.Run(ctx, id, in)
}
