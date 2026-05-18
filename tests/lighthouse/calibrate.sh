#!/usr/bin/env bash
# Lighthouse CI threshold calibration script.
#
# Runs 10 Lighthouse collections against the test server on the current
# codebase to establish p50 and p95 baselines per metric.
#
# Output: calibration report with recommended thresholds.
#
# Usage:
#   ./tests/lighthouse/calibrate.sh
#
# Prerequisites:
#   - make build (Go binary at ./build/agent-deck)
#   - Node.js >= 18 with npx
#   - Chrome/Chromium installed

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

BINARY="./build/agent-deck"
PORT=19998
SERVER_URL="http://127.0.0.1:${PORT}"
LHCI_VERSION="0.15.1"
NUM_RUNS=10
SERVER_PID=""
WORK_DIR="$(mktemp -d)"

cleanup() {
  if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    echo "[calibrate] Stopping test server (PID $SERVER_PID)..."
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  echo "[calibrate] Cleaning up work directory: $WORK_DIR"
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT INT TERM

# --- Pre-flight ---

if [ ! -x "$BINARY" ]; then
  echo "ERROR: $BINARY not found. Run 'make build' first."
  exit 2
fi

if ! command -v npx &>/dev/null; then
  echo "ERROR: npx not found. Install Node.js (>= 18)."
  exit 2
fi

# --- Create temporary calibration config ---

CALIB_CONFIG="$WORK_DIR/calibrate-lighthouserc.json"
cat > "$CALIB_CONFIG" <<'CALEOF'
{
  "ci": {
    "collect": {
      "url": ["http://127.0.0.1:19998/"],
      "numberOfRuns": 10,
      "settings": {
        "preset": "desktop",
        "throttling": {
          "rttMs": 40,
          "throughputKbps": 10240,
          "cpuSlowdownMultiplier": 1
        },
        "skipAudits": [
          "uses-http2",
          "canonical",
          "maskable-icon",
          "tap-targets"
        ]
      }
    }
  }
}
CALEOF

# --- Start test server ---

echo "[calibrate] Starting test server on port $PORT..."
AGENTDECK_PROFILE=_test "$BINARY" web --no-tui --listen "127.0.0.1:${PORT}" --token test &
SERVER_PID=$!

echo "[calibrate] Waiting for /healthz (max 30s)..."
for i in $(seq 1 30); do
  if curl -sf "${SERVER_URL}/healthz" > /dev/null 2>&1; then
    echo "[calibrate] Server ready after ${i}s."
    break
  fi
  if [ "$i" -eq 30 ]; then
    echo "ERROR: Server did not become ready within 30 seconds."
    exit 2
  fi
  sleep 1
done

# --- Pre-warm ---

echo "[calibrate] Pre-warming server..."
curl -s "${SERVER_URL}/" > /dev/null
sleep 1

# --- Collect 10 runs ---

echo "[calibrate] Collecting $NUM_RUNS Lighthouse runs (this takes ~3-5 minutes)..."
LHCI_BUILD_CONTEXT__CURRENT_HASH="$(git rev-parse HEAD 2>/dev/null || echo 'unknown')" \
  npx "@lhci/cli@${LHCI_VERSION}" collect --config="$CALIB_CONFIG"

# --- Parse results ---

echo ""
echo "============================================"
echo "  Lighthouse CI Calibration Report"
echo "  Runs: $NUM_RUNS | Commit: $(git rev-parse --short HEAD 2>/dev/null || echo 'unknown')"
echo "============================================"
echo ""

# Use Node.js to parse the JSON results and compute percentiles
node -e "
const fs = require('fs');
const path = require('path');

// Find lhci results directory
const lhciDir = path.join(process.cwd(), '.lighthouseci');
if (!fs.existsSync(lhciDir)) {
  console.error('ERROR: .lighthouseci/ directory not found. Collection may have failed.');
  process.exit(1);
}

const files = fs.readdirSync(lhciDir)
  .filter(f => f.startsWith('lhr-') && f.endsWith('.json'))
  .map(f => JSON.parse(fs.readFileSync(path.join(lhciDir, f), 'utf-8')));

if (files.length === 0) {
  console.error('ERROR: No Lighthouse result files found in .lighthouseci/');
  process.exit(1);
}

console.log('Result files found: ' + files.length);
console.log('');

// Extract metrics
const metrics = {};
const auditKeys = [
  'total-byte-weight',
  'first-contentful-paint',
  'largest-contentful-paint',
  'total-blocking-time',
  'speed-index',
  'cumulative-layout-shift',
];

for (const key of auditKeys) {
  metrics[key] = files
    .map(f => f.audits && f.audits[key] && f.audits[key].numericValue)
    .filter(v => v !== undefined && v !== null)
    .sort((a, b) => a - b);
}

// resource-summary:script:size requires special handling
metrics['resource-summary:script:size'] = files
  .map(f => {
    const audit = f.audits && f.audits['resource-summary'];
    if (!audit || !audit.details || !audit.details.items) return null;
    const scripts = audit.details.items.find(i => i.resourceType === 'script');
    return scripts ? scripts.transferSize : null;
  })
  .filter(v => v !== null)
  .sort((a, b) => a - b);

function percentile(arr, p) {
  if (arr.length === 0) return 'N/A';
  const idx = Math.ceil(arr.length * p / 100) - 1;
  return arr[Math.max(0, idx)];
}

function formatBytes(b) {
  return typeof b === 'number' ? (b / 1024).toFixed(1) + ' KB' : 'N/A';
}

function formatMs(ms) {
  return typeof ms === 'number' ? ms.toFixed(0) + ' ms' : 'N/A';
}

console.log('Metric                          | p50        | p95        | Hard (p95+10%) | Soft (p95+20%)');
console.log('--------------------------------|------------|------------|----------------|----------------');

const thresholds = {};

for (const key of Object.keys(metrics)) {
  const arr = metrics[key];
  const p50 = percentile(arr, 50);
  const p95 = percentile(arr, 95);
  const hard = typeof p95 === 'number' ? Math.ceil(p95 * 1.1) : 'N/A';
  const soft = typeof p95 === 'number' ? Math.ceil(p95 * 1.2) : 'N/A';

  const isBytes = key.includes('byte') || key.includes('size');
  const isCLS = key.includes('layout-shift');
  const fmt = isBytes ? formatBytes : (isCLS ? (v => typeof v === 'number' ? v.toFixed(4) : 'N/A') : formatMs);

  console.log(
    key.padEnd(32) + '| ' +
    fmt(p50).padEnd(11) + '| ' +
    fmt(p95).padEnd(11) + '| ' +
    (isBytes || isCLS ? String(hard).padEnd(15) : 'N/A'.padEnd(15)) + '| ' +
    (!isBytes && !isCLS ? String(soft).padEnd(15) : 'N/A'.padEnd(15))
  );

  thresholds[key] = { p50, p95, hard, soft };
}

console.log('');
console.log('--- Recommended .lighthouserc.json assertions ---');
console.log('');
console.log(JSON.stringify({
  'total-byte-weight': ['error', { maxNumericValue: thresholds['total-byte-weight'].hard }],
  'resource-summary:script:size': ['error', { maxNumericValue: thresholds['resource-summary:script:size'].hard }],
  'first-contentful-paint': ['warn', { maxNumericValue: thresholds['first-contentful-paint'].soft }],
  'largest-contentful-paint': ['warn', { maxNumericValue: thresholds['largest-contentful-paint'].soft }],
  'total-blocking-time': ['warn', { maxNumericValue: thresholds['total-blocking-time'].soft }],
  'speed-index': ['warn', { maxNumericValue: thresholds['speed-index'].soft }],
  'cumulative-layout-shift': ['error', { maxNumericValue: 0.1 }],
}, null, 2));

console.log('');
console.log('Copy the assertions block above into .lighthouserc.json ci.assert.assertions');
console.log('(keep is-on-https, uses-http2, redirects-http, maskable-icon as off)');
"

echo ""
echo "[calibrate] Done. Review the thresholds above and update .lighthouserc.json."
echo "[calibrate] Then verify with: ./tests/lighthouse/budget-check.sh"
