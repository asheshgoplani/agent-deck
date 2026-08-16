package main

// In-memory MCPManager for the Playwright web fixture. Seeded with a
// deterministic catalog and starts with no MCPs attached.
//
// Pairs with internal/web's MCPManager interface; tests exercise
// attach/detach/list/toggle without touching real ~/.claude.json or
// .mcp.json files on disk.

import (
	"sort"
	"sync"

	"github.com/asheshgoplani/agent-deck/internal/web"
)

type fixtureMCPManager struct {
	mu       sync.Mutex
	catalog  []web.MCPCatalogEntry
	attached map[string]map[string][]string // projectPath -> scope -> []name
}

func newFixtureMCPManager() *fixtureMCPManager {
	return &fixtureMCPManager{
		catalog: []web.MCPCatalogEntry{
			{Name: "exa", Description: "AI-powered web search", Transport: "stdio", Command: "npx"},
			{Name: "youtube", Description: "YouTube search + transcripts", Transport: "stdio", Command: "npx"},
			{Name: "playwright", Description: "Browser automation", Transport: "stdio", Command: "npx"},
		},
		attached: make(map[string]map[string][]string),
	}
}

func (f *fixtureMCPManager) ListCatalog() []web.MCPCatalogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]web.MCPCatalogEntry(nil), f.catalog...)
}

func (f *fixtureMCPManager) ListAttached(target web.MCPTarget) (map[string][]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string][]string, 3)
	for _, scope := range []string{"local", "project", "global", "user"} {
		names := f.attached[target.ProjectPath][scope]
		cp := append([]string(nil), names...)
		sort.Strings(cp)
		if cp == nil {
			cp = []string{}
		}
		out[scope] = cp
	}
	return out, nil
}

func (f *fixtureMCPManager) Attach(target web.MCPTarget, name, scope string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attached[target.ProjectPath] == nil {
		f.attached[target.ProjectPath] = make(map[string][]string)
	}
	for _, n := range f.attached[target.ProjectPath][scope] {
		if n == name {
			return nil
		}
	}
	f.attached[target.ProjectPath][scope] = append(f.attached[target.ProjectPath][scope], name)
	return nil
}

func (f *fixtureMCPManager) Detach(target web.MCPTarget, name, scope string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attached[target.ProjectPath] == nil {
		return nil
	}
	src := f.attached[target.ProjectPath][scope]
	out := src[:0]
	for _, n := range src {
		if n != name {
			out = append(out, n)
		}
	}
	f.attached[target.ProjectPath][scope] = out
	return nil
}

func (f *fixtureMCPManager) Move(target web.MCPTarget, name, fromScope, toScope string) error {
	if err := f.Detach(target, name, fromScope); err != nil {
		return err
	}
	return f.Attach(target, name, toScope)
}

// Reset clears all attached MCPs (called by /__fixture/reset).
func (f *fixtureMCPManager) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attached = make(map[string]map[string][]string)
}
