#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
SKILL="$ROOT/skills/orchestrate/SKILL.md"
PLAN_PROMPT="$ROOT/skills/orchestrate/references/prompts/plan.md"

assert_contains() {
  local file=$1
  local text=$2
  if ! grep -Fq -- "$text" "$file"; then
    printf 'expected %s to contain: %s\n' "$file" "$text" >&2
    exit 1
  fi
}

assert_absent() {
  local file=$1
  local text=$2
  if grep -Fq -- "$text" "$file"; then
    printf 'expected %s to omit: %s\n' "$file" "$text" >&2
    exit 1
  fi
}

assert_contains "$SKILL" 'design/spec document ──────→ focused-first gate'
assert_contains "$SKILL" 'A design or specification defaults to one focused implementation worker.'
assert_contains "$SKILL" 'Planning requires a recorded trigger'
assert_contains "$SKILL" 'one review and at most one amendment'
assert_contains "$SKILL" 'do not launch or rotate to another planner'
assert_absent "$SKILL" '**planning stage** below before any implementation'
assert_absent "$SKILL" 'the plan stops being a suggestion and becomes **the spec'

assert_contains "$PLAN_PROMPT" 'This is a coordination plan, not a shadow implementation.'
assert_contains "$PLAN_PROMPT" 'Do not embed production code'
assert_contains "$PLAN_PROMPT" 'Do not copy design passages verbatim'
assert_absent "$PLAN_PROMPT" 'the actual code or edit'
assert_absent "$PLAN_PROMPT" 'EMBEDDED verbatim'

printf '%s\n' 'orchestrate focused-first contract: ok'
