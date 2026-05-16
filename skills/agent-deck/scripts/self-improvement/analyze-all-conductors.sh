#!/usr/bin/env bash
# Analyze 5 small conductors (excluding agent-deck which is done, and innotrade
# which is too large for one pass).
#
# For each conductor:
#   1. Ensure analyzer prompt is in place
#   2. For each distilled file without a report: spawn analyzer (--wait)
#   3. After all reports are done: spawn one synthesizer → FINDINGS.md
#   4. 60s pacing between calls (kind to rate limits while file-all may be running)

set -u

SKILL_DIR="$HOME/.claude/plugins/cache/agent-deck/agent-deck/12c0a65dfb13/skills/agent-deck"
SOURCE_CONDUCTOR="$HOME/.agent-deck/conductor/agent-deck"
SOURCE_PROMPTS="$SOURCE_CONDUCTOR/analysis/prompts"
PACE=60
PER_ANALYZER_TIMEOUT=600

# Order: smallest → largest, so failures happen early
CONDUCTORS=(ryan sherif opengraphdb si personal)

ts() { date -Is; }
log() { echo "[$(ts)] $*"; }

log "===== analyze-other-conductors start ====="

for conductor in "${CONDUCTORS[@]}"; do
    CDIR="$HOME/.agent-deck/conductor/$conductor"
    DISTILLED="$CDIR/analysis/distilled"
    REPORTS="$CDIR/analysis/reports"
    PROMPTS="$CDIR/analysis/prompts"

    [ -d "$DISTILLED" ] || { log "SKIP $conductor: no distilled dir"; continue; }
    mkdir -p "$REPORTS" "$PROMPTS"
    cp "$SOURCE_PROMPTS/analyzer.md" "$PROMPTS/analyzer.md"
    cp "$SOURCE_PROMPTS/synthesizer.md" "$PROMPTS/synthesizer.md"

    log "----- conductor: $conductor -----"

    # Phase 1: analyze each distilled file that doesn't yet have a report
    files=( "$DISTILLED"/*.md )
    total=${#files[@]}
    idx=0
    ok=0
    fail=0
    for distilled_file in "${files[@]}"; do
        idx=$((idx + 1))
        sid=$(basename "$distilled_file" .md)
        report_path="$REPORTS/$sid.md"
        if [[ -s "$report_path" ]]; then
            log "  [$conductor $idx/$total] SKIP $sid (report exists)"
            continue
        fi

        log "  [$conductor $idx/$total] BEGIN $sid"
        worker_prompt="Follow the instructions in:
  $PROMPTS/analyzer.md

Substitute these values throughout:
  {INPUT_PATH}  = $distilled_file
  {OUTPUT_PATH} = $report_path
  {SID}         = $sid

NOTE: This is the $conductor conductor. Same agent-deck capabilities apply (sessions, conductors, watchers, MCP, etc.) but the user's work content is conductor-specific.

Read the analyzer.md file first. Then read INPUT_PATH. Then write OUTPUT_PATH per the schema. Print DONE and exit."

        start_ts=$(date +%s)
        if "$SKILL_DIR/scripts/launch-subagent.sh" \
                "analyze-$conductor-$sid" \
                "$worker_prompt" \
                --path "$CDIR" --wait --timeout "$PER_ANALYZER_TIMEOUT" \
                >/dev/null 2>&1; then
            end_ts=$(date +%s)
            elapsed=$((end_ts - start_ts))
            if [[ -s "$report_path" ]]; then
                ok=$((ok + 1))
                log "  [$conductor $idx/$total] OK $sid (${elapsed}s, $(wc -c <"$report_path") bytes)"
            else
                fail=$((fail + 1))
                log "  [$conductor $idx/$total] EMPTY $sid (${elapsed}s)"
            fi
        else
            fail=$((fail + 1))
            log "  [$conductor $idx/$total] FAIL $sid"
        fi

        agent-deck session stop "analyze-$conductor-$sid" --quiet >/dev/null 2>&1 || true
        agent-deck rm "analyze-$conductor-$sid" >/dev/null 2>&1 || true

        if [[ $idx -lt $total ]]; then
            sleep "$PACE"
        fi
    done

    log "  $conductor analyzer summary: ok=$ok fail=$fail total=$total"

    # Phase 2: synthesize that conductor's reports into FINDINGS.md
    if [[ $ok -gt 0 ]]; then
        log "  [$conductor] BEGIN synthesizer"
        synth_prompt="Follow the instructions in:
  $PROMPTS/synthesizer.md

Substitute:
  {REPORTS_DIR} = $REPORTS
  {OUTPUT_PATH} = $CDIR/FINDINGS.md
  {YYYY-MM-DD}  = $(date +%Y-%m-%d)

Read every file in REPORTS_DIR and produce the merged FINDINGS.md at OUTPUT_PATH per the schema. Then print DONE and exit."

        if "$SKILL_DIR/scripts/launch-subagent.sh" \
                "synthesize-$conductor" \
                "$synth_prompt" \
                --path "$CDIR" --wait --timeout 600 \
                >/dev/null 2>&1; then
            log "  [$conductor] OK synthesizer → $CDIR/FINDINGS.md ($(wc -c <"$CDIR/FINDINGS.md" 2>/dev/null || echo 0) bytes)"
        else
            log "  [$conductor] FAIL synthesizer"
        fi
        agent-deck session stop "synthesize-$conductor" --quiet >/dev/null 2>&1 || true
        agent-deck rm "synthesize-$conductor" >/dev/null 2>&1 || true
    else
        log "  [$conductor] no reports, skipping synthesizer"
    fi

    log "  $conductor done. Pacing 60s before next conductor."
    sleep 60
done

log "===== analyze-other-conductors done ====="
