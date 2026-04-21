package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// InstallTmuxShim drops a `tmux` wrapper into ShimDir that transparently
// forces every invocation through the sandbox's per-test socket via `-S`.
// Without this the binary's internal `exec.Command("tmux", ...)` calls hit
// the user's default tmux server, poisoning (and being poisoned by) real
// sessions.
//
// The shim discovers the real tmux lazily from the first PATH entry that is
// NOT ShimDir — so the child of the shim finds /usr/bin/tmux even though
// the shim itself is named `tmux`.
//
// Call this from tests that actually start tmux sessions.
func (s *Sandbox) InstallTmuxShim(t *testing.T) {
	t.Helper()
	sockPath := s.TmuxSocket()
	shimPath := filepath.Join(s.ShimDir, "tmux")

	script := fmt.Sprintf(`#!/usr/bin/env bash
# Harness tmux shim — forces all invocations to the per-test socket.
set -u

SOCK=%q
SHIM_DIR=%q

# Find the real tmux: scan PATH, skip any entry equal to our shim dir.
real=""
IFS=":" read -ra parts <<< "$PATH"
for p in "${parts[@]}"; do
  [ "$p" = "$SHIM_DIR" ] && continue
  if [ -x "$p/tmux" ]; then
    real="$p/tmux"
    break
  fi
done
if [ -z "$real" ]; then
  echo "eval-tmux-shim: no real tmux found on PATH (excluding shim dir)" >&2
  exit 127
fi

# Splice '-S <sock>' right after the command name. Caller's own -S (if any)
# is preserved after ours and wins by virtue of appearing later — but in
# practice the binary never passes -S, which is why we need this shim.
exec "$real" -S "$SOCK" "$@"
`, sockPath, s.ShimDir)

	if err := os.WriteFile(shimPath, []byte(script), 0o755); err != nil {
		t.Fatalf("tmux shim write: %v", err)
	}
}
