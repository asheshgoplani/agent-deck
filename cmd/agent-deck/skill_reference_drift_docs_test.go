package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestSkillReferenceCodeFencesAreClosed fails when a skill reference opens a
// fenced code block it never closes. An unclosed ```toml block in
// config-reference.md once rendered seven config sections as code and
// broke their table-of-contents anchors.
func TestSkillReferenceCodeFencesAreClosed(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob(filepath.Join("..", "..", "skills", "agent-deck", "references", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, filepath.Join("..", "..", "skills", "agent-deck", "SKILL.md"))
	if len(files) < 10 {
		t.Fatalf("found only %d skill docs; the glob is wrong", len(files))
	}
	fenceRe := regexp.MustCompile("^\\s*(`{3,}|~{3,})")
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		open := ""
		openLine := 0
		for i, line := range strings.Split(string(data), "\n") {
			m := fenceRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			fence := m[1]
			switch {
			case open == "":
				open, openLine = fence, i+1
			case fence[0] == open[0] && len(fence) >= len(open) && strings.TrimSpace(line) == fence:
				open = ""
			case fence[0] == open[0] && len(fence) >= len(open):
				// A fence with an info string (```toml) never closes a
				// block, so meeting one while a block is open means the
				// previous block was left unclosed and this "opener" is
				// what closes it, shifting every later block by one.
				t.Errorf("%s:%d opens a %s code block while the one from line %d is still open; close that block first", path, i+1, strings.TrimSpace(line), openLine)
				open = ""
			}
		}
		if open != "" {
			t.Errorf("%s:%d opens a %s code fence that is never closed", path, openLine, open)
		}
	}
}

// TestCLIReferenceWebFlagsMatchCode pins the `agent-deck web` flag table in
// cli-reference.md to the flags parseWebCommandOptions defines, both ways.
// The table once listed a `--open` flag that does not exist.
func TestCLIReferenceWebFlagsMatchCode(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("web_cmd.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func parseWebCommandOptions(")
	if start < 0 {
		t.Fatal("parseWebCommandOptions not found in web_cmd.go")
	}
	body = body[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	defRe := regexp.MustCompile(`fs\.\w+\(&[\w.]+, "([a-z0-9-]+)"`)
	code := map[string]bool{}
	for _, m := range defRe.FindAllStringSubmatch(body, -1) {
		code["--"+m[1]] = true
	}
	if len(code) < 5 {
		t.Fatalf("found only %d web flags in web_cmd.go; the pattern is stale: %v", len(code), code)
	}

	doc, err := os.ReadFile(filepath.Join("..", "..", "skills", "agent-deck", "references", "cli-reference.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(doc)
	i := strings.Index(text, "agent-deck web [options]")
	if i < 0 {
		t.Fatal("cli-reference.md has no `agent-deck web [options]` section")
	}
	rowRe := regexp.MustCompile("^\\| `(--[a-z0-9-]+)` \\|")
	documented := map[string]bool{}
	inTable := false
	for _, line := range strings.Split(text[i:], "\n") {
		if m := rowRe.FindStringSubmatch(line); m != nil {
			documented[m[1]] = true
			inTable = true
			continue
		}
		if inTable && !strings.HasPrefix(line, "|") {
			break
		}
	}

	var missing, extra []string
	for f := range code {
		if !documented[f] {
			missing = append(missing, f)
		}
	}
	for f := range documented {
		if !code[f] {
			extra = append(extra, f)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("web flags defined in web_cmd.go but missing from the cli-reference.md table: %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("web flags in the cli-reference.md table that web_cmd.go does not define: %v", extra)
	}
}

// TestUserDocsRelativeLinksResolve fails when a user-facing doc links a
// repo-relative path that does not exist. llms-full.txt is assembled by hand
// from README.md and SKILL.md, and once kept SKILL.md's skill-relative links
// (references/cli-reference.md, ../session-share/SKILL.md), which point
// nowhere from the repo root.
func TestUserDocsRelativeLinksResolve(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	files := []string{"README.md", "AGENTS.md", "CLAUDE.md", "CONTRIBUTING.md", "llms.txt", "llms-full.txt"}
	for _, glob := range []string{"skills/agent-deck/*.md", "skills/agent-deck/references/*.md", "docs/*.md"} {
		m, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(glob)))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range m {
			rel, _ := filepath.Rel(root, p)
			files = append(files, filepath.ToSlash(rel))
		}
	}
	if len(files) < 30 {
		t.Fatalf("found only %d docs; a glob is wrong", len(files))
	}
	fenceBlock := regexp.MustCompile("(?s)```.*?```")
	linkRe := regexp.MustCompile(`\]\(([^)\s]+)\)`)
	schemeRe := regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)
	checked := 0
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		text := fenceBlock.ReplaceAllString(string(data), "")
		for _, m := range linkRe.FindAllStringSubmatch(text, -1) {
			target, _, _ := strings.Cut(m[1], "#")
			if target == "" || schemeRe.MatchString(target) || strings.HasPrefix(target, "<") {
				continue
			}
			var p string
			if strings.HasPrefix(target, "/") {
				p = filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(target, "/")))
			} else {
				p = filepath.Join(root, filepath.FromSlash(filepath.ToSlash(filepath.Dir(rel))), filepath.FromSlash(target))
			}
			checked++
			if _, err := os.Stat(p); err != nil {
				t.Errorf("%s links %q, which does not exist", rel, m[1])
			}
		}
	}
	if checked < 50 {
		t.Fatalf("checked only %d links; the link pattern is wrong", checked)
	}
}
