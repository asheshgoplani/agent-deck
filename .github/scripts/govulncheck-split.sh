#!/usr/bin/env bash
# govulncheck-split.sh: summarise a `govulncheck -format json` stream.
#
# Usage: govulncheck-split.sh vuln.json
#
# Groups findings by OSV id and classifies each OSV by its strongest finding:
#   called    a finding's innermost frame names a function (symbol reachable)
#   imported  a finding names a package but no function
#   required  only the module is in the build list
# Writes one markdown table row per OSV to $GITHUB_STEP_SUMMARY (stdout when
# unset), one ::error annotation per called OSV and one ::notice per other OSV.
#
# Exit codes: 1 when any OSV is called (the same strictness as plain
# `govulncheck ./...`), 2 when the input is not a govulncheck stream (a scan
# that crashed must never pass), 0 otherwise. Needs bash and jq only.

set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 vuln.json" >&2
  exit 2
fi
input="$1"
summary="${GITHUB_STEP_SUMMARY:-/dev/stdout}"

if ! entries="$(jq -c -s '
  def level: if .trace[0].function then "called"
             elif .trace[0].package then "imported"
             else "required" end;
  def rank: {"called": 0, "imported": 1, "required": 2}[.];
  def frame: (.package // .module // "") as $p
    | ($p | split("/") | last) as $short
    | if .function then
        [$short, ((.receiver // "") | ltrimstr("*") | select(. != "")), .function]
        | join(".")
      else $p end;
  if any(.[]; type == "object" and has("config")) | not then
    error("no govulncheck config message")
  else . end
  | (map(select(type == "object" and has("osv")) | .osv | {key: .id, value: (.summary // "")})
     | from_entries) as $osv
  | [ .[] | select(type == "object" and has("finding")) | .finding ]
  | group_by(.osv)
  | map(
      (map(level | rank) | min) as $r
      | (map(select((level | rank) == $r)) | first) as $f
      | {
          id: $f.osv,
          summary: ($osv[$f.osv] // ""),
          level: ($f | level),
          rank: $r,
          where: ($f.trace[0].package // $f.trace[0].module // ""),
          found: ($f.trace[0].version // ""),
          fixed: ($f.fixed_version // "none"),
          trace: (if $r == 0 then ($f.trace | reverse | map(frame) | join(" -> ")) else "" end)
        })
  | sort_by(.rank, .id)
' "$input" 2>&1)"; then
  echo "::error title=govulncheck::$input is not a govulncheck JSON stream: ${entries//$'\n'/ }"
  exit 2
fi

{
  echo "## govulncheck"
  echo
  if [ "$entries" = "[]" ]; then
    echo "No vulnerabilities found."
  else
    echo "| OSV | Module / package | Found | Fixed | Level | Example trace |"
    echo "| --- | --- | --- | --- | --- | --- |"
    jq -r '.[] | [.id, .where, .found, .fixed, .level, .trace]
      | map(gsub("\\|"; "\\|") | gsub("\n"; " ")) | "| " + join(" | ") + " |"' <<<"$entries"
  fi
} >>"$summary"

# Workflow command values must escape %, CR and LF.
jq -r '
  def esc: gsub("%"; "%25") | gsub("\r"; "%0D") | gsub("\n"; "%0A");
  .[] | if .level == "called"
    then "::error title=govulncheck::\(.id) \(.summary) (fixed in \(.fixed))" | esc
    else "::notice title=govulncheck::\(.id) \(.summary) (\(.level), fixed in \(.fixed))" | esc
    end' <<<"$entries"

if jq -e 'any(.[]; .level == "called")' <<<"$entries" >/dev/null; then
  exit 1
fi
exit 0
