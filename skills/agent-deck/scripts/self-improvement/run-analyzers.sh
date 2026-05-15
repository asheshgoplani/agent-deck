#!/usr/bin/env bash
# Driver: analyze every distilled transcript that doesn't yet have a report.
# Spawns one agent-deck sub-agent per distilled file, sequentially, with pacing.
# Skips files that already have a report (resumable).

set -u

ANALYSIS_DIR="$HOME/.agent-deck/conductor/agent-deck/analysis"
DISTILLED_DIR="$ANALYSIS_DIR/distilled"
REPORTS_DIR="$ANALYSIS_DIR/reports"
PROMPT_PATH="$ANALYSIS_DIR/prompts/analyzer.md"
STATUS_FILE="$ANALYSIS_DIR/run-status.log"
SKILL_DIR="$HOME/.claude/plugins/cache/agent-deck/agent-deck/12c0a65dfb13/skills/agent-deck"

PACE_SECONDS=30
PER_ANALYZER_TIMEOUT=600   # seconds, for the --wait

mkdir -p "$REPORTS_DIR"

ts() { date -Is; }

log() {
    echo "[$(ts)] $*" | tee -a "$STATUS_FILE"
}

log "===== run.sh start ====="
log "ANALYSIS_DIR=$ANALYSIS_DIR"

# Build worklist: distilled files without a matching report.
worklist=()
for d in "$DISTILLED_DIR"/*.md; do
    sid=$(basename "$d" .md)
    if [[ -s "$REPORTS_DIR/$sid.md" ]]; then
        log "SKIP $sid (report exists)"
        continue
    fi
    worklist+=("$sid")
done

total=${#worklist[@]}
log "Worklist size: $total"

if [[ $total -eq 0 ]]; then
    log "Nothing to do. Exiting."
    exit 0
fi

idx=0
ok=0
fail=0
for sid in "${worklist[@]}"; do
    idx=$((idx + 1))
    log "[$idx/$total] BEGIN $sid"

    input_path="$DISTILLED_DIR/$sid.md"
    output_path="$REPORTS_DIR/$sid.md"

    worker_prompt="Follow the instructions in:
  $PROMPT_PATH

Substitute these values throughout:
  {INPUT_PATH}  = $input_path
  {OUTPUT_PATH} = $output_path
  {SID}         = $sid

Read the analyzer.md file first for the full schema. Then read INPUT_PATH. Then write OUTPUT_PATH per the schema. Then print DONE: $output_path and exit.

Do not propose follow-up work. Do not touch any files except OUTPUT_PATH."

    start_ts=$(date +%s)
    if "$SKILL_DIR/scripts/launch-subagent.sh" \
            "analyze-$sid" \
            "$worker_prompt" \
            --path "$HOME/.agent-deck/conductor/agent-deck" \
            --wait \
            --timeout "$PER_ANALYZER_TIMEOUT" \
            >>"$STATUS_FILE" 2>&1; then
        end_ts=$(date +%s)
        elapsed=$((end_ts - start_ts))
        if [[ -s "$output_path" ]]; then
            ok=$((ok + 1))
            log "[$idx/$total] OK $sid (${elapsed}s, $(wc -c <"$output_path") bytes)"
        else
            fail=$((fail + 1))
            log "[$idx/$total] EMPTY $sid (${elapsed}s, no output file)"
        fi
    else
        fail=$((fail + 1))
        log "[$idx/$total] FAIL $sid (launch-subagent returned non-zero)"
    fi

    # Cleanup: remove the worker session to keep registry tidy
    agent-deck session remove "analyze-$sid" >/dev/null 2>&1 || true

    if [[ $idx -lt $total ]]; then
        log "Sleeping ${PACE_SECONDS}s before next..."
        sleep "$PACE_SECONDS"
    fi
done

log "===== run.sh done ====="
log "Summary: ok=$ok fail=$fail total=$total"
