#!/usr/bin/env node
// Compare two lhci collect outputs and gate on percentage deltas.
//
// Usage:
//   node tests/lighthouse/compare-deltas.mjs <baseDir> <headDir>
//
// Where each dir holds the .lighthouseci/ output from a single `lhci collect`
// run (i.e. one or more lhr-*.json files). The script reads, from each report:
//   - audits['total-byte-weight'].numericValue
//   - audits['resource-summary'].details.items.find(i => i.resourceType === 'script').transferSize
// takes the median per metric per dir, and fails if the head-vs-base delta
// exceeds the threshold env vars below.
//
// Env (all percentages, default 5):
//   MAX_BYTE_WEIGHT_DELTA_PCT  — gates total-byte-weight
//   MAX_SCRIPT_SIZE_DELTA_PCT  — gates resource-summary:script:size
//
// Exit:
//   0 — both metrics within threshold (or base value is zero, see notes)
//   1 — at least one metric exceeded threshold
//   2 — input parse / shape error

import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

const [, , baseDir, headDir] = process.argv;
if (!baseDir || !headDir) {
  console.error('usage: compare-deltas.mjs <baseDir> <headDir>');
  process.exit(2);
}

const MAX_BYTE_WEIGHT_DELTA_PCT = Number(process.env.MAX_BYTE_WEIGHT_DELTA_PCT ?? 5);
const MAX_SCRIPT_SIZE_DELTA_PCT = Number(process.env.MAX_SCRIPT_SIZE_DELTA_PCT ?? 5);

function lhrFiles(dir) {
  if (!existsSync(dir)) return [];
  return readdirSync(dir).filter(f => f.startsWith('lhr-') && f.endsWith('.json'));
}

function loadReports(dir, { allowEmpty = false } = {}) {
  const files = lhrFiles(dir);
  if (files.length === 0) {
    if (allowEmpty) return [];
    console.error(`no lhr-*.json files found in ${dir}`);
    process.exit(2);
  }
  return files.map(f => {
    const path = join(dir, f);
    let raw;
    try {
      raw = readFileSync(path, 'utf8');
    } catch (e) {
      console.error(`failed to read ${path}: ${e.message}`);
      process.exit(2);
    }
    try {
      return JSON.parse(raw);
    } catch (e) {
      console.error(`failed to parse ${path} as JSON: ${e.message}`);
      process.exit(2);
    }
  });
}

function median(values) {
  const sorted = values.filter(v => Number.isFinite(v)).sort((a, b) => a - b);
  if (sorted.length === 0) return NaN;
  const mid = Math.floor(sorted.length / 2);
  return sorted.length % 2 === 0 ? (sorted[mid - 1] + sorted[mid]) / 2 : sorted[mid];
}

function extractByteWeight(report) {
  return report?.audits?.['total-byte-weight']?.numericValue;
}

function extractScriptSize(report) {
  const items = report?.audits?.['resource-summary']?.details?.items ?? [];
  const scripts = items.find(i => i.resourceType === 'script');
  return scripts?.transferSize;
}

function summarize(reports, label) {
  const byteWeight = median(reports.map(extractByteWeight));
  const scriptSize = median(reports.map(extractScriptSize));
  console.log(`${label}: ${reports.length} run(s) — total-byte-weight=${byteWeight} B, script:size=${scriptSize} B`);
  return { byteWeight, scriptSize };
}

function checkDelta(metric, base, head, maxPct) {
  if (!Number.isFinite(base) || !Number.isFinite(head)) {
    console.error(`${metric}: missing data (base=${base}, head=${head})`);
    return { ok: false, reason: 'missing data' };
  }
  if (base === 0) {
    // Zero baseline means no useful relative comparison; treat as pass and
    // rely on the absolute lhci assert step to catch any unexpected size.
    console.log(`${metric}: base is 0 B; skipping delta check (absolute thresholds still enforced).`);
    return { ok: true };
  }
  const deltaBytes = head - base;
  const deltaPct = (deltaBytes / base) * 100;
  const sign = deltaPct >= 0 ? '+' : '';
  const verdict = deltaPct > maxPct ? 'FAIL' : 'OK';
  console.log(`${metric}: ${base} B → ${head} B (${sign}${deltaBytes} B, ${sign}${deltaPct.toFixed(2)}%) [limit +${maxPct}%] ${verdict}`);
  return { ok: deltaPct <= maxPct, deltaPct };
}

const baseReports = loadReports(baseDir, { allowEmpty: true });
const headReports = loadReports(headDir);

if (baseReports.length === 0) {
  console.log(`No base lhci data in ${baseDir}.`);
  console.log('Delta gate SKIPPED. Falling through to absolute lhci assert.');
  console.log('(This is expected on the bootstrap PR that introduces the dual-collect workflow.');
  console.log(' Subsequent PRs against a base ref that already includes the workflow will gate normally.)');
  process.exit(0);
}

console.log(`Comparing ${headReports.length} head run(s) against ${baseReports.length} base run(s).`);
console.log(`Thresholds: total-byte-weight +${MAX_BYTE_WEIGHT_DELTA_PCT}%, script:size +${MAX_SCRIPT_SIZE_DELTA_PCT}%`);
console.log('');

const base = summarize(baseReports, 'base');
const head = summarize(headReports, 'head');
console.log('');

const byteWeightCheck = checkDelta('total-byte-weight', base.byteWeight, head.byteWeight, MAX_BYTE_WEIGHT_DELTA_PCT);
const scriptSizeCheck = checkDelta('script:size', base.scriptSize, head.scriptSize, MAX_SCRIPT_SIZE_DELTA_PCT);

console.log('');
const failed = [];
if (!byteWeightCheck.ok) failed.push('total-byte-weight');
if (!scriptSizeCheck.ok) failed.push('script:size');

if (failed.length > 0) {
  console.error(`Bundle delta gate FAILED for: ${failed.join(', ')}.`);
  console.error(`Recalibrate or split the change. To re-baseline absolute thresholds: ./tests/lighthouse/calibrate.sh`);
  process.exit(1);
}

console.log('Bundle delta gate PASSED.');
