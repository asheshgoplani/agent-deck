package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenPhonePolicyMatrix replays the feature sweep's CLI matrix for
// `open` (openphone-test/cli.log, 18 Nov 2026) against the real binary, plus
// the private-file rows it found missing (D1 .env, D5 too large, D6 binary).
// For every file row: what reaches the phone list ("phone"), and that the
// private marker never appears in anything printed for the phone.
func TestOpenPhonePolicyMatrix(t *testing.T) {
	parallelSubprocessTest(t)
	home := t.TempDir()
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "proj")
	const marker = "SECRET-VALUE-42"
	w := func(rel string, data []byte) {
		t.Helper()
		p := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde")
	for _, rel := range []string{"proj/.env", "proj/.git/config", "proj/.aws/credentials", "proj/server.pem", "proj/id_rsa", "proj/client_secret.json", ".ssh/id_x", "other/secret.txt"} {
		w(rel, []byte(marker))
	}
	w("proj/report.html", []byte(`<html><head><title>Report</title></head><body><img src="assets/pic.png"><img src=".env"><a href=".env">x</a></body></html>`))
	w("proj/notes.md", []byte("# Notes\n\n![p](assets/pic.png) ![e](.env) [env](.env)\n"))
	w("proj/notes ünï cödé 日本.md", []byte("# Unicode\n"))
	w("proj/my report (v2).html", []byte("<title>V2</title><p>v2</p>"))
	w("proj/plain.txt", []byte("plain\n"))
	w("proj/assets/pic.png", png)
	w("proj/blob.bin", []byte{0, 1, 2, 0, 3})
	w("proj/huge.html", []byte(strings.Repeat("a", 4<<20+1)))
	for link, target := range map[string]string{"proj/sshlink.html": "../.ssh/id_x", "proj/link.md": "../other/secret.txt",
		"proj/piclink.png": "assets/pic.png", "proj/linkdir": "../other", "proj/innocent.md": ".env"} {
		if err := os.Symlink(target, filepath.Join(tmp, link)); err != nil {
			t.Fatal(err)
		}
	}
	add := func(dir, title string) string {
		t.Helper()
		out, errOut, code := runAgentDeck(t, home, "add", dir, "--title", title, "-c", "bash", "--no-parent", "--json")
		var r struct {
			ID string `json:"id"`
		}
		if code != 0 || json.Unmarshal([]byte(out), &r) != nil || r.ID == "" {
			t.Fatalf("add %s: %d %s %s", title, code, out, errOut)
		}
		return r.ID
	}
	pager := add(proj, "pager")
	second := add(t.TempDir(), "second")
	env := []string{"SSH_CONNECTION=test"}
	run := func(args ...string) (map[string]any, string, int) {
		t.Helper()
		out, errOut, code := runAgentDeckEnv(t, home, "", env, args...)
		if strings.Contains(out+errOut, marker) {
			t.Fatalf("%v printed a private file: %s %s", args, out, errOut)
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(out), &m)
		return m, out + errOut, code
	}
	str := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }

	// File rows: target -> what the phone gets.
	files := []struct{ target, phone, where string }{
		{"report.html", "shown", "phone"},
		{"notes.md", "shown", "phone"},
		{"notes ünï cödé 日本.md", "shown", "phone"},
		{"my report (v2).html", "shown", "phone"},
		{"plain.txt", "shown", "phone"},
		{"assets/pic.png", "shown", "phone"},
		{"piclink.png", "shown", "phone"},
		{".env", "private", "mac"},             // sweep D1
		{".git/config", "private", "mac"},      // dot directory
		{".aws/credentials", "private", "mac"}, // dot directory
		{"server.pem", "private", "mac"},
		{"id_rsa", "private", "mac"},
		{"client_secret.json", "private", "mac"},
		{"innocent.md", "private", "mac"},  // symlink to .env
		{"huge.html", "too_large", "mac"},  // sweep D5
		{"blob.bin", "unsupported", "mac"}, // sweep D6
		{"sshlink.html", "outside_roots", "mac"},
		{"link.md", "outside_roots", "mac"},
		{"linkdir/secret.txt", "outside_roots", "mac"},
		{"../other/secret.txt", "outside_roots", "mac"},
		{"../.ssh/id_x", "outside_roots", "mac"},
		{filepath.Join(tmp, "other", "secret.txt"), "outside_roots", "mac"},
		{"/etc/passwd", "outside_roots", "mac"},
	}
	ids := map[string]string{}
	for _, f := range files {
		target := f.target
		if !filepath.IsAbs(target) {
			target = filepath.Join(proj, target)
		}
		m, out, code := run("open", target, "--session", "pager", "--wait", "0", "--json")
		if code != 0 || str(m, "phone") != f.phone || str(m, "where") != f.where || str(m, "status") != "queued" {
			t.Errorf("open %s: exit %d phone %q where %q; want %q %q\n%s", f.target, code, str(m, "phone"), str(m, "where"), f.phone, f.where, out)
		}
		if f.phone == "shown" {
			ids[f.target] = str(m, "page_id")
		} else if str(m, "page_id") != "" {
			t.Errorf("open %s: a page id for a file kept off the phone", f.target)
		}
	}
	// The same project file opened for a session whose project it is not.
	if m, out, _ := run("open", filepath.Join(proj, "report.html"), "--session", "second", "--wait", "0", "--json"); str(m, "where") != "mac" || str(m, "phone") != "outside_roots" {
		t.Errorf("other session's project: %s", out)
	}
	// Refusals with codes and exit status.
	for _, tc := range []struct {
		args []string
		code string
		exit int
	}{
		{[]string{"open", "--last", "--session", "second", "--wait", "0", "--json"}, "no_page", 2},
		{[]string{"open", filepath.Join(proj, "report.html"), "--wait", "0", "--json"}, "session_not_found", 1},
		{[]string{"open", filepath.Join(proj, "report.html"), "--session", "nope", "--wait", "0", "--json"}, "session_not_found", 1},
		{[]string{"open", filepath.Join(proj, "missing.html"), "--session", "pager", "--wait", "0", "--json"}, "not_found", 1},
		{[]string{"open", proj, "--session", "pager", "--wait", "0", "--json"}, "not_found", 1},
		{[]string{"open", "file:///etc/passwd", "--session", "pager", "--wait", "0", "--json"}, "invalid_target", 1},
		{[]string{"open", "javascript:alert(1)", "--session", "pager", "--wait", "0", "--json"}, "invalid_target", 1},
		{[]string{"open", "http://127.0.0.1:8080/", "--session", "pager", "--wait", "0", "--json"}, "invalid_target", 1},
		{[]string{"open", "--show", "0000000000000000", "--session", "pager", "--json"}, "not_found", 1},
		{[]string{"open", "--show", "../../x", "--session", "pager", "--json"}, "not_found", 1},
	} {
		m, out, exit := run(tc.args...)
		if exit != tc.exit || str(m, "code") != tc.code {
			t.Errorf("%v: exit %d code %q; want %d %q\n%s", tc.args, exit, str(m, "code"), tc.exit, tc.code, out)
		}
	}
	if _, out, exit := run("open", "--last", "--list", "--session", "pager", "--json"); exit == 0 {
		t.Errorf("--last with --list accepted: %s", out)
	}
	// The list holds exactly the shown files; --show of each is clean.
	listOut, _, code := runAgentDeckEnv(t, home, "", env, "open", "--list", "--session", "pager", "--json")
	var list struct {
		Pages []struct{ ID, Name string } `json:"pages"`
	}
	if code != 0 || json.Unmarshal([]byte(listOut), &list) != nil || len(list.Pages) != 6 { // piclink.png resolves to assets/pic.png: one entry
		t.Fatalf("list: %d %s", code, listOut)
	}
	for target, id := range ids {
		m, out, exit := run("open", "--show", id, "--session", "pager", "--json")
		doc := str(m, "html")
		if exit != 0 || !strings.Contains(doc, `<meta http-equiv="Content-Security-Policy" content="default-src 'none'`) {
			t.Errorf("--show %s: %d %s", target, exit, out)
		}
		if strings.Contains(doc, "connect-src https") || strings.Contains(doc, `href=".env"`) || strings.Contains(doc, "data:application/octet-stream") {
			t.Errorf("--show %s leaks: %s", target, doc)
		}
	}
	// A shown page later swapped for a symlink to a private file is refused at read time.
	reportID := ids["report.html"]
	if err := os.Rename(filepath.Join(proj, "report.html"), filepath.Join(proj, "report.orig")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".env", filepath.Join(proj, "report.html")); err != nil {
		t.Fatal(err)
	}
	if m, out, exit := run("open", "--show", reportID, "--session", "pager", "--json"); exit == 0 || str(m, "code") != "outside_roots" {
		t.Errorf("swapped page: %d %s", exit, out)
	}
	_, _ = pager, second

	// S3: no host path reaches the phone (list, show).
	listOut, _, _ = runAgentDeckEnv(t, home, "", env, "open", "--list", "--session", "pager", "--json")
	if strings.Contains(listOut, tmp) || strings.Contains(listOut, `"path"`) {
		t.Errorf("the list carries host paths: %s", listOut)
	}
	if text, _, code := runAgentDeckEnv(t, home, "", env, "open", "--list", "--session", "pager"); code != 0 || strings.HasPrefix(strings.TrimSpace(text), "{") || !strings.Contains(text, "Notes") {
		t.Errorf("--list without --json prints lines: %d %q", code, text)
	}
	// S9: a relative link to a listed page points at that page by id.
	w("proj/docs/index.html", []byte(`<title>Docs</title><a href="../notes.md">notes</a> <a href="plan.md">not listed</a>`))
	w("proj/docs/plan.md", []byte("# Plan\n"))
	m, out, _ := run("open", filepath.Join(proj, "docs", "index.html"), "--session", "pager", "--wait", "0", "--json")
	docsID := str(m, "page_id")
	m, out, _ = run("open", "--show", docsID, "--session", "pager", "--json")
	if doc := str(m, "html"); !strings.Contains(doc, `<a href="/@page/`+ids["notes.md"]+`">notes</a>`) || !strings.Contains(doc, `<a href="plan.md">not listed</a>`) {
		t.Errorf("relative links by page id: %s", out)
	}
	// S2: --last skips a newest page whose file was deleted.
	w("proj/gone.md", []byte("# Gone\n"))
	run("open", filepath.Join(proj, "gone.md"), "--session", "pager", "--wait", "0", "--json")
	if err := os.Remove(filepath.Join(proj, "gone.md")); err != nil {
		t.Fatal(err)
	}
	m, out, code = run("open", "--last", "--session", "pager", "--wait", "0", "--json")
	if code != 0 || str(m, "page_id") != docsID {
		t.Errorf("--last after the newest file was deleted: %d %s", code, out)
	}
}
