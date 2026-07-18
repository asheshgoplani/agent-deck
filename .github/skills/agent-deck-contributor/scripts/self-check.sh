#!/usr/bin/env bash
# self-check.sh — agent-deck contributor pre-open gate.
#
# Runs, on your own branch and PR-body draft, the same checks the repo-side
# intake gate (.github/workflows/pr-intake.yml + .github/INTAKE.md) and the
# maintainer's review machine will run. Fix every FAIL before opening the PR;
# resolve or explain every WARN in the PR body.
#
# Usage:   scripts/self-check.sh [pr-body.md]
#          (run from anywhere inside the agent-deck repo)
# Env:     BASE_REF=origin/main   comparison base
#          FULL_TESTS=1           run the whole suite, not just touched packages
#          LINKED_ISSUE=<n|url>   set when a >3000-line diff has an agreed issue/Discussion
#          SKIP_REVERT_CHECK=1    skip the revert-check (e.g. pure test-infra PRs)
#
# Needs: git, grep, awk, perl. Go checks are skipped automatically on non-Go diffs.
# Exit 0 = no FAIL.

set -u

BODY_FILE="${1:-pr-body.md}"
BASE_REF="${BASE_REF:-origin/main}"

pass=0; fail=0; warn=0; skip=0
PASS() { printf 'PASS  %-24s %s\n' "$1" "${2:-}"; pass=$((pass+1)); }
FAIL() { printf 'FAIL  %-24s %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
WARN() { printf 'WARN  %-24s %s\n' "$1" "${2:-}"; warn=$((warn+1)); }
SKIP() { printf 'skip  %-24s %s\n' "$1" "${2:-}"; skip=$((skip+1)); }

ROOT=$(git rev-parse --show-toplevel 2>/dev/null) || { echo "not inside a git repo"; exit 2; }
cd "$ROOT" || exit 2
MB=$(git merge-base HEAD "$BASE_REF" 2>/dev/null) || { echo "no merge-base with $BASE_REF — run: git fetch origin main"; exit 2; }

CHANGED_ALL=$(git diff --name-only "$MB" HEAD)
if [ -z "$CHANGED_ALL" ]; then echo "no changes vs $BASE_REF — nothing to check"; exit 2; fi
GO_CHANGED=$(git diff --name-only --diff-filter=d "$MB" HEAD -- '*.go')
GO_TESTS_CHANGED=$(git diff --name-only --diff-filter=d "$MB" HEAD -- '*_test.go')
GO_PROD_CHANGED=$(printf '%s\n' "$GO_CHANGED" | grep -v '_test\.go$' || true)

ADDED_LINES=$(git diff "$MB" HEAD | grep '^+' | grep -v '^+++' | sed 's/^+//')
# Pattern scans read added CODE lines only: documentation (*.md) legitimately
# quotes the very patterns this sweep hunts, and this script necessarily
# contains its own regexes — both are excluded. The invisible-chars check
# still covers every file, and .github/ changes are human-gated regardless.
SELF_REL=".github/skills/agent-deck-contributor/scripts/self-check.sh"
ADDED_CODE=$(git diff "$MB" HEAD -- . ':!*.md' ":!$SELF_REL" | grep '^+' | grep -v '^+++' | sed 's/^+//')

HAVE_GO=1
command -v go >/dev/null 2>&1 || HAVE_GO=0

# Sandbox: throwaway HOME + cleared XDG, but keep Go caches so runs stay fast.
SANDBOX_HOME=$(mktemp -d)
GOMODCACHE_SAVED=""; GOCACHE_SAVED=""
if [ "$HAVE_GO" = 1 ]; then
  GOMODCACHE_SAVED=$(go env GOMODCACHE); GOCACHE_SAVED=$(go env GOCACHE)
fi
sandboxed() {
  HOME="$SANDBOX_HOME" XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= CLAUDE_CONFIG_DIR= \
    GOMODCACHE="$GOMODCACHE_SAVED" GOCACHE="$GOCACHE_SAVED" "$@"
}
cleanup() { rm -rf "$SANDBOX_HOME" 2>/dev/null; }
trap cleanup EXIT

echo "== agent-deck contributor self-check =="
echo "   base: $BASE_REF ($(git rev-parse --short "$MB"))   body: $BODY_FILE"
echo

# ---------------------------------------------------------------- code checks
if [ -n "$GO_CHANGED" ] && [ "$HAVE_GO" = 1 ]; then
  # 1. gofmt — lint CI fails on formatting.
  UNFMT=$(gofmt -l $GO_CHANGED 2>/dev/null)
  if [ -z "$UNFMT" ]; then PASS gofmt
  else FAIL gofmt "run: gofmt -w $(echo $UNFMT | tr '\n' ' ')"; fi

  # 2. go vet
  if OUT=$(sandboxed go vet ./... 2>&1); then PASS go-vet
  else FAIL go-vet "$(echo "$OUT" | head -3 | tr '\n' ' | ')"; fi

  # 3. go build
  if OUT=$(sandboxed go build ./... 2>&1); then PASS go-build
  else FAIL go-build "$(echo "$OUT" | head -3 | tr '\n' ' | ')"; fi

  # 4. sandboxed tests (CI invocation; touched packages by default, FULL_TESTS=1 for all)
  if [ "${FULL_TESTS:-0}" = 1 ]; then
    PKGS="./..."
  else
    PKGS=$(printf '%s\n' "$GO_CHANGED" | xargs -n1 dirname | sort -u | sed 's|^|./|' | tr '\n' ' ')
  fi
  if OUT=$(sandboxed go test $PKGS 2>&1); then PASS sandboxed-tests "$PKGS"
  else FAIL sandboxed-tests "$(echo "$OUT" | grep -E '^(--- FAIL|FAIL|# )' | head -5 | tr '\n' ' | ')"; fi

  # 5. a test exists for production changes, in a package you actually touched
  if [ -n "$GO_PROD_CHANGED" ] && [ -z "$GO_TESTS_CHANGED" ]; then
    WARN test-added "production .go changed but no *_test.go changed — new behavior needs a test that fails without it"
  elif [ -n "$GO_PROD_CHANGED" ] && [ -n "$GO_TESTS_CHANGED" ]; then
    PROD_DIRS=$(printf '%s\n' "$GO_PROD_CHANGED" | xargs -n1 dirname | sort -u)
    TEST_DIRS=$(printf '%s\n' "$GO_TESTS_CHANGED" | xargs -n1 dirname | sort -u)
    if [ -n "$(comm -12 <(printf '%s\n' "$PROD_DIRS") <(printf '%s\n' "$TEST_DIRS"))" ]; then
      PASS test-added
    else
      WARN test-added "changed tests live only in packages your production change didn't touch — cover the changed packages"
    fi
  elif [ -n "$GO_TESTS_CHANGED" ]; then
    PASS test-added
  fi

  # 6. revert-check — your changed tests must FAIL on the base without your fix.
  #    (Mirrors the correctness lens: revert the core hunks, re-run the tests.)
  if [ -n "$GO_PROD_CHANGED" ] && [ -n "$GO_TESTS_CHANGED" ] && [ "${SKIP_REVERT_CHECK:-0}" != 1 ]; then
    WT=$(mktemp -d)/wt
    if git worktree add -q "$WT" "$MB" 2>/dev/null; then
      RC_OK=1
      for f in $GO_TESTS_CHANGED; do
        mkdir -p "$WT/$(dirname "$f")"; cp "$f" "$WT/$f"
      done
      RC_PKGS=$(printf '%s\n' "$GO_TESTS_CHANGED" | xargs -n1 dirname | sort -u | sed 's|^|./|' | tr '\n' ' ')
      if OUT=$( cd "$WT" && sandboxed go test $RC_PKGS 2>&1 ); then
        FAIL revert-check "your tests still PASS without your change — they prove nothing; make at least one fail on base"
        RC_OK=0
      else
        if echo "$OUT" | grep -q 'build failed\|cannot find\|undefined:'; then
          WARN revert-check "tests do not compile on base (new symbols) — acceptable, but note it in Evidence"
        else
          PASS revert-check "tests fail without the change, as they should"
        fi
      fi
      git worktree remove -f "$WT" 2>/dev/null; rm -rf "$(dirname "$WT")" 2>/dev/null
      : "$RC_OK"
    else
      WARN revert-check "could not create base worktree — run manually: revert non-test hunks, re-run tests, expect failure"
    fi
  elif [ "${SKIP_REVERT_CHECK:-0}" = 1 ]; then
    SKIP revert-check "SKIP_REVERT_CHECK=1"
  fi
elif [ -n "$GO_CHANGED" ]; then
  WARN go-toolchain "Go not installed — gofmt/vet/build/test/revert checks NOT run"
else
  SKIP go-checks "no .go files changed"
fi

# ------------------------------------------------------------- diff hygiene
ADD=$(git diff --numstat "$MB" HEAD | awk '{a+=$1} END {print a+0}')
DEL=$(git diff --numstat "$MB" HEAD | awk '{d+=$2} END {print d+0}')
TOTAL=$((ADD + DEL))
if [ "$ADD" -gt 3000 ] && [ -z "${LINKED_ISSUE:-}" ]; then
  FAIL diff-size "+$ADD lines with no linked issue/Discussion — gate flags this needs-discussion; agree the shape first (or set LINKED_ISSUE=<n>)"
elif [ "$TOTAL" -gt 400 ]; then
  WARN diff-size "+$ADD/-$DEL — over ~400 changed lines review quality measurably degrades; split what can split, or justify the size in the PR body"
else
  PASS diff-size "+$ADD/-$DEL"
fi

if echo "$CHANGED_ALL" | grep -qx 'CHANGELOG.md'; then
  FAIL forbidden-paths "CHANGELOG.md is edited — entries are added at landing time; drop it from the diff"
else
  PASS no-changelog
fi
if echo "$CHANGED_ALL" | grep -q '^\.github/'; then
  WARN dot-github ".github/ changes never auto-merge (human security gate) — keep them out of unrelated PRs"
fi
if echo "$CHANGED_ALL" | grep -qE '^go\.(mod|sum)$'; then
  WARN dependency-change "go.mod/go.sum touched — community dependency changes get extra scrutiny; justify the module in the PR body"
  if [ "$HAVE_GO" = 1 ]; then
    if OUT=$(sandboxed go mod verify 2>&1) && echo "$OUT" | grep -q 'all modules verified'; then
      PASS go-mod-verify
    else
      FAIL go-mod-verify "$(echo "$OUT" | head -2 | tr '\n' ' | ')"
    fi
  fi
  if git diff "$MB" HEAD -- go.mod | grep -qE '^\+\s*replace\s'; then
    FAIL replace-directive "a 'replace' directive is an automatic stop-for-human — remove it"
  fi
fi
if printf '%s\n' "$ADDED_CODE" | grep -q 'GOPROXY'; then
  FAIL goproxy-override "GOPROXY/module-proxy override introduced — automatic stop-for-human; remove it"
fi

# --------------------------------------------------- hidden-logic self-scan
# Author-side version of the maintainer's hidden-logic sweep (see
# references/security-self-scan.md for the full manual list). ADDED_LINES and
# ADDED_CODE are defined near the top of the script.
scan_hits() { printf '%s\n' "$ADDED_CODE" | grep -nE "$1" | head -3; }

HITS=$(printf '%s\n' "$ADDED_LINES" | perl -ne 'print if /[\x{200B}-\x{200F}\x{202A}-\x{202E}\x{2060}\x{FEFF}]/' | head -3)
if [ -n "$HITS" ]; then FAIL invisible-chars "zero-width/RTL-override characters in added lines — remove them"; else PASS invisible-chars; fi

HITS=$(scan_hits 'curl[^|;&]*\|[[:space:]]*(ba|z)?sh|wget[^|;&]*\|[[:space:]]*sh')
if [ -n "$HITS" ]; then FAIL curl-pipe-sh "curl|bash / wget|sh introduced — automatic stop-for-human: $HITS"; else PASS curl-pipe-sh; fi

HITS=$(scan_hits '0o?777|0o?666')
if [ -n "$HITS" ]; then WARN file-perms "world-writable mode in added lines — secrets/control dirs need 0600/0700: $HITS"; fi

HITS=$(scan_hits 'InsecureSkipVerify|crypto/(md5|rc4|des)')
if [ -n "$HITS" ]; then WARN weak-crypto "weak crypto / TLS-verify bypass in added lines — confirm or remove: $HITS"; fi

HITS=$(scan_hits 'time\.Now\(\)\.Unix\(\)[[:space:]]*>[[:space:]]*[0-9]{6,}')
if [ -n "$HITS" ]; then WARN time-bomb "literal-timestamp gate in added code — only OK as a fixed clock inside _test.go: $HITS"; fi

PROD_ADDED=$(git diff "$MB" HEAD -- '*.go' ':!*_test.go' | grep '^+' | grep -v '^+++' | sed 's/^+//')
HITS=$(printf '%s\n' "$PROD_ADDED" | grep -nE 'net\.Dial|http\.(Get|Post)\(|websocket|smtp\.' | head -3)
if [ -n "$HITS" ]; then WARN network-calls "new network calls in production paths — the security lens reads each one; justify in the PR body: $HITS"; fi
HITS=$(printf '%s\n' "$PROD_ADDED" | grep -nE 'exec\.Command(Context)?\([^)]*(\+|fmt\.Sprintf)' | head -3)
if [ -n "$HITS" ]; then WARN constructed-exec "exec of a constructed string — pass validated argv, never a shell-interpolated string: $HITS"; fi

# --------------------------------------------------------------- PR-body lint
# Mirrors .github/workflows/pr-intake.yml checks 1-3 + the INTAKE.md marker contract.
if [ ! -f "$BODY_FILE" ]; then
  WARN pr-body "no PR body draft at '$BODY_FILE' — write one from references/pr-body-template.md, then re-run: self-check.sh $BODY_FILE"
else
  BODY=$(cat "$BODY_FILE")
  section() {  # text of "## <h>" up to next "## "
    printf '%s\n' "$BODY" | awk -v h="## $1" '
      /^## / { f = ($0 == h) ? 1 : 0; next } f { print }'
  }
  meaningful() {  # strip HTML comments, checkbox lines, blanks
    printf '%s\n' "$1" | perl -0pe 's/<!--.*?-->//gs' | grep -vE '^[-*][[:space:]]*\[[ xX]\]' | grep -q '[^[:space:]]'
  }

  MISSING=""
  for h in "What problem does this solve?" "Why this change" "User impact" "AI disclosure" "What actually bothered you" "Checklist"; do
    printf '%s\n' "$BODY" | grep -q "^## $h" || MISSING="$MISSING '## $h'"
  done
  if [ -z "$MISSING" ]; then PASS body-headings
  else FAIL body-headings "missing required section(s):$MISSING (exact headings from .github/PULL_REQUEST_TEMPLATE.md)"; fi

  AI_SECTION=$(section "AI disclosure")
  BOXES=$(printf '%s\n' "$AI_SECTION" | grep -cE '^[-*][[:space:]]*\[[xX]\]' || true)
  AI_KIND=""
  if [ "$BOXES" = 1 ]; then
    LINE=$(printf '%s\n' "$AI_SECTION" | grep -E '^[-*][[:space:]]*\[[xX]\]' | tr '[:upper:]' '[:lower:]')
    case "$LINE" in
      *authored*) AI_KIND=authored ;; *assisted*) AI_KIND=assisted ;; *human*) AI_KIND=human ;;
    esac
    if [ -n "$AI_KIND" ]; then
      PASS ai-disclosure "$AI_KIND"
    else
      FAIL ai-disclosure "checked box not recognized — use the template's Human-written / AI-assisted / AI-authored lines"
    fi
  elif [ "$BOXES" = 0 ]; then
    FAIL ai-disclosure "no box checked — check exactly one (Human-written / AI-assisted / AI-authored)"
  else
    FAIL ai-disclosure "$BOXES boxes checked — check exactly one"
  fi

  if [ "$AI_KIND" = assisted ] || [ "$AI_KIND" = authored ]; then
    MODEL_LINE=$(printf '%s\n' "$AI_SECTION" | grep -i 'Model(s), if AI helped' | perl -pe 's/<!--.*?-->//g; s/.*helped:\**//i')
    if printf '%s' "$MODEL_LINE" | grep -q '[^[:space:]]'; then PASS model-named "$(echo $MODEL_LINE)"
    else FAIL model-named "AI helped but no model named — name it (e.g. claude-opus-4-x) or write 'unsure'"; fi
  fi

  if meaningful "$(section 'What actually bothered you')"; then PASS human-intent
  else FAIL human-intent "empty — one real sentence; if an agent opened this, quote what the human asked for (the one field intake cannot accept blank)"; fi

  if meaningful "$(section 'Evidence')"; then PASS evidence
  else WARN evidence "empty — required for behavior changes: real output, capture, or before/after logs (mock-only proof is insufficient)"; fi

  MARKER=$(printf '%s\n' "$BODY" | grep -oE '<!--[[:space:]]*gate:ai=[^[:space:]]+[[:space:]]+model=[^[:space:]]+[[:space:]]+intent=[^[:space:]]+[[:space:]]*-->' | tail -1)
  if [ -z "$MARKER" ]; then
    WARN gate-marker "no gate marker — add as the LAST line: <!-- gate:ai=$AI_KIND model=<name-or-unsure> intent=yes -->"
  elif printf '%s' "$MARKER" | grep -q '<[a-z|-]*>'; then
    FAIL gate-marker "marker still has template placeholders — fill in real values"
  else
    M_AI=$(printf '%s' "$MARKER" | sed -E 's/.*gate:ai=([^ ]+).*/\1/')
    LAST_LINE=$(printf '%s\n' "$BODY" | grep -v '^[[:space:]]*$' | tail -1)
    if [ -n "$AI_KIND" ] && [ "$M_AI" != "$AI_KIND" ]; then
      FAIL gate-marker "marker says ai=$M_AI but the checked box says $AI_KIND — make them agree (visible sections are authoritative)"
    elif [ "$LAST_LINE" != "$MARKER" ]; then
      WARN gate-marker "marker present but not the last line of the body — move it to the very end"
    else
      PASS gate-marker "$MARKER"
    fi
  fi

  if printf '%s\n' "$BODY" | grep -qE 'CRITICAL|URGENT|\bSECURITY\b'; then
    WARN urgency-language "urgency language in the body — without a concrete repro this trips the slop check; real vulnerabilities go through SECURITY.md privately"
  fi
fi

# ----------------------------------------------------------------- summary
echo
echo "== summary: $pass PASS, $fail FAIL, $warn WARN, $skip skipped =="
if [ "$fail" -gt 0 ]; then
  echo "Fix every FAIL before opening the PR (exact fixes: references/gate-spec.md)."
  exit 1
fi
if [ "$warn" -gt 0 ]; then
  echo "Resolve each WARN or explain it honestly in the PR body — stated imperfections pass; silent ones become review flags."
fi
echo "Ready to open. Use the PR body you just linted, marker line last."
exit 0
