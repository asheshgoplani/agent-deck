// render-visual-details.js — Renders the "Visual Regression Results" section
// of the weekly regression issue from a Playwright JSON report.
//
// Playwright's JSON reporter nests describe() blocks as suites inside suites
// (file suite -> describe suite -> specs), so specs must be collected
// recursively; a top-level-only walk sees `specs: []` and reports nothing
// (issue #1674: "No detailed results available" despite a real failure).
//
// Returns { details, specCount, failedCount, noData }:
//   - noData: true when the report is missing, unparseable, or contains zero
//     specs. Callers must treat that as "NO DATA (skipped)", not as a failure.
'use strict';

function collectSpecs(suite, out) {
  for (const spec of suite.specs || []) out.push(spec);
  for (const child of suite.suites || []) collectSpecs(child, out);
  return out;
}

function stripAnsi(s) {
  return s.replace(/\x1b\[[0-9;]*m/g, '');
}

function firstError(result) {
  if (result.error) return result.error;
  if (Array.isArray(result.errors) && result.errors.length > 0) return result.errors[0];
  return null;
}

function renderVisualDetails(raw) {
  if (raw == null || raw.trim() === '') {
    return { details: 'No visual test results found (empty report).', specCount: 0, failedCount: 0, noData: true };
  }

  let data;
  try {
    data = JSON.parse(raw);
  } catch (e) {
    return { details: `Could not parse visual results: ${e.message}`, specCount: 0, failedCount: 0, noData: true };
  }

  const specs = [];
  for (const suite of data.suites || []) collectSpecs(suite, specs);

  if (specs.length === 0) {
    return { details: 'No visual test results found in the Playwright report.', specCount: 0, failedCount: 0, noData: true };
  }

  const lines = [];
  let failedCount = 0;
  for (const spec of specs) {
    lines.push(`- ${spec.ok ? ':white_check_mark:' : ':x:'} ${spec.title}`);
    if (!spec.ok) {
      failedCount++;
      for (const test of spec.tests || []) {
        for (const result of test.results || []) {
          const err = firstError(result);
          if (err && err.message) {
            lines.push(`  - Error: ${stripAnsi(err.message).split('\n')[0]}`);
          }
        }
      }
    }
  }

  return { details: lines.join('\n'), specCount: specs.length, failedCount, noData: false };
}

module.exports = { renderVisualDetails };
