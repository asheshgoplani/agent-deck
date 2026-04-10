// Phase 8 plan 05 — PERF-H esbuild bundling regression spec.
//
// Structural gates (readFileSync-based, no server required):
//   1. assets.go exists and defines a ResolveAsset method on *Assets
//   2. bundle.go exists and imports github.com/evanw/esbuild/pkg/api
//   3. index.html contains at least one {{ASSET:...}} placeholder token
//   4. static_files.go (or server.go) invokes ResolveAsset or
//      SubstitutePlaceholders in the index serving path
//   5. manifest.json exists at internal/web/static/dist/manifest.json
//      after running `go generate ./internal/web/...`
//
// Runtime gate (skips cleanly if no server at :18420):
//   6. Byte budget: gzipped first-party payload < 150 KB
//
// Rollback gate:
//   7. Covered by Go unit test TestAssets_RollbackEnvVar in
//      internal/web/assets_test.go (skip at runtime — informational).

import { test, expect } from '@playwright/test';
import { readFileSync, existsSync } from 'fs';
import { join } from 'path';
import { gzipSync } from 'zlib';

const ROOT = join(__dirname, '..', '..', '..');
const ASSETS_GO = join(ROOT, 'internal', 'web', 'assets.go');
const BUNDLE_GO = join(ROOT, 'internal', 'web', 'bundle.go');
const INDEX_HTML = join(ROOT, 'internal', 'web', 'static', 'index.html');
const STATIC_FILES_GO = join(ROOT, 'internal', 'web', 'static_files.go');
const SERVER_GO = join(ROOT, 'internal', 'web', 'server.go');
const MANIFEST = join(ROOT, 'internal', 'web', 'static', 'dist', 'manifest.json');

function collectCaptures(src: string, pattern: RegExp): string[] {
  const out: string[] = [];
  for (const m of src.matchAll(pattern)) {
    if (m[1]) out.push(m[1]);
  }
  return out;
}

test.describe('PERF-H — esbuild bundling', () => {
  test('structural: assets.go exists and exports ResolveAsset', () => {
    expect(existsSync(ASSETS_GO), 'internal/web/assets.go must exist').toBe(true);
    const src = readFileSync(ASSETS_GO, 'utf-8');
    expect(
      /func\s*\(\w+\s*\*?Assets\)\s*ResolveAsset/.test(src),
      'assets.go must define a ResolveAsset method on Assets',
    ).toBe(true);
  });

  test('structural: bundle.go imports esbuild/pkg/api', () => {
    expect(existsSync(BUNDLE_GO), 'internal/web/bundle.go must exist').toBe(true);
    const src = readFileSync(BUNDLE_GO, 'utf-8');
    expect(
      /github\.com\/evanw\/esbuild\/pkg\/api/.test(src),
      'bundle.go must import github.com/evanw/esbuild/pkg/api',
    ).toBe(true);
    expect(
      /api\.Build\s*\(/.test(src),
      'bundle.go must call api.Build()',
    ).toBe(true);
  });

  test('structural: index.html contains at least one {{ASSET:...}} placeholder', () => {
    const src = readFileSync(INDEX_HTML, 'utf-8');
    expect(
      /\{\{ASSET:[^}]+\}\}/.test(src),
      'index.html must contain {{ASSET:logical}} placeholder tokens. Do NOT inline hashed paths.',
    ).toBe(true);
  });

  test('structural: static_files.go or server.go invokes ResolveAsset / SubstitutePlaceholders in the index path', () => {
    // The helper may live in either static_files.go (handleIndex) or
    // server.go (if refactored). Accept a match in either file.
    const staticSrc = existsSync(STATIC_FILES_GO) ? readFileSync(STATIC_FILES_GO, 'utf-8') : '';
    const serverSrc = existsSync(SERVER_GO) ? readFileSync(SERVER_GO, 'utf-8') : '';
    const hasCall = /ResolveAsset|SubstitutePlaceholders/.test(staticSrc) ||
      /ResolveAsset|SubstitutePlaceholders/.test(serverSrc);
    expect(
      hasCall,
      'static_files.go or server.go must call ResolveAsset or SubstitutePlaceholders when serving index.html',
    ).toBe(true);
  });

  test('filesystem: go generate produces manifest.json in internal/web/static/dist', () => {
    expect(
      existsSync(MANIFEST),
      'internal/web/static/dist/manifest.json must exist (run `go generate ./internal/web/...` before running this spec)',
    ).toBe(true);
  });

  test('byte budget: gzipped first-party payload < 150 KB', async ({ page }) => {
    try {
      await page.goto('/?t=perf-h-budget', { timeout: 3000 });
    } catch (_e) {
      test.skip(true, 'test server not running on :18420 — byte budget gate requires a live server');
      return;
    }
    const html = await page.content();
    const scriptRe = /<script[^>]*src="([^"]+)"[^>]*>/g;
    const linkRe = /<link[^>]*rel="stylesheet"[^>]*href="([^"]+)"[^>]*>/g;
    const deferRe = /<script[^>]*defer[^>]*src="([^"]+)"/g;

    const deferredSrcs = new Set<string>(collectCaptures(html, deferRe));

    const firstParty = [
      ...collectCaptures(html, scriptRe),
      ...collectCaptures(html, linkRe),
    ]
      .filter(src => src.startsWith('/static/') || src.startsWith('/'))
      .filter(src => !deferredSrcs.has(src))
      .filter(src => !/preact|xterm|signals|htm|chart/.test(src))
      .filter((src, idx, arr) => arr.indexOf(src) === idx);

    let totalGzipped = 0;
    for (const src of firstParty) {
      const resp = await page.request.get(src);
      if (!resp.ok()) continue;
      const body = await resp.body();
      const gzipped = gzipSync(body);
      totalGzipped += gzipped.length;
    }

    const budget = 150 * 1024;
    expect(
      totalGzipped,
      `First-party gzipped payload: ${totalGzipped} bytes (${(totalGzipped / 1024).toFixed(1)} KB). Budget: 150 KB. Files counted: ${firstParty.join(', ')}`,
    ).toBeLessThan(budget);
  });

  test('rollback: AGENTDECK_WEB_BUNDLE=0 forces dev mode (covered by Go unit test TestAssets_RollbackEnvVar)', async () => {
    test.skip(true, 'rollback env var is covered by Go unit test TestAssets_RollbackEnvVar');
  });
});
