#!/usr/bin/env bash
# Run only in CI or an isolated container, never against a developer's live deck.
set -euo pipefail
if [[ "${CI:-}" != "true" && ! -f /.dockerenv ]]; then
  echo 'Shared-state proof tests require CI or an isolated container.' >&2
  exit 2
fi
# Resolve caches before isolating HOME so setup-go's populated cache is reused.
export GOCACHE="$(go env GOCACHE)" GOMODCACHE="$(go env GOMODCACHE)"
sandbox=$(mktemp -d /tmp/adwp.XXXXXX)
export HOME="$sandbox/home" TMPDIR="$sandbox/tmp"
mkdir -p "$HOME" "$TMPDIR"
export XDG_CONFIG_HOME="$HOME/config" XDG_DATA_HOME="$HOME/data" XDG_CACHE_HOME="$HOME/cache"
unset CLAUDE_CONFIG_DIR AGENTDECK_ACCOUNT AGENTDECK_INSTANCE_ID CODEX_HOME CODEX_THREAD_ID TMUX TMUX_TMPDIR
export GOMAXPROCS=2
receipt="$sandbox/proof.jsonl"
go test -json -race -tags watcher_proof -count=1 -p 1 -timeout 3m ./internal/ui -run '^TestStorageWatcherProof' | tee "$receipt"
python3 - "$receipt" <<'PY'
import json
import sys

expected = {
    "TestStorageWatcherProofSynchronousIssuanceDoesNotProbe",
    "TestStorageWatcherProofDelayedUnticketedLoad",
    "TestStorageWatcherProofDelayedUnticketedLoad/initial",
    "TestStorageWatcherProofDelayedUnticketedLoad/manual",
}
ran, passed = set(), set()
with open(sys.argv[1], encoding="utf-8") as stream:
    for line in stream:
        event = json.loads(line)
        action, test = event.get("Action"), event.get("Test")
        if action in {"skip", "fail", "build-fail"}:
            raise SystemExit(f"Required proof did not pass: {event}")
        if test and action == "run":
            ran.add(test)
        if test and action == "pass":
            passed.add(test)
if ran != expected or passed != expected:
    raise SystemExit(f"Required proof coverage mismatch: ran={ran}, passed={passed}")
print("Shared-state proofs: all required controls ran and passed; zero skips.")
PY
