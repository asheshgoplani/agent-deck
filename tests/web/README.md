# Agent Deck — Web UI tests

This directory hosts the JS test suite for the agent-deck web UI:

- **Vitest** unit tests in `unit/` (jsdom + `@testing-library/preact`)
- **Playwright** e2e + screenshot regression in `e2e/`
- **PARITY_MATRIX.md** — the contract between the TUI and the web UI

The Go-side runtime sync invariant lives at `internal/web/parity_test.go`.

## Quick start

```bash
# One-time install (both Vitest deps and the Playwright chromium browser)
make test-web-install

# Fast inner loop
make test-web-unit         # vitest, ~1s
make test-web-e2e          # playwright + screenshots, ~25s

# Everything
make test-web
```

## Architecture

```
tests/web/
├── package.json           # vitest, playwright, @testing-library/preact, jsdom
├── vitest.config.js       # bare-specifier resolution via createRequire alias map
├── playwright.config.js   # 3 projects: chromium-{desktop,tablet,phone}
├── helpers/
│   ├── setup.js           # jsdom polyfills (fetch, EventSource, ResizeObserver)
│   ├── global-setup.js    # builds + spawns the Go fixture binary
│   └── global-teardown.js # SIGTERM the fixture process
├── fixtures/cmd/web-fixture/main.go
│                          # in-memory MenuDataLoader + SessionMutator binary
├── unit/                  # vitest unit tests
│   ├── api.test.js        # apiFetch contract
│   └── state.test.js      # signals + clampSidebarWidth
├── e2e/                   # playwright tests
│   ├── smoke.spec.js              # boot, asset serving, shell render
│   ├── parity-actions.spec.js     # one test per parity-matrix action row
│   ├── parity-state.spec.js       # one test per parity-matrix state field
│   └── visual-baselines.spec.js   # screenshot regression — pre-redesign baseline
├── screenshots/           # the visual contract (committed)
└── PARITY_MATRIX.md       # canonical TUI ↔ web action+state map
```

### Why a fixture binary instead of the real `agent-deck web`?

Real sessions need tmux, real session storage, hook files on disk, and a
profile dir — none of which CI workers have or should have. The fixture
binary at `fixtures/cmd/web-fixture/main.go` boots only `internal/web.NewServer`
with an in-memory `MenuDataLoader` + `SessionMutator`, plus a `/__fixture/*`
admin surface that lets tests reset state and force TUI-style transitions
without running tmux. Same Go code path as production for the routes under
test; deterministic state for the screenshots.

### Updating screenshots

Only do this when the visual change is intentional (e.g. PR-B lands the
redesign):

```bash
cd tests/web
npm run test:e2e:update-snapshots
```

Then review every diff in `git diff -- tests/web/screenshots/` before
committing. Unintentional diffs are regressions, not updates.

### Reading the parity matrix

`PARITY_MATRIX.md` enumerates every TUI action and every state field, with
its web counterpart. Rows tagged MISSING are gaps — the web doesn't yet
expose them. The `parity-actions.spec.js` "MISSING actions stay MISSING"
block pins those gaps so a silent addition (without matrix update) fails
the build.

When PR-B (or any later PR) closes a gap:

1. Add the new endpoint in `internal/web/handlers_*.go`.
2. Update `PARITY_MATRIX.md`: change the row from MISSING to the new method+path.
3. Move the test from the "MISSING" block in `parity-actions.spec.js` into the lifecycle block with a positive assertion.
4. Add a state-field test in `parity-state.spec.js` if the change exposes new JSON fields.

The Go invariant test in `internal/web/parity_test.go` covers the inverse:
"the same operation through the HTTP layer and through the SessionMutator
interface produces the same observable state." Add a new case there for
each new endpoint.
