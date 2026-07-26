#!/usr/bin/env bash
# Regression gate for #873: README and release notes advertise
# `brew install asheshgoplani/tap/agent-deck`. Goreleaser publishes the
# formula to https://github.com/asheshgoplani/homebrew-tap on each release.
#
# This script verifies the public path users actually take:
#   1. The tap repo is reachable.
#   2. The formula file exists in the expected directory.
#   3. The formula's `version` matches the latest GitHub release tag.
#   4. Every download URL in the formula resolves (no 404 dangling refs).
#
# Run on every release tag; also run on PRs that touch install docs or
# the goreleaser brews block.

set -euo pipefail

OWNER="asheshgoplani"
TAP_REPO="homebrew-tap"
FORMULA_NAME="agent-deck"
FORMULA_PATH="Formula/${FORMULA_NAME}.rb"
RAW_BASE="https://raw.githubusercontent.com/${OWNER}/${TAP_REPO}/main"
RELEASES_API="https://api.github.com/repos/${OWNER}/agent-deck/releases/latest"

GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

fail() { echo -e "${RED}FAIL${NC}: $*" >&2; exit 1; }
pass() { echo -e "${GREEN}OK${NC}  : $*"; }

# 1. Tap reachable
if ! curl -sf -o /dev/null "https://github.com/${OWNER}/${TAP_REPO}"; then
  fail "tap repo https://github.com/${OWNER}/${TAP_REPO} is not reachable"
fi
pass "tap repo reachable"

# 2 + 3. Formula exists and its version matches the latest release.
#
# Retried, because raw.githubusercontent.com serves the formula through a CDN
# with a cache lifetime of a few minutes: right after a release the tap commit
# can be pushed while raw still returns the previous formula. A single attempt
# turned that CDN lag into a red gate (#1759). A real break (tap push failed,
# formula never updated) still fails — it just takes ATTEMPTS tries first.
ATTEMPTS="${HOMEBREW_VERIFY_ATTEMPTS:-6}"
RETRY_DELAY="${HOMEBREW_VERIFY_RETRY_DELAY:-30}"

formula_url="${RAW_BASE}/${FORMULA_PATH}"
formula=""
formula_version=""
latest_tag=""

for attempt in $(seq 1 "${ATTEMPTS}"); do
  # Cache-bust so a retry can actually observe a newer formula.
  formula=$(curl -sf "${formula_url}?nocache=$(date +%s)") ||
    fail "formula not found at ${formula_url}"
  formula_version=$(printf '%s\n' "${formula}" | sed -nE 's/^[[:space:]]*version "([^"]+)".*/\1/p' | head -1)
  [ -n "${formula_version}" ] || fail "could not parse formula version"

  latest_tag=$(curl -sf "${RELEASES_API}" | sed -nE 's/.*"tag_name":[[:space:]]*"v?([^"]+)".*/\1/p' | head -1)
  [ -n "${latest_tag}" ] || fail "could not fetch latest release tag"

  if [ "${formula_version}" = "${latest_tag}" ]; then
    break
  fi

  if [ "${attempt}" -eq "${ATTEMPTS}" ]; then
    fail "formula version ${formula_version} != latest release ${latest_tag} after ${ATTEMPTS} attempts (goreleaser publish step likely failed)"
  fi
  echo "formula version ${formula_version} != latest release ${latest_tag}; retrying in ${RETRY_DELAY}s (attempt ${attempt}/${ATTEMPTS})"
  sleep "${RETRY_DELAY}"
done
pass "formula exists at ${FORMULA_PATH}"
pass "formula version ${formula_version} matches latest release"

# 4. All URLs in formula resolve
urls=$(printf '%s\n' "${formula}" | sed -nE 's/^[[:space:]]*url "([^"]+)".*/\1/p')
[ -n "${urls}" ] || fail "no asset URLs found in formula"

while IFS= read -r url; do
  code=$(curl -sIL -o /dev/null -w "%{http_code}" "${url}")
  if [ "${code}" != "200" ]; then
    fail "asset ${url} returned HTTP ${code}"
  fi
  pass "asset reachable: ${url##*/}"
done <<< "${urls}"

# 5. README still advertises the same install command (catches doc drift)
script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readme="${script_dir}/../README.md"
if ! grep -qF "brew install ${OWNER}/tap/${FORMULA_NAME}" "${readme}"; then
  fail "README.md no longer references 'brew install ${OWNER}/tap/${FORMULA_NAME}'; tap is configured but advertised command drifted"
fi
pass "README install command matches tap"

echo
echo "All homebrew install path checks passed."
