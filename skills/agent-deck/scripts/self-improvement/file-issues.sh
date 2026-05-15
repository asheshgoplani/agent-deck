#!/usr/bin/env bash
# Interactive issue filer for agent-deck self-improvement findings.
#
# Flow per chosen finding:
#   1. AI session drafts an issue body (privacy-aware)
#   2. AI session audits the draft for any remaining PII
#   3. If SAFE_TO_SHARE: show preview + gh command
#   4. User approves [f]ile / [e]dit / [s]kip / [d]iff / [q]uit
#   5. On approve: gh issue create + manifest update
#
# Never files without explicit per-issue [f] confirmation.

set -u

CONDUCTOR_DIR="$HOME/.agent-deck/conductor/agent-deck"
ANALYSIS_DIR="$CONDUCTOR_DIR/analysis"
SCRIPTS_DIR="$ANALYSIS_DIR/scripts"
PROMPTS_DIR="$ANALYSIS_DIR/prompts"
FINDINGS_PATH="$CONDUCTOR_DIR/FINDINGS.md"
MANIFEST_PATH="$CONDUCTOR_DIR/analysis-manifest.json"
GH_REPO="${GH_REPO:-asheshgoplani/agent-deck}"
SKILL_DIR="${SKILL_DIR:-$HOME/.claude/plugins/cache/agent-deck/agent-deck/12c0a65dfb13/skills/agent-deck}"

# work dir for this session
WORK_DIR=$(mktemp -d -t file-issues-XXXXXX)
trap 'rm -rf "$WORK_DIR"' EXIT

# --- helpers ----------------------------------------------------------------

bold() { printf "\033[1m%s\033[0m\n" "$*"; }
red()  { printf "\033[31m%s\033[0m\n" "$*"; }
yellow() { printf "\033[33m%s\033[0m\n" "$*"; }
green() { printf "\033[32m%s\033[0m\n" "$*"; }

require() {
    for c in "$@"; do
        command -v "$c" >/dev/null 2>&1 || { red "Missing dependency: $c"; exit 1; }
    done
}

# --- preflight --------------------------------------------------------------

require python3 jq gh agent-deck
[[ -f "$FINDINGS_PATH" ]] || { red "FINDINGS.md not found at $FINDINGS_PATH"; exit 1; }
[[ -f "$PROMPTS_DIR/issue-drafter.md" ]] || { red "issue-drafter.md prompt missing"; exit 1; }
[[ -f "$PROMPTS_DIR/auditor.md" ]] || { red "auditor.md prompt missing"; exit 1; }

bold "================================================="
bold "  agent-deck self-improvement: file issues"
bold "================================================="
echo "  conductor:  agent-deck"
echo "  findings:   $FINDINGS_PATH"
echo "  manifest:   $MANIFEST_PATH"
echo "  gh repo:    $GH_REPO"
echo "  work dir:   $WORK_DIR"
echo ""

# --- 1. list candidates -----------------------------------------------------

bold "📂 Loading candidates..."
python3 "$SCRIPTS_DIR/list_candidates.py" \
    --findings "$FINDINGS_PATH" \
    --manifest "$MANIFEST_PATH" \
    --gh-repo "$GH_REPO" \
    --out "$WORK_DIR/candidates.json" || { red "Candidate list failed"; exit 1; }

TOTAL=$(jq -r '.total' "$WORK_DIR/candidates.json")
FILED=$(jq -r '.filed_in_manifest' "$WORK_DIR/candidates.json")
GH_HITS=$(jq -r '.exists_on_gh' "$WORK_DIR/candidates.json")
echo "   Total: $TOTAL  |  Already filed (manifest): $FILED  |  Exists on GH (dedup): $GH_HITS"
echo ""

# --- 2. show menu -----------------------------------------------------------

bold "📋 Candidates (sorted: severity → frequency):"
echo ""
jq -r '.candidates | to_entries | .[:25] | .[] | "  [\(.key + 1 | tostring | (. + " " * (3 - length))[0:3])] \(.value.severity) freq=\(.value.frequency)  \(if .value.already_filed_by_us then "[ALREADY FILED] " elif .value.existing_gh_match then "[GH MATCH] " else "" end)\(.value.title[:90])"' \
    "$WORK_DIR/candidates.json"
echo ""
echo "Pick:  [N]   filing one finding by its number"
echo "       [N,M] multiple (e.g., 1,3,5)"
echo "       [q]   quit"
echo ""
read -rp "> " PICK
[[ "$PICK" == "q" || -z "$PICK" ]] && { echo "bye"; exit 0; }

# Parse picks into array
IFS=',' read -ra PICKS <<< "$PICK"

# --- 3. process each pick ---------------------------------------------------

for raw in "${PICKS[@]}"; do
    idx=$(echo "$raw" | tr -d ' ')
    [[ "$idx" =~ ^[0-9]+$ ]] || { red "skip invalid pick: $raw"; continue; }
    array_idx=$((idx - 1))

    bold ""
    bold "================================================="
    bold "  Processing finding #$idx"
    bold "================================================="

    title=$(jq -r ".candidates[$array_idx].title" "$WORK_DIR/candidates.json")
    fingerprint=$(jq -r ".candidates[$array_idx].fingerprint" "$WORK_DIR/candidates.json")
    severity=$(jq -r ".candidates[$array_idx].severity" "$WORK_DIR/candidates.json")
    already_filed=$(jq -r ".candidates[$array_idx].already_filed_by_us" "$WORK_DIR/candidates.json")
    gh_match=$(jq -r ".candidates[$array_idx].existing_gh_match // \"\"" "$WORK_DIR/candidates.json")

    echo "  title:       $title"
    echo "  severity:    $severity"
    echo "  fingerprint: $fingerprint"
    [[ "$already_filed" == "true" ]] && yellow "  ⚠  ALREADY FILED in manifest. Skipping unless you confirm."
    [[ -n "$gh_match" ]] && yellow "  ⚠  Similar issue exists on GH: $gh_match"
    if [[ "$already_filed" == "true" || -n "$gh_match" ]]; then
        read -rp "  Continue anyway? [y/N] " yn
        [[ "$yn" =~ ^[yY] ]] || { echo "  skipped"; continue; }
    fi

    # --- 3a. AI drafts the issue body
    bold ""
    echo "⏳ Spawning AI drafter session..."
    DRAFT_PATH="$WORK_DIR/issue-${idx}.draft.md"
    DRAFTER_PROMPT="Follow the instructions in:
  $PROMPTS_DIR/issue-drafter.md

Substitute:
  {FINDINGS_PATH} = $FINDINGS_PATH
  {FINDING_KEY}   = $title
  {OUTPUT_PATH}   = $DRAFT_PATH

Read the prompt for the schema, then read FINDINGS_PATH, locate the finding matching FINDING_KEY (use the title), draft the issue body to OUTPUT_PATH, then print DONE and exit."

    "$SKILL_DIR/scripts/launch-subagent.sh" \
        "draft-issue-${idx}" \
        "$DRAFTER_PROMPT" \
        --path "$CONDUCTOR_DIR" --wait --timeout 300 >/dev/null 2>&1
    agent-deck session stop "draft-issue-${idx}" --quiet >/dev/null 2>&1 || true
    agent-deck rm "draft-issue-${idx}" >/dev/null 2>&1 || true

    if [[ ! -s "$DRAFT_PATH" ]]; then
        red "  drafter produced no output. Skipping."
        continue
    fi
    green "  ✓ draft written: $(wc -c <"$DRAFT_PATH") bytes"

    # --- 3b. Audit the draft for PII
    echo "⏳ Spawning AI auditor session..."
    AUDIT_PATH="$WORK_DIR/issue-${idx}.audit.md"
    AUDITOR_PROMPT="Follow the instructions in:
  $PROMPTS_DIR/auditor.md

Substitute:
  {INPUT_PATH}  = $DRAFT_PATH
  {OUTPUT_PATH} = $AUDIT_PATH

Read the auditor.md prompt, then read INPUT_PATH with adversarial fresh eyes for any remaining PII. Write your audit report to OUTPUT_PATH. Then print DONE and exit."

    "$SKILL_DIR/scripts/launch-subagent.sh" \
        "audit-issue-${idx}" \
        "$AUDITOR_PROMPT" \
        --path "$CONDUCTOR_DIR" --wait --timeout 300 >/dev/null 2>&1
    agent-deck session stop "audit-issue-${idx}" --quiet >/dev/null 2>&1 || true
    agent-deck rm "audit-issue-${idx}" >/dev/null 2>&1 || true

    if [[ ! -s "$AUDIT_PATH" ]]; then
        red "  auditor produced no output. Cannot file safely. Skipping."
        continue
    fi

    VERDICT=$(grep -E "^\*\*Overall:\*\*" "$AUDIT_PATH" | head -1 | sed -E 's/.*\*\*Overall:\*\*\s*(\S+).*/\1/')
    HIGH_COUNT=$(grep -c "^- \*\*Line " "$AUDIT_PATH" 2>/dev/null || echo 0)
    echo "  audit verdict: $VERDICT  ($HIGH_COUNT line-level concerns)"

    if [[ "$VERDICT" != "SAFE_TO_SHARE" ]]; then
        red "  ✗ NOT SAFE — review $AUDIT_PATH manually, edit $DRAFT_PATH, re-run."
        echo "  draft: $DRAFT_PATH"
        echo "  audit: $AUDIT_PATH"
        continue
    fi
    green "  ✓ SAFE_TO_SHARE"

    # --- 3c. Show preview + prompt
    bold ""
    bold "📄 Preview ($(wc -c <"$DRAFT_PATH") bytes):"
    echo "-------------------------------------------------"
    head -60 "$DRAFT_PATH"
    echo ""
    if [[ $(wc -l <"$DRAFT_PATH") -gt 60 ]]; then
        yellow "  ... (truncated; view full at $DRAFT_PATH)"
    fi
    echo "-------------------------------------------------"
    echo ""
    bold "Choices:"
    echo "  [f] file it now (gh issue create)"
    echo "  [e] edit the body first (\$EDITOR)"
    echo "  [d] show full body"
    echo "  [a] show full audit report"
    echo "  [s] skip to next finding"
    echo "  [q] quit entirely"
    echo ""

    while true; do
        read -rp "> " CHOICE
        case "$CHOICE" in
            f|F)
                echo ""
                python3 "$SCRIPTS_DIR/file_issue.py" "$DRAFT_PATH" \
                    --repo "$GH_REPO" \
                    --fingerprint "$fingerprint" \
                    --source-citation "FINDINGS.md (run $(date +%Y-%m-%d))" \
                    --manifest "$MANIFEST_PATH"
                break ;;
            e|E)
                "${EDITOR:-vi}" "$DRAFT_PATH"
                yellow "  edited. Re-running auditor on updated draft..."
                # re-audit
                "$SKILL_DIR/scripts/launch-subagent.sh" \
                    "reaudit-issue-${idx}" \
                    "Follow $PROMPTS_DIR/auditor.md with {INPUT_PATH}=$DRAFT_PATH {OUTPUT_PATH}=$AUDIT_PATH then print DONE and exit." \
                    --path "$CONDUCTOR_DIR" --wait --timeout 300 >/dev/null 2>&1
                agent-deck rm "reaudit-issue-${idx}" >/dev/null 2>&1 || true
                VERDICT=$(grep -E "^\*\*Overall:\*\*" "$AUDIT_PATH" | head -1 | sed -E 's/.*\*\*Overall:\*\*\s*(\S+).*/\1/')
                echo "  re-audit verdict: $VERDICT"
                ;;
            d|D)
                cat "$DRAFT_PATH"
                echo "" ;;
            a|A)
                cat "$AUDIT_PATH"
                echo "" ;;
            s|S)
                echo "  skipped"; break ;;
            q|Q)
                echo "  quitting"; exit 0 ;;
            *)
                echo "  unknown choice. Use f/e/d/a/s/q." ;;
        esac
    done
done

bold ""
green "✅ Done. Manifest: $MANIFEST_PATH"
