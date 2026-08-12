#!/usr/bin/env bash
# Integration tests for the pre-commit marketplace skill-version bump hook.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
HOOK="$ROOT/scripts/bump-plugin-skill-version.py"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

failures=0

fail() { printf 'FAIL — %s\n' "$1"; failures=$((failures + 1)); }
pass() { printf 'ok   — %s\n' "$1"; }

if grep -Fq 'plugin-skill-version:' "$ROOT/lefthook.yml" && \
  grep -Fq 'scripts/bump-plugin-skill-version.py' "$ROOT/lefthook.yml"; then
  pass "Lefthook invokes the plugin skill version hook"
else
  fail "Lefthook does not invoke the plugin skill version hook"
fi

assert_version() {
  local want="$1"
  local got
  got="$(git show :.claude-plugin/marketplace.json | python3 -c 'import json,sys; print(json.load(sys.stdin)["metadata"]["version"])')"
  if [ "$got" = "$want" ]; then
    pass "staged marketplace version is $want"
  else
    fail "staged marketplace version is $got; want $want"
  fi
}

init_repo() {
  local name="$1"
  local repo="$tmp/$name"
  mkdir -p "$repo/.claude-plugin" "$repo/skills/published/references" "$repo/skills/private"
  cat >"$repo/.claude-plugin/marketplace.json" <<'JSON'
{
  "name": "agent-deck",
  "metadata": { "version": "1.2.0" },
  "plugins": [{ "name": "agent-deck", "skills": ["./skills/published"] }]
}
JSON
  printf '%s\n' 'published skill' >"$repo/skills/published/SKILL.md"
  printf '%s\n' 'private skill' >"$repo/skills/private/SKILL.md"
  git -C "$repo" init -q
  git -C "$repo" config user.email test@example.com
  git -C "$repo" config user.name 'Hook Test'
  git -C "$repo" add .
  git -C "$repo" commit -qm baseline
  printf '%s\n' "$repo"
}

# Any nested file in a marketplace-published skill is part of the payload.
repo="$(init_repo nested-skill-change)"
printf '%s\n' 'reference update' >"$repo/skills/published/references/guide.md"
git -C "$repo" add skills/published/references/guide.md
(cd "$repo" && "$HOOK")
(cd "$repo" && assert_version 1.2.1)

# Several published-skill changes still make one commit and one patch bump.
repo="$(init_repo multiple-skill-changes)"
printf '%s\n' 'first update' >>"$repo/skills/published/SKILL.md"
printf '%s\n' 'second update' >"$repo/skills/published/references/guide.md"
git -C "$repo" add skills/published
(cd "$repo" && "$HOOK")
(cd "$repo" && assert_version 1.2.1)

# An explicit minor release is intentional and must not be silently advanced.
repo="$(init_repo manual-minor-version)"
python3 - "$repo/.claude-plugin/marketplace.json" <<'PY'
import json
import sys

path = sys.argv[1]
data = json.load(open(path))
data["metadata"]["version"] = "1.3.0"
with open(path, "w") as f:
    json.dump(data, f, indent=2)
    f.write("\n")
PY
printf '%s\n' 'skill update' >>"$repo/skills/published/SKILL.md"
git -C "$repo" add .claude-plugin/marketplace.json skills/published/SKILL.md
(cd "$repo" && "$HOOK")
(cd "$repo" && assert_version 1.3.0)

# Skill directories not in the marketplace do not change the plugin payload.
repo="$(init_repo unpublished-skill-change)"
printf '%s\n' 'private update' >>"$repo/skills/private/SKILL.md"
git -C "$repo" add skills/private/SKILL.md
(cd "$repo" && "$HOOK")
(cd "$repo" && assert_version 1.2.0)

# A malformed manifest must stop the commit instead of guessing a version.
repo="$(init_repo invalid-manifest)"
printf '%s\n' '{not json' >"$repo/.claude-plugin/marketplace.json"
printf '%s\n' 'skill update' >>"$repo/skills/published/SKILL.md"
git -C "$repo" add .claude-plugin/marketplace.json skills/published/SKILL.md
if out="$(cd "$repo" && "$HOOK" 2>&1)"; then
  fail "invalid marketplace manifest is rejected"
elif printf '%s' "$out" | grep -Fq 'is not valid JSON'; then
  pass "invalid marketplace manifest is rejected"
else
  fail "invalid marketplace manifest reports a useful error"
fi

if [ "$failures" -ne 0 ]; then
  printf '%d FAILED\n' "$failures"
  exit 1
fi

printf 'ALL PASS\n'
