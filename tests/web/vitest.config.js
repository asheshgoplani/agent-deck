import { defineConfig } from 'vitest/config'
import { resolve } from 'node:path'
import { createRequire } from 'node:module'

const repoRoot = resolve(import.meta.dirname, '..', '..')

// Resolve npm packages from the tests/web/node_modules tree so the alias
// values are absolute paths. Bare specifiers used by component sources
// (which live outside tests/web/) wouldn't otherwise find this node_modules.
const req = createRequire(import.meta.url)
const aliasFor = (spec) => req.resolve(spec)

export default defineConfig({
  // Vite root is the repo root. Unit tests load component sources with
  // relative dynamic imports ('../../../internal/web/...'). Vitest 4 resolves
  // those against the root-relative URL of the test file, so a tests/web root
  // clamps them to '/internal/...' and they fail to load. Bare specifiers
  // (preact, htm/preact, @preact/signals) still resolve from tests/web via the
  // alias map below; cacheDir stays under tests/web/node_modules.
  root: repoRoot,
  cacheDir: resolve(import.meta.dirname, 'node_modules', '.vite'),
  server: {
    fs: {
      allow: [repoRoot],
    },
  },
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: [resolve(import.meta.dirname, 'helpers', 'setup.js')],
    include: ['tests/web/unit/**/*.test.js'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'html', 'lcov'],
      reportsDirectory: resolve(import.meta.dirname, 'coverage'),
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
    // ORDER MATTERS. Vite keeps object aliases in insertion order and matches a
    // string `find` by prefix (`importee === find || importee.startsWith(find +
    // '/')`), so a bare 'preact' listed first also swallows 'preact/hooks' and
    // rewrites it to <abs>/preact.mjs/hooks — a path that does not exist. Every
    // subpath entry must precede its bare parent. This stayed latent until the
    // first test imported a module that pulls in preact/hooks.
    alias: {
      'preact/hooks': aliasFor('preact/hooks'),
      'preact/jsx-runtime': aliasFor('preact/jsx-runtime'),
      'preact': aliasFor('preact'),
      'htm/preact': aliasFor('htm/preact'),
      '@preact/signals': aliasFor('@preact/signals'),
      '@preact/signals-core': aliasFor('@preact/signals-core'),
      // xterm ships via the index.html import map, not npm; see helpers/xtermStub.js.
      '@xterm/xterm': resolve(import.meta.dirname, 'helpers', 'xtermStub.js'),
      '@xterm/addon-fit': resolve(import.meta.dirname, 'helpers', 'xtermStub.js'),
      '@xterm/addon-webgl': resolve(import.meta.dirname, 'helpers', 'xtermStub.js'),
    },
  },
})
