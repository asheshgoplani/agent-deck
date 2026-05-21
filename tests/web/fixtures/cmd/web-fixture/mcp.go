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

func (f *fixtureMCPManager) ListAttached(projectPath string) (map[string][]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string][]string, 3)
	for _, scope := range []string{"local", "global", "user"} {
		names := f.attached[projectPath][scope]
		cp := append([]string(nil), names...)
		sort.Strings(cp)
		if cp == nil {
			cp = []string{}
		}
		out[scope] = cp
	}
	return out, nil
}

func (f *fixtureMCPManager) Attach(projectPath, name, scope string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attached[projectPath] == nil {
		f.attached[projectPath] = make(map[string][]string)
	}
	for _, n := range f.attached[projectPath][scope] {
		if n == name {
			return nil
		}
	}
	f.attached[projectPath][scope] = append(f.attached[projectPath][scope], name)
	return nil
}

func (f *fixtureMCPManager) Detach(projectPath, name, scope string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attached[projectPath] == nil {
		return nil
	}
	src := f.attached[projectPath][scope]
	out := src[:0]
	for _, n := range src {
		if n != name {
			out = append(out, n)
		}
	}
	f.attached[projectPath][scope] = out
	return nil
}

func (f *fixtureMCPManager) Move(projectPath, name, fromScope, toScope string) error {
	if err := f.Detach(projectPath, name, fromScope); err != nil {
		return err
	}
	return f.Attach(projectPath, name, toScope)
}

// Reset clears all attached MCPs (called by /__fixture/reset).
func (f *fixtureMCPManager) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attached = make(map[string]map[string][]string)
}
