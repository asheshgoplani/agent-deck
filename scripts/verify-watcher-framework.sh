#!/usr/bin/env bash
# verify-watcher-framework.sh — End-to-end integration harness for the watcher framework.
# Validates engine, adapters, router, health bridge, folder hierarchy, and CLI surface.
#
# Usage: bash scripts/verify-watcher-framework.sh
# Exit codes: 0 = all checks pass, 1 = one or more checks failed.
#
# Runs in <60s on macOS + Linux. Requires: go 1.24+, bash 4+.

set -euo pipefail

PASS_COUNT=0
FAIL_COUNT=0
GOTOOLCHAIN="${GOTOOLCHAIN:-go1.25.12}"
export GOTOOLCHAIN

pass() {
  echo "[PASS] $1"
  PASS_COUNT=$((PASS_COUNT + 1))
}

fail() {
  echo "[FAIL] $1"
  FAIL_COUNT=$((FAIL_COUNT + 1))
}

step() {
  echo ""
  echo "--- $1 ---"
}

# --------------------------------------------------------------------------
# Step 1: Build check
# --------------------------------------------------------------------------
step "Build: compile all packages"
if go build ./... 2>&1; then
  pass "go build ./..."
else
  fail "go build ./... (compilation errors)"
fi

# --------------------------------------------------------------------------
# Step 2: Watcher package unit + integration tests (includes layout, state,
#         event_log, engine, adapters, health bridge, router)
# --------------------------------------------------------------------------
step "Tests: internal/watcher (unit + integration, -race)"
if go test ./internal/watcher/... -race -count=1 -timeout 120s 2>&1; then
  pass "go test ./internal/watcher/... -race"
else
  fail "go test ./internal/watcher/... -race"
fi

# --------------------------------------------------------------------------
# Step 3: CLI watcher tests (includes drift-check, health fields, import)
# --------------------------------------------------------------------------
step "Tests: cmd/agent-deck watcher tests (-race)"
if go test ./cmd/agent-deck/... -run "Watcher" -race -count=1 -timeout 60s 2>&1; then
  pass "go test ./cmd/agent-deck/... -run Watcher -race"
else
  fail "go test ./cmd/agent-deck/... -run Watcher -race"
fi

# --------------------------------------------------------------------------
# Step 4: Session watcher meta tests (singular path regression)
# --------------------------------------------------------------------------
step "Tests: internal/session watcher meta tests"
if go test ./internal/session/... -run "TestWatcher" -race -count=1 -timeout 30s 2>&1; then
  pass "go test ./internal/session/... -run TestWatcher -race"
else
  fail "go test ./internal/session/... -run TestWatcher -race"
fi

# --------------------------------------------------------------------------
# Step 5: Source artifact checks
# --------------------------------------------------------------------------
step "Artifacts: verify key source files exist"

REQUIRED_FILES=(
  "internal/watcher/layout.go"
  "internal/watcher/state.go"
  "internal/watcher/event_log.go"
  "internal/watcher/layout_test.go"
  "internal/watcher/health_bridge.go"
  "internal/watcher/health_bridge_test.go"
  "internal/watcher/engine.go"
  "internal/watcher/webhook.go"
  "internal/watcher/router.go"
  "cmd/agent-deck/watcher_cmd.go"
  "cmd/agent-deck/watcher_cmd_test.go"
  "cmd/agent-deck/assets/skills/watcher-creator/SKILL.md"
  "assets/watcher-templates/CLAUDE.md"
  "assets/watcher-templates/POLICY.md"
  "assets/watcher-templates/LEARNINGS.md"
)
ALL_EXIST=true
for f in "${REQUIRED_FILES[@]}"; do
  if [ ! -f "$f" ]; then
    fail "missing required file: $f"
    ALL_EXIST=false
  fi
done
if $ALL_EXIST; then
  pass "all ${#REQUIRED_FILES[@]} required source files present"
fi

# --------------------------------------------------------------------------
# Step 6: Drift check — no stale watchers/ in embedded skill
# --------------------------------------------------------------------------
step "Drift: embedded SKILL.md has no stale watchers/ paths"

SKILL_PATH="cmd/agent-deck/assets/skills/watcher-creator/SKILL.md"
STALE=$(grep -n "watchers/" "$SKILL_PATH" | grep -v -E "legacy|migration|renamed|symlink" || true)
if [ -z "$STALE" ]; then
  pass "SKILL.md contains no stale watchers/ references"
else
  fail "SKILL.md contains stale watchers/ references: $STALE"
fi

# --------------------------------------------------------------------------
# Step 7: Security checks — T-21-SL and T-21-PI mitigations present
# --------------------------------------------------------------------------
step "Security: T-21-SL (symlink traversal) and T-21-PI (path injection)"

if grep -q "os.Lstat" internal/watcher/layout.go; then
  pass "T-21-SL: os.Lstat present in layout.go (symlink traversal guard)"
else
  fail "T-21-SL: os.Lstat missing from layout.go"
fi

if grep -q "watcherNameRegex" internal/watcher/layout.go; then
  pass "T-21-PI: watcherNameRegex present in layout.go (path injection guard)"
else
  fail "T-21-PI: watcherNameRegex missing from layout.go"
fi

# --------------------------------------------------------------------------
# Step 8: CHANGELOG migration callout
# --------------------------------------------------------------------------
step "Docs: CHANGELOG.md contains v1.6.0 migration callout"

if grep -q "watcher.*data directory renamed\|watchers.*watcher.*singular\|compatibility symlink" CHANGELOG.md; then
  pass "CHANGELOG.md has v1.6.0 migration notice"
else
  fail "CHANGELOG.md missing v1.6.0 migration notice"
fi

# --------------------------------------------------------------------------
# Summary
# --------------------------------------------------------------------------
echo ""
echo "============================================"
echo "  Watcher Framework Verification Summary"
echo "============================================"
echo "  Passed: $PASS_COUNT"
echo "  Failed: $FAIL_COUNT"
echo "============================================"

if [ "$FAIL_COUNT" -gt 0 ]; then
  echo ""
  echo "RESULT: FAILED ($FAIL_COUNT check(s) did not pass)"
  exit 1
fi

echo ""
echo "RESULT: ALL CHECKS PASSED"
exit 0
