package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openPhoneResult mirrors the `open --json` object the voice desk and the
// phone gateway read.
type openPhoneResult struct {
	Status    string `json:"status"`
	Where     string `json:"where"`
	Session   string `json:"session"`
	Title     string `json:"title"`
	Path      string `json:"path"`
	URL       string `json:"url"`
	PageID    string `json:"page_id"`
	PageTitle string `json:"page_title"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Phone     string `json:"phone"`
	ElapsedMS *int64 `json:"elapsed_ms"`
	Error     string `json:"error"`
	Code      string `json:"code"`
}

func TestOpenReachesThePhonePageList(t *testing.T) {
	parallelSubprocessTest(t)
	home := t.TempDir()
	project := t.TempDir()
	outside := t.TempDir()
	stdout, stderr, code := runAgentDeck(t, home, "add", project, "--title", "Idea board", "-c", "bash", "--no-parent", "--json")
	if code != 0 {
		t.Fatalf("add: %d %s %s", code, stdout, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &added); err != nil || added.ID == "" {
		t.Fatalf("add result: %s %v", stdout, err)
	}
	other, _, _ := runAgentDeck(t, home, "add", t.TempDir(), "--title", "Empty one", "-c", "bash", "--no-parent", "--json")
	var empty struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(other), &empty)

	write := func(p, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	page := filepath.Join(project, "board", "index.html")
	write(page, `<html><head><title>Idea board</title><link rel="stylesheet" href="s.css"></head><body><img src="a.png"><p>v1</p></body></html>`)
	write(filepath.Join(project, "board", "s.css"), "p{color:teal}")
	write(filepath.Join(project, "board", "a.png"), "\x89PNG\r\n\x1a\nA")
	notes := filepath.Join(project, "notes.md")
	write(notes, "# Notes\n\n| k | v |\n|---|---|\n| a | b |\n")
	secret := filepath.Join(outside, "secret.html")
	write(secret, "<p>private</p>")

	env := []string{"AGENTDECK_INSTANCE_ID=" + added.ID, "SSH_CONNECTION=test"}
	run := func(args ...string) (openPhoneResult, string, int) {
		t.Helper()
		out, errOut, code := runAgentDeckEnv(t, home, "", env, args...)
		var r openPhoneResult
		if strings.Contains(strings.Join(args, " "), "--json") {
			if err := json.Unmarshal([]byte(out), &r); err != nil {
				t.Fatalf("%v: not one JSON object: %q %q", args, out, errOut)
			}
		}
		return r, out + errOut, code
	}

	// 1. HTML page in the project: queued for the Mac app, on the phone list.
	r, out, code := run("open", page, "--json", "--wait", "0")
	if code != 0 || r.Status != "queued" || r.Where != "phone" || r.Phone != "shown" || r.Kind != "html" ||
		r.Session != added.ID || r.Title != "Idea board" || r.PageTitle != "Idea board" || r.Name != "index.html" || r.Path != page || len(r.PageID) != 16 || r.ElapsedMS == nil {
		t.Fatalf("open html: %d %+v %s", code, r, out)
	}
	htmlID := r.PageID
	// 2. Markdown page.
	r, out, code = run("open", notes, "--json", "--wait", "0")
	if code != 0 || r.Kind != "markdown" || r.Where != "phone" || r.PageTitle != "Notes" || r.Name != "notes.md" {
		t.Fatalf("open md: %d %+v %s", code, r, out)
	}
	mdID := r.PageID
	// 3. A file outside the session's project path and roots: Mac only.
	r, out, code = run("open", secret, "--json", "--wait", "0")
	if code != 0 || r.Where != "mac" || r.Phone != "outside_roots" || r.PageID != "" {
		t.Fatalf("open outside: %d %+v %s", code, r, out)
	}
	// 4. URL: on the phone list as a link.
	r, out, code = run("open", "https://example.com/plan", "--json", "--wait", "0")
	if code != 0 || r.Kind != "url" || r.Phone != "url" || r.URL != "https://example.com/plan" {
		t.Fatalf("open url: %d %+v %s", code, r, out)
	}

	// The list: newest first, no outside file.
	listOut, listErr, code := runAgentDeckEnv(t, home, "", env, "open", "--list", "--json")
	var list struct {
		Session string `json:"session"`
		Title   string `json:"title"`
		Pages   []struct {
			ID, Kind, Title, Name, Path, URL string
		} `json:"pages"`
	}
	if err := json.Unmarshal([]byte(listOut), &list); err != nil || code != 0 {
		t.Fatalf("list: %d %q %q %v", code, listOut, listErr, err)
	}
	if len(list.Pages) != 3 || list.Pages[0].Kind != "url" || list.Pages[1].ID != mdID || list.Pages[2].ID != htmlID || list.Title != "Idea board" {
		t.Fatalf("list order/content: %+v", list)
	}
	if strings.Contains(listOut, "secret") {
		t.Fatalf("outside file listed: %s", listOut)
	}

	// The page content: self-contained, assets inlined, follow-up edits visible.
	show := func(id string) (string, map[string]any) {
		t.Helper()
		out, errOut, code := runAgentDeckEnv(t, home, "", env, "open", "--show", id, "--json")
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil || code != 0 {
			t.Fatalf("show %s: %d %q %q", id, code, out, errOut)
		}
		return m["html"].(string), m
	}
	doc, meta := show(htmlID)
	if !strings.Contains(doc, "<p>v1</p>") || !strings.Contains(doc, "data:image/png;base64,") || !strings.Contains(doc, "p{color:teal}") || meta["title"] != "Idea board" {
		t.Fatalf("show html: %v", meta)
	}
	write(page, `<html><head><title>Idea board</title></head><body><p>v2 after the edit</p></body></html>`)
	if doc, _ = show(htmlID); !strings.Contains(doc, "v2 after the edit") {
		t.Fatalf("follow-up edit not visible: %s", doc)
	}
	if doc, _ = show(mdID); !strings.Contains(doc, `<div class="tbl"><table>`) || !strings.Contains(doc, "<h1>Notes</h1>") {
		t.Fatalf("show md: %s", doc)
	}
	raw, _, code := runAgentDeckEnv(t, home, "", env, "open", "--show", mdID)
	if code != 0 || !strings.HasPrefix(raw, "<!doctype html>") {
		t.Fatalf("show raw: %d %q", code, raw)
	}

	// Privacy at serve time: a page whose file was swapped for a symlink out of
	// the project is refused, not followed.
	if err := os.Remove(notes); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, notes); err != nil {
		t.Fatal(err)
	}
	out2, _, code := runAgentDeckEnv(t, home, "", env, "open", "--show", mdID, "--json")
	if code == 0 || !strings.Contains(out2, `"code":"outside_roots"`) || strings.Contains(out2, "private") {
		t.Fatalf("symlinked page must be refused: %d %s", code, out2)
	}
	for _, bad := range []string{"../../etc/passwd", "zzzzzzzzzzzzzzzz", "0123456789abcdef"} {
		out, _, code := runAgentDeckEnv(t, home, "", env, "open", "--show", bad, "--json")
		if code == 0 || !strings.Contains(out, `"code":"not_found"`) {
			t.Fatalf("bad page id %q: %d %s", bad, code, out)
		}
	}

	// --last re-opens the newest page and keeps the desk's shape.
	r, out, code = run("open", "--last", "--json", "--wait", "0")
	if code != 0 || r.Status != "queued" || r.URL != "https://example.com/plan" || r.Session != added.ID || r.Title != "Idea board" {
		t.Fatalf("last: %d %+v %s", code, r, out)
	}
	// Opening a file again keeps its page id, so a phone showing it can refresh.
	r, out, code = run("open", page, "--json", "--wait", "0")
	if code != 0 || r.PageID != htmlID {
		t.Fatalf("re-open must keep page id %s: %d %+v %s", htmlID, code, r, out)
	}
	// --last for a session with no page: exit 2 with code no_page.
	out, errOut, code := runAgentDeckEnv(t, home, "", []string{"SSH_CONNECTION=test"}, "open", "--last", "--session", empty.ID, "--json")
	if code != 2 || !strings.Contains(out, `"code":"no_page"`) {
		t.Fatalf("last without page: %d %q %q", code, out, errOut)
	}
	_, errOut, code = runAgentDeckEnv(t, home, "", []string{"SSH_CONNECTION=test"}, "open", "--last", "--session", empty.ID)
	if code != 2 || !strings.Contains(errOut, "no page opened") {
		t.Fatalf("last without page (text): %d %q", code, errOut)
	}
	// Usage errors.
	if _, _, code := runAgentDeckEnv(t, home, "", env, "open", "--last", "--list"); code == 0 {
		t.Fatal("--last with --list accepted")
	}
	if _, _, code := runAgentDeckEnv(t, home, "", env, "open", page, "--last"); code == 0 {
		t.Fatal("--last with a target accepted")
	}
}
