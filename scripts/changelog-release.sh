#!/usr/bin/env bash
# changelog-release.sh: keep CHANGELOG.md's Unreleased section and release
# headings in step with releases.
#
# Usage:
#   scripts/changelog-release.sh [--file F] <version> [<date>]
#       Move everything under "## [Unreleased]" to a new
#       "## [<version>] - <date>" heading right below it, and leave an empty
#       "## [Unreleased]" on top for the next changes. <date> defaults to
#       today in Europe/Berlin (YYYY-MM-DD). A leading "v" is dropped.
#       Refuses (exit 1) when there is no Unreleased section, when it is
#       empty, or when <version> already has a heading.
#   scripts/changelog-release.sh [--file F] --check [<version>]
#       Exit 0 when the changelog has exactly one "## [Unreleased]" heading
#       and, if <version> is given, a "## [<version>]" heading.
#
# --file defaults to CHANGELOG.md at the top of the git checkout (or the
# current directory outside one). Every user-visible change adds one plain
# line under Unreleased when it lands; the release moves them with this
# script (make changelog-release VERSION=X.Y.Z), and the release's
# "### Highlights" are written on top by hand.
set -uo pipefail

usage() { awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; }
die() { echo "changelog-release: $1" >&2; exit "${2:-1}"; }

file="" check=0 version="" date=""
while [ $# -gt 0 ]; do
  case $1 in
    --file) [ $# -ge 2 ] || die "--file needs a path" 2; file=$2; shift 2 ;;
    --check) check=1; shift ;;
    -h|--help) usage; exit 0 ;;
    -*) die "unknown flag $1 (see --help)" 2 ;;
    *)
      if [ -z "$version" ]; then version=${1#v}
      elif [ -z "$date" ] && [ "$check" = 0 ]; then date=$1
      else die "too many arguments (see --help)" 2
      fi
      shift ;;
  esac
done

if [ -z "$file" ]; then
  root=$(git rev-parse --show-toplevel 2>/dev/null) || root=.
  file=$root/CHANGELOG.md
fi
[ -f "$file" ] || die "$file not found" 2
if [ -n "$version" ] && ! [[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$ ]]; then
  die "version '$version' is not X.Y.Z" 2
fi

unreleased=$(grep -c '^## \[Unreleased\][[:space:]]*$' "$file")
# has_heading <version>: the file has a "## [<version>]" heading.
# The "]" ends the version, so any spacing or date after it still counts.
has_heading() { awk -v h="## [$1]" 'index($0, h) == 1 { f = 1 } END { exit !f }' "$file"; }

if [ "$check" = 1 ]; then
  ok=1
  if [ "$unreleased" != 1 ]; then
    echo "$file: want exactly one '## [Unreleased]' heading, found $unreleased" >&2; ok=0
  fi
  if [ -n "$version" ] && ! has_heading "$version"; then
    echo "$file: no '## [$version]' heading; run scripts/changelog-release.sh $version" >&2; ok=0
  fi
  [ "$ok" = 1 ] || exit 1
  echo "$file: Unreleased section present${version:+, v$version has an entry}"
  exit 0
fi

[ -n "$version" ] || die "missing <version> (see --help)" 2
if [ -z "$date" ]; then date=$(TZ=Europe/Berlin date +%F); fi
[[ $date =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || die "date '$date' is not YYYY-MM-DD" 2
[ "$unreleased" = 1 ] || die "$file: want exactly one '## [Unreleased]' heading, found $unreleased"
has_heading "$version" && die "$file already has a '## [$version]' heading"

# Write a copy next to the file (cp -p keeps its mode) and rename it over
# the original, so a failed write never leaves a half-written changelog.
tmp=$(mktemp "$file.XXXXXX") || exit 1
trap 'rm -f "$tmp"' EXIT
cp -p "$file" "$tmp" || exit 1
# Body: the lines between "## [Unreleased]" and the next "## " heading,
# without blank lines at either end.
if ! awk -v heading="## [$version] - $date" '
  state == 0 && /^## \[Unreleased\][[:space:]]*$/ { print; state = 1; next }
  state == 1 && /^## / { state = 2; flush(); print; next }
  state == 1 { body[++n] = $0; next }
  { print }
  END {
    if (state == 1) flush()
    if (!found) exit 3
  }
  function flush(  first, last, i) {
    for (first = 1; first <= n && body[first] ~ /^[[:space:]]*$/; first++) {}
    for (last = n; last >= first && body[last] ~ /^[[:space:]]*$/; last--) {}
    if (first > last) exit 3
    found = 1
    print ""
    print heading
    print ""
    for (i = first; i <= last; i++) print body[i]
    print ""
  }
' "$file" >"$tmp"; then
  die "$file: nothing under '## [Unreleased]' to release"
fi
mv -f "$tmp" "$file" || exit 1
echo "$file: moved Unreleased to '## [$version] - $date'"
