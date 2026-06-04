import { defineConfig } from 'vitest/config'
import { resolve } from 'node:path'
import { createRequire } from 'node:module'

const repoRoot = resolve(import.meta.dirname, '..', '..')

// Resolve npm packages from the tests/web/node_modules tree so the alias
// values are absolute paths. Bare specifiers used by component sources
// (which live outside tests/web/) wouldn't otherwise find this node_modules.
const req = createRequire(import.meta.url)

// aliasFor MUST return the package's ESM build (the `import` export condition),
// not its CJS build. `require.resolve()` follows the `require` condition and
// returns the CJS dist (e.g. preact/dist/preact.js). That is fine until a unit
// test RENDERS a component: @preact/signals patches preact's hook `options` at
// import time, and that patch only takes effect when @preact/signals and preact
// (and preact/hooks) share ONE module instance. Mixing the CJS preact (via the
// alias) with the ESM preact that @preact/signals/htm import internally yields
// two `options` objects, so the signals hook integration reads hook state off
// the wrong component and throws `Cannot read properties of undefined (__$f)`.
// Resolving every alias to the ESM build keeps a single shared instance.
//
// Implementation: resolve the spec's CJS dist (the `require` condition, the
// historic behavior) then swap `.js` -> `.mjs` to reach the sibling ESM build.
// This works because every preact-ecosystem package here ships `<name>.js`
// (CJS) and `<name>.mjs` (ESM) side by side in the same dir; we fall back to
// the CJS dist if no `.mjs` exists.
const aliasFor = (spec) => {
  const cjs = req.resolve(spec)
  const mjs = cjs.replace(/\.js$/, '.mjs')
  // Only prefer the .mjs when it actually exists (defensive against a package
  // that ships only CJS); fall back to the CJS dist otherwise.
  try {
    req.resolve(mjs)
    return mjs
  } catch (_) {
    return cjs
  }
}

export default defineConfig({
  // Vite root stays at tests/web/ so node_modules resolution works for
  // bare specifiers (preact, htm/preact, @preact/signals). The component
  // sources live one directory up; fs.allow is widened to the repo root.
  root: import.meta.dirname,
  server: {
    fs: {
      allow: [repoRoot],
    },
  },
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: ['./helpers/setup.js'],
    include: ['unit/**/*.test.js'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'html', 'lcov'],
      reportsDirectory: './coverage',
      include: [resolve(repoRoot, 'internal/web/static/app/**/*.js')],
      exclude: [
        resolve(repoRoot, 'internal/web/static/app/main.js'),
      ],
    },
  },
  resolve: {
    // Bare specifiers used by component sources need to resolve to the
    // tests/web/node_modules tree because the components live outside
    // tests/web/ and Vite's bare-specifier resolver walks UP from the
    // file's location — finding nothing at repoRoot/node_modules.
    //
    // Aliasing each spec to its `require.resolve()` result lets Vite jump
    // straight to the installed file. Sub-imports inside those files
    // (e.g. signals.module.js → preact/hooks) re-resolve via the same
    // alias map, so transitive resolution works without breakage.
    //
    // ORDER MATTERS: Vite alias uses prefix matching with first-match-wins.
    // The specific `preact/hooks` and `preact/jsx-runtime` keys MUST precede
    // the bare `preact` entry, otherwise `preact/hooks` matches `preact` first
    // and gets rewritten to <preact-file>/hooks (unresolvable). This only
    // surfaces when a unit test imports a component module that imports
    // `preact/hooks` directly (e.g. EditSessionDialog.js); prior tests only
    // imported hook-free modules (api.js/state.js/dataModel.js) so the bug
    // stayed latent.
    alias: {
      'preact/hooks': aliasFor('preact/hooks'),
      'preact/jsx-runtime': aliasFor('preact/jsx-runtime'),
      'preact': aliasFor('preact'),
      'htm/preact': aliasFor('htm/preact'),
      '@preact/signals': aliasFor('@preact/signals'),
      '@preact/signals-core': aliasFor('@preact/signals-core'),
    },
  },
})
