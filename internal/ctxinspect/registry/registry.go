// Package registry assembles the context-inspection adapters into the registry
// the CLI and the TUI use.
//
// It exists so [ctxinspect] itself stays free of its adapters: an adapter
// imports the engine, so the engine cannot import the adapters back. This
// package is the one place that knows the full set, which is also the one place
// a new harness has to be added.
package registry

import (
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/claude"
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/codex"
)

// Default returns the adapter registry.
//
// Order is match order, so specific adapters come first. The generic adapter is
// passed as the fallback rather than appended to the list: it claims every tool,
// so registering it in the ordered set would shadow everything behind it. As a
// fallback it turns an unsupported harness into a populated inventory screen
// with no invented numbers, instead of an error.
func Default() *ctxinspect.Registry {
	return ctxinspect.NewRegistry(
		ctxinspect.NewGenericAdapter(),
		claude.New(),
		codex.New(),
	)
}
