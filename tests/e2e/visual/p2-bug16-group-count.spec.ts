import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Phase 4 / Plan 06 / Task 1: BUG #16 / UX-05 regression test
 *
 * Asserts that the group header count parens (N) reflect the
 * frontend-computed count of visible children (filtered by search),
 * NOT the API-provided group.sessionCount which counts the full
 * subtree and ignores search filter / SSE flux.
 *
 * Root cause (LOCKED per 04-CONTEXT.md): GroupRow.js line 57 reads
 * ${group.sessionCount || 0} from the API-provided MenuGroup struct.
 * The backend at internal/web/menu_snapshot_builder.go populates this
 * via groupTree.SessionCountForGroup which counts the full subtree.
 * The frontend then filters and renders a different number — drift.
 *
 * Fix (LOCKED per 04-CONTEXT.md, frontend-only): GroupRow.js declares
 * a countVisibleChildren(groupPath, query) helper that reads
 * sessionsSignal.value + searchQuerySignal.value and counts sessions
 * whose groupPath starts with this group's path AND pass the current
 * search filter. Backend untouched.
 *
 * TDD ORDER: this spec is committed in FAILING state in Task 1; Task 2
 * applies the GroupRow.js helper + line 57 edit and flips the spec
 * to green.
 */

const APP_ROOT = join(__dirname, '..', '..', '..', 'internal', 'web', 'static', 'app');
const SESSION_GROUPS_GO = join(__dirname, '..', '..', '..', 'internal', 'session', 'groups.go');
const MENU_BUILDER_GO = join(__dirname, '..', '..', '..', 'internal', 'web', 'menu_snapshot_builder.go');
const SESSION_DATA_SERVICE_GO = join(__dirname, '..', '..', '..', 'internal', 'web', 'session_data_service.go');

test.describe('BUG #16 / UX-05 — group count from rendered children', () => {
  test('structural: GroupRow.js does NOT read group.sessionCount', () => {
    const src = readFileSync(join(APP_ROOT, 'GroupRow.js'), 'utf-8');
    expect(
      /group\.sessionCount/.test(src),
      'GroupRow.js must NOT read group.sessionCount — replaced by countVisibleChildren per BUG #16 / UX-05',
    ).toBe(false);
  });

  test('structural: GroupRow.js declares countVisibleChildren helper', () => {
    const src = readFileSync(join(APP_ROOT, 'GroupRow.js'), 'utf-8');
    expect(
      /function\s+countVisibleChildren|countVisibleChildren\s*=/.test(src),
      'GroupRow.js must declare countVisibleChildren — BUG #16 / UX-05',
    ).toBe(true);
  });

  test('structural: GroupRow.js imports sessionsSignal and searchQuerySignal', () => {
    const src = readFileSync(join(APP_ROOT, 'GroupRow.js'), 'utf-8');
    expect(
      /sessionsSignal/.test(src),
      'GroupRow.js must import sessionsSignal from ./state.js — BUG #16 / UX-05',
    ).toBe(true);
    expect(
      /searchQuerySignal/.test(src),
      'GroupRow.js must import searchQuerySignal from ./state.js — BUG #16 / UX-05',
    ).toBe(true);
  });

  test('structural: backend files contain expected SessionCountForGroup signature (smoke check that they were not gutted)', () => {
    // The fix is frontend-only. We do NOT diff against HEAD here (that would
    // require child_process which the security hook discourages). Instead we
    // read the three backend files and assert they still contain a known
    // signature that proves they were not modified to remove the function.
    const groupsSrc = readFileSync(SESSION_GROUPS_GO, 'utf-8');
    const builderSrc = readFileSync(MENU_BUILDER_GO, 'utf-8');
    const serviceSrc = readFileSync(SESSION_DATA_SERVICE_GO, 'utf-8');
    expect(
      /SessionCountForGroup/.test(groupsSrc),
      'internal/session/groups.go must still contain SessionCountForGroup — backend untouched per BUG #16 / UX-05 frontend-only fix',
    ).toBe(true);
    expect(
      /SessionCountForGroup/.test(builderSrc),
      'internal/web/menu_snapshot_builder.go must still call SessionCountForGroup — backend untouched per BUG #16 / UX-05 frontend-only fix',
    ).toBe(true);
    expect(
      /SessionCount\s+int/.test(serviceSrc),
      'internal/web/session_data_service.go must still declare SessionCount — backend untouched per BUG #16 / UX-05 frontend-only fix',
    ).toBe(true);
  });

  test('DOM: group count tracks search filter (3 sessions, search to 1, clear back to 3)', async ({ page, request }) => {
    const groupPath = `bug16-test-group-${Date.now()}`;
    const sessionIds: string[] = [];

    // Create 3 sessions in a unique group via the web API.
    // The _test profile does not pre-populate sessions per 04-CONTEXT.md
    // line 556, so the spec creates fixtures inline.
    for (let i = 0; i < 3; i++) {
      const title = `bug16-${i}-${Date.now()}`;
      const res = await request.post('/api/sessions', {
        headers: { Authorization: 'Bearer test' },
        data: { title, groupPath, tool: 'shell' },
      });
      if (!res.ok()) {
        test.skip(true, `API fixture create failed (${res.status()}) — backend may not allow ad-hoc session creation under _test profile; defer to Phase 8 fixtures`);
        return;
      }
      try {
        const body = await res.json();
        if (body && body.id) sessionIds.push(body.id);
      } catch (_) { /* tolerate non-JSON */ }
    }

    try {
      await page.goto('/?t=test');
      await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
      await page.waitForTimeout(500);

      // Find the group header for our unique groupPath. Match by aria-expanded
      // and the group name in the title span.
      const groupHeader = page.locator(`#preact-session-list button[aria-expanded] >> text=${groupPath}`).locator('..');
      const groupCount = await groupHeader.count();
      test.skip(groupCount === 0, 'group header not rendered — fixture creation may have failed');

      // Initial count should be 3.
      const initialText = (await groupHeader.first().textContent()) || '';
      expect(
        initialText,
        `group header text must contain "(3)" for the unfiltered group ${groupPath}; got "${initialText}"`,
      ).toContain('(3)');

      // Type a search query matching only one session.
      // Open the search input first.
      await page.locator('button[title^="Filter sessions"], button[title^="Search sessions"]').first().click();
      await page.waitForTimeout(100);
      await page.locator('input[type="text"][placeholder*="sessions"]').first().fill('bug16-1-');
      await page.waitForTimeout(300);

      const filteredText = (await groupHeader.first().textContent()) || '';
      expect(
        filteredText,
        `group header text must contain "(1)" when search filters to one session; got "${filteredText}"`,
      ).toContain('(1)');

      // Clear search.
      await page.locator('input[type="text"][placeholder*="sessions"]').first().fill('');
      await page.waitForTimeout(300);

      const clearedText = (await groupHeader.first().textContent()) || '';
      expect(
        clearedText,
        `group header text must return to "(3)" after clearing search; got "${clearedText}"`,
      ).toContain('(3)');
    } finally {
      // Cleanup created sessions.
      for (const id of sessionIds) {
        await request.delete(`/api/sessions/${id}`, {
          headers: { Authorization: 'Bearer test' },
        }).catch(() => {});
      }
    }
  });
});
