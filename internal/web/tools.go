//go:build tools

// Package web: tools.go pins build-time dependencies that are only used
// by `go generate` drivers (specifically internal/web/bundle.go which
// runs esbuild via the pkg/api library). The `tools` build tag is never
// set by the regular build so this file contributes nothing to the
// compiled binary — its sole purpose is to keep `go mod tidy` from
// dropping the esbuild dep, since bundle.go itself is excluded from the
// regular build via `//go:build ignore`.
//
// PERF-H: esbuild v0.28.0 was selected because it is the newest release
// that supports ES2022 + FormatESModule splitting + stable hashed output
// naming. Upgrade requires re-running `go generate ./internal/web/...`
// and re-measuring the byte budget gate in
// tests/e2e/visual/p8-perf-h-bundle.spec.ts.
package web

import (
	_ "github.com/evanw/esbuild/pkg/api"
)
