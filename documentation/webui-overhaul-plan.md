# Agent Deck WebUI Overhaul — Plan

Maintainer: @asheshgoplani
Conductor: agent-deck (df4696ae-1776358795)
Date opened: 2026-04-29

## North star

Make the agent-deck WebUI visually match the Claude.ai design exported in the bundle, AND state-level synced with the TUI such that any action in either view is reflected in the other within a tick. State sync is load-bearing, not optional.

## Phasing

### Phase 0 — Specification (this doc + parity matrix)

Before any code, produce two artifacts:

1. **Parity matrix** at `tests/web/PARITY_MATRIX.md`. Enumerate every TUI action and state surface today. For each row: name, where it lives in the TUI (file + key), corresponding web API endpoint (existing or proposed), the state field(s) it reads/writes, and the test (Playwright e2e + Go invariant) that exercises both sides. Gaps become PR-A test cases.

2. **Sync architecture decision** appended to this plan. Today the web likely uses SSE/polling against the same Go backend the TUI uses. Confirm the chosen mechanism, capture latency expectations (e.g. "web sees TUI action within 1 tick = ~250ms"), and name the failure mode the test suite must catch (e.g. "drift if either side adds a feature without parity").

### Phase 1 — Foundation (PR-A)

PR-A scope is **test infrastructure + parity scaffolding only**. NO design changes.

1. **Test infrastructure**
   - `web/package.json` (new)
   - Vitest + @testing-library/preact for unit tests
   - Playwright + @playwright/test for e2e with screenshot regression
   - `tests/web/screenshots/` baseline directory
   - `Makefile` target: `make test-web`
   - GitHub Actions workflow: `.github/workflows/web-tests.yml`

2. **Parity matrix tests**
   - Every parity-matrix row gets a Playwright e2e that exercises the action in the web AND verifies the corresponding state via CLI or direct DB query.
   - "Both views see the same truth" is the assertion contract.

3. **Runtime sync invariants**
   - `internal/web/parity_test.go` — Go test that fires actions via web API and via the session package directly, asserts both produce the same observable state.

4. **Verification before declaring PR-A done**
   - `make test-web` full suite green
   - `go test ./internal/web/... -race` green
   - Manual: run TUI + web side-by-side, fire 5 representative actions in each, screenshot both, confirm visual+state match. Document in PR description.

### Phase 2 — Redesign (PR-B)

PR-B applies the visual design from the bundle's `project/src/*.jsx` to the existing 27 Preact+htm components in `internal/web/static/app/`.

1. Treat `bundle/project/src/{app,fleet,shell,panes,mock}.jsx` and the chat transcript at `bundle/chats/chat1.md` as the design spec.
2. Port design changes into existing components — keep all functionality (command palette, dialogs, search, push, settings, toasts) intact.
3. Every visual change runs through the parity tests built in PR-A. If any test fails, fix the test or fix the design — never silently skip.
4. Iterate recursively: render, screenshot, diff against design HTML, refine, retest. Don't ship until both visual AND functional contracts hold.

### Phase 3 — Verification & docs

1. Manual TUI+web side-by-side for the full feature surface, not just 5 actions.
2. Update `documentation/SKILLS.md` if the new web exposes any skill operations differently.
3. Add a brief WebUI section to `skills/agent-deck/SKILL.md` so users who load the skill discover the redesigned interface.
4. Update `CHANGELOG.md` under `[Unreleased]` (or `[1.7.74]` if that's the slot).

## Stack decision

Keep **Preact + htm + signals** (already vendored at `internal/web/static/vendor/`).

Justification:
- Preserves all 27 existing components and their functionality
- Zero build step, Go-served as static assets
- ~10KB vendored runtime
- htm gives JSX-like ergonomics without a transpiler
- Matches the chat's stated direction ("polished prototype, single full-bleed app")
- React adds bundle weight + build step for no UX gain
- Lit forces shadow DOM and class authoring
- htmx can't express the command palette or Tweaks panel without lots of custom JS

## Sole-owner confirmation

The `.claude/worktrees/agent-*` dirs in the repo are leftover from earlier today's parallel agent runs (review-prs-batch, fix-bundle-v1771, etc.) — they are cleanup debt, not concurrent implementers. Nothing else is touching `internal/web/`. The webui-redesign-impl session is the sole implementer.

## What's NOT in scope

- Migrating Preact to React, Vue, Solid, or any other framework
- Removing the existing TUI or any of its features
- Changing the Go backend's API surface (only adding endpoints if the design demands one)
- Shipping PR-A and PR-B in the same release — PR-A lands first, gets a release if it's shippable on its own, then PR-B builds on it

## Bundle locations

- Extracted bundle: `/tmp/agent-deck-design/agent-deck/`
- README (handoff instructions): `/tmp/agent-deck-design/agent-deck/README.md`
- Chat transcript (intent): `/tmp/agent-deck-design/agent-deck/chats/chat1.md`
- Primary HTML mockup: `/tmp/agent-deck-design/agent-deck/project/Agent Deck Web.html`
- JSX design spec: `/tmp/agent-deck-design/agent-deck/project/src/`
- Bundle's vanilla-JS partial port (FYI only, do not blindly copy): `/tmp/agent-deck-design/agent-deck/project/internal/web/static/`

## Failure modes the plan must defend against

1. Silent feature regression — visual change ships but a TUI feature is no longer reachable from web. Caught by the parity matrix Playwright suite.
2. State drift — web and TUI disagree about session status, group membership, MCP attach state. Caught by the runtime parity invariant test.
3. Build-process creep — adding Vite/webpack/esbuild in PR-A would change the deployment story. Vitest is dev-only; Playwright is dev-only. Production stays vanilla Go-served static assets.
4. Hidden coupling — design assistant might have invented endpoints that don't exist. PR-A's parity matrix step calls these out before PR-B writes a single visual change.
