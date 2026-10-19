#!/usr/bin/env bash
# Flag a fix PR (fix: title, or it closes an issue labelled bug) that adds no regression test.
#   scripts/check-fix-has-test.sh --pr 123 [--advisory]
#   scripts/check-fix-has-test.sh --range origin/main..HEAD --title "fix: ..." [--body-file body.md] [--advisory]
# Exit 1 with a one-line reason when the test is missing, 0 otherwise (always 0 with --advisory), 2 on unreadable input.
# Reads GitHub through gh, read-only. Logic and fixtures: scripts/check-fix-has-test/.
set -euo pipefail
bin="${TMPDIR:-/tmp}/check-fix-has-test-$(id -u)"  # rebuilt every run, reused path: nothing to clean up
# Build from the repo root, run from the caller's directory so relative paths (--body-file) mean what they say.
(cd "$(dirname "$0")/.." && go build -o "$bin" ./scripts/check-fix-has-test)
exec "$bin" "$@"
