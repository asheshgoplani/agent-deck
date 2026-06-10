//go:build race

package testutil

// RaceEnabled reports whether the binary was built with -race. Used to skip
// resource-heavy guard tests (which spawn nested `go test`) under the full
// `go test -race ./...` suite, where their contention destabilizes tmux.
const RaceEnabled = true
