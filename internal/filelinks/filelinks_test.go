package filelinks

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// layout builds:
//
//	<tmp>/root/report.html, notes.txt, shot.png, logo.svg, blob.bin, sub/deep.txt
//	<tmp>/root/link-in -> report.html        (symlink staying inside)
//	<tmp>/root/link-out -> ../secret.txt     (symlink escaping)
//	<tmp>/root/dir-out -> ..                 (directory symlink escaping)
//	<tmp>/secret.txt
//	<tmp>/rootlink -> root                   (the root itself via a symlink)
func layout(t *testing.T) (tmp, root string) {
	t.Helper()
	tmp = t.TempDir()
	root = filepath.Join(tmp, "root")
	write := func(rel, body string) {
		p := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("root/report.html", "<script>alert(1)</script>")
	write("root/notes.txt", "hello\n")
	write("root/shot.png", "\x89PNG\r\n\x1a\n\x00\x00")
	write("root/logo.svg", `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	write("root/blob.bin", "a\x00b")
	write("root/sub/deep.txt", "deep")
	write("secret.txt", "TOP SECRET")
	for link, target := range map[string]string{
		"root/link-in":      "report.html",
		"root/link-out":     "../secret.txt",
		"root/dir-out":      "..",
		"rootlink":          "root",
		"root/dangling-out": "../missing.txt",
	} {
		if err := os.Symlink(target, filepath.Join(tmp, link)); err != nil {
			t.Fatal(err)
		}
	}
	return tmp, root
}

func TestOpenServesFilesUnderARoot(t *testing.T) {
	_, root := layout(t)
	cases := []struct {
		path, wantType string
		inline         bool
		body           string
	}{
		{filepath.Join(root, "notes.txt"), "text/plain; charset=utf-8", true, "hello\n"},
		{filepath.Join(root, "sub/deep.txt"), "text/plain; charset=utf-8", true, "deep"},
		{filepath.Join(root, "sub/../notes.txt"), "text/plain; charset=utf-8", true, "hello\n"},
		// HTML and SVG are shown as source, never rendered.
		{filepath.Join(root, "report.html"), "text/plain; charset=utf-8", true, "<script>alert(1)</script>"},
		{filepath.Join(root, "logo.svg"), "text/plain; charset=utf-8", true, ""},
		{filepath.Join(root, "shot.png"), "image/png", true, ""},
		{filepath.Join(root, "blob.bin"), "application/octet-stream", false, "a\x00b"},
		// A symlink that stays inside the root is fine.
		{filepath.Join(root, "link-in"), "text/plain; charset=utf-8", true, "<script>alert(1)</script>"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			f, err := Open([]string{root}, tc.path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer f.Close()
			if f.ContentType != tc.wantType || f.Inline != tc.inline {
				t.Errorf("type %q inline %v, want %q inline %v", f.ContentType, f.Inline, tc.wantType, tc.inline)
			}
			body, err := io.ReadAll(f)
			if err != nil {
				t.Fatal(err)
			}
			if tc.body != "" && string(body) != tc.body {
				t.Errorf("body %q, want %q (classify must rewind)", body, tc.body)
			}
		})
	}
}

func TestOpenRefusesEscapes(t *testing.T) {
	tmp, root := layout(t)
	cases := []struct {
		name, path string
		want       error
	}{
		{"dotdot", filepath.Join(root, "..", "secret.txt"), ErrOutside},
		{"dotdot unclean", root + "/sub/../../secret.txt", ErrOutside},
		{"symlink out", filepath.Join(root, "link-out"), ErrOutside},
		{"dir symlink out", filepath.Join(root, "dir-out", "secret.txt"), ErrOutside},
		{"outside entirely", filepath.Join(tmp, "secret.txt"), ErrOutside},
		{"relative", "root/notes.txt", ErrNotAbsolute},
		{"empty", "", ErrNotAbsolute},
		{"nul byte", filepath.Join(root, "notes.txt") + "\x00.png", ErrNotAbsolute},
		{"missing", filepath.Join(root, "nope.txt"), ErrNotFound},
		{"missing outside reads as outside", filepath.Join(tmp, "nope.txt"), ErrOutside},
		{"dangling symlink out reads as outside", filepath.Join(root, "dangling-out"), ErrOutside},
		{"directory (no listing)", filepath.Join(root, "sub"), ErrNotRegular},
		{"the root itself", root, ErrNotRegular},
		// Reaching the root through an outside symlink to it is refused up
		// front: only paths lexically under a root reach the filesystem.
		{"via an outside symlink to the root", filepath.Join(tmp, "rootlink", "notes.txt"), ErrOutside},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := Open([]string{root}, tc.path)
			if err == nil {
				f.Close()
				t.Fatalf("Open(%q) served %s", tc.path, f.Path)
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("Open(%q) = %v, want %v", tc.path, err, tc.want)
			}
		})
	}
}

func TestOpenSiblingSharingTheRootPrefix(t *testing.T) {
	_, root := layout(t)
	other := root + "-other"
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open([]string{root}, filepath.Join(other, "x.txt")); !errors.Is(err, ErrOutside) {
		t.Fatalf("a sibling dir sharing the root's prefix must be outside, got %v", err)
	}
}

func TestOpenIsOffWithoutRoots(t *testing.T) {
	_, root := layout(t)
	for _, roots := range [][]string{nil, {}} {
		if _, err := Open(roots, filepath.Join(root, "notes.txt")); !errors.Is(err, ErrDisabled) {
			t.Fatalf("Open with roots %v = %v, want ErrDisabled", roots, err)
		}
	}
}

func TestOpenSkipsAMissingRoot(t *testing.T) {
	_, root := layout(t)
	f, err := Open([]string{filepath.Join(root, "gone"), root}, filepath.Join(root, "notes.txt"))
	if err != nil {
		t.Fatalf("a missing first root must not block the next one: %v", err)
	}
	f.Close()
}

func TestOpenCapsTheSize(t *testing.T) {
	_, root := layout(t)
	big := filepath.Join(root, "big.log")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := Open([]string{root}, big); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Open(big) = %v, want ErrTooLarge", err)
	}
}

func TestNormalizeRoots(t *testing.T) {
	expand := func(p string) string { return strings.Replace(p, "~", "/home/me", 1) }
	got := NormalizeRoots([]string{" ~/proj ", "", "relative/dir", "/srv/x/", "/srv/x", "~/proj/../proj"}, expand)
	want := []string{"/home/me/proj", "/srv/x"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("NormalizeRoots = %v, want %v", got, want)
	}
	if NormalizeRoots(nil, expand) != nil || NormalizeRoots([]string{"rel"}, expand) != nil {
		t.Fatal("no usable roots must normalize to nil (off)")
	}
}

func TestIsTextAllowsARuneCutAtTheSniffLimit(t *testing.T) {
	b := []byte(strings.Repeat("a", 10) + "é")
	if !isText(b[:len(b)-1], true) {
		t.Fatal("a truncated sniff ending mid-rune is still text")
	}
	if isText(b[:len(b)-1], false) {
		t.Fatal("a complete file ending mid-rune is not valid UTF-8")
	}
}

// The open goes through os.Root on the resolved root, so a symlink swapped in
// between the resolve-and-check and the open still can't leave the root. This
// calls the open step directly with what such a race would hand it.
func TestOpenUnderRefusesASwappedInEscape(t *testing.T) {
	_, root := layout(t)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"link-out", "dir-out/secret.txt", "../secret.txt"} {
		f, err := openUnder(realRoot, rel)
		if err == nil {
			f.Close()
			t.Fatalf("openUnder(%q) left the root", rel)
		}
		// A symlink in place of the file is refused before the open; one
		// further up is refused by os.Root.
		if !errors.Is(err, ErrOutside) && !errors.Is(err, ErrNotRegular) {
			t.Errorf("openUnder(%q) = %v, want a refusal", rel, err)
		}
	}
}

// Lane review: a root that widens to / or to the home directory would expose
// every readable file (~/.ssh, the deck's own config and token). Refused, with
// the reason, whatever the entry looked like before expansion.
func TestCheckRootsRefusesWideningRoots(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	t.Setenv("FILELINKS_SET", "/srv/reports")
	t.Setenv("FILELINKS_EMPTY", "")
	expand := func(p string) string {
		p = os.ExpandEnv(p)
		if p == "~" || strings.HasPrefix(p, "~/") {
			return home + p[1:]
		}
		return p
	}
	roots, refused := CheckRoots([]string{
		"/", "//", "$FILELINKS_UNSET/", "${FILELINKS_UNSET}/reports", "$FILELINKS_EMPTY/x",
		"~", home, home + "/", "$FILELINKS_SET", "~/projects",
	}, expand)
	want := []string{"/srv/reports", filepath.Join(home, "projects")}
	if strings.Join(roots, ",") != strings.Join(want, ",") {
		t.Fatalf("roots = %v, want %v", roots, want)
	}
	if len(refused) != 8 {
		t.Fatalf("refused = %+v, want 8 entries", refused)
	}
	for _, r := range refused {
		if r.Entry == "" || r.Reason == "" {
			t.Errorf("refusal without entry or reason: %+v", r)
		}
	}
	if !strings.Contains(refused[2].Reason, "FILELINKS_UNSET") {
		t.Errorf("an unset variable should be named in the reason, got %q", refused[2].Reason)
	}
}

// Defense in depth: a root that only resolves to / (a symlink) is skipped at
// open time too.
func TestOpenSkipsARootResolvingToSlash(t *testing.T) {
	tmp := t.TempDir()
	link := filepath.Join(tmp, "slash")
	if err := os.Symlink("/", link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open([]string{link}, "/etc/hosts"); !errors.Is(err, ErrOutside) {
		t.Fatalf("Open via a root resolving to / = %v, want ErrOutside", err)
	}
}

// Lane review: a missing file behind a symlinked directory that leads outside
// must read as "outside", the same as an existing one, or the 404/403 split
// tells which outside paths exist.
func TestOpenGivesNoExistenceOracleBehindASymlinkedDir(t *testing.T) {
	tmp, root := layout(t)
	outside := filepath.Join(tmp, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "there.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{"dirlink": outside, "dangling-dir": filepath.Join(tmp, "nowhere")} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{
		filepath.Join(root, "dirlink", "there.txt"),
		filepath.Join(root, "dirlink", "missing.txt"),
		filepath.Join(root, "dirlink", "missing", "deeper.txt"),
		filepath.Join(root, "dangling-dir", "x.txt"),
	} {
		if _, err := Open([]string{root}, p); !errors.Is(err, ErrOutside) {
			t.Errorf("Open(%q) = %v, want ErrOutside", p, err)
		}
	}
	// A missing file genuinely inside the root is still "not found".
	if _, err := Open([]string{root}, filepath.Join(root, "sub", "missing", "x.txt")); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing inside = %v, want ErrNotFound", err)
	}
}

// Lane review: a root that CONTAINS the home directory, ~/.ssh or the deck's
// config or data directory (/home, /Users, $HOME/.., or a parent of an XDG dir
// outside home) exposes them as surely as / does. Refused by name and, via a
// symlink, at open time.
func TestRootsThatContainSensitiveDirsAreRefused(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "users", "me")
	cfg := filepath.Join(tmp, "xdg", "config")
	data := filepath.Join(tmp, "xdgdata", "share")
	for _, d := range []string{filepath.Join(home, "projects"), filepath.Join(home, ".ssh"), cfg, data, filepath.Join(tmp, "other")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), []byte("KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_DATA_HOME", data)

	roots, refused := CheckRoots([]string{
		filepath.Join(tmp, "users"),     // contains home
		home + "/..",                    // the same, spelled from $HOME
		tmp,                             // contains everything
		filepath.Join(tmp, "xdg"),       // contains the agent-deck config dir
		filepath.Join(tmp, "xdgdata"),   // contains the agent-deck data dir
		filepath.Join(home, ".ssh"),     // ~/.ssh itself
		filepath.Join(home, "projects"), // fine: inside home, not containing it
		filepath.Join(tmp, "other"),     // fine
	}, nil)
	want := []string{filepath.Join(home, "projects"), filepath.Join(tmp, "other")}
	if strings.Join(roots, ",") != strings.Join(want, ",") {
		t.Fatalf("roots = %v, want %v (refused %+v)", roots, want, refused)
	}
	if len(refused) != 6 {
		t.Fatalf("refused = %+v, want 6", refused)
	}

	// A root that only resolves to an ancestor of home (a symlink) is skipped
	// at open time, so ~/.ssh stays unreadable.
	link := filepath.Join(tmp, "other", "up")
	if err := os.Symlink(filepath.Join(tmp, "users"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open([]string{link}, filepath.Join(link, "me", ".ssh", "id_ed25519")); !errors.Is(err, ErrOutside) {
		t.Fatalf("Open via a root resolving to an ancestor of home = %v, want ErrOutside", err)
	}
}

// Lane review (CodeQL go/path-injection): a path outside every root must be
// refused before any filesystem call on it, so it can't time-probe outside
// paths or wake autofs and network mounts (/net/<host>/..., /Volumes).
func TestOpenNeverTouchesTheFilesystemForAnOutsidePath(t *testing.T) {
	tmp, root := layout(t)
	var touched []string
	realEval, realLstat := evalSymlinks, lstat
	t.Cleanup(func() { evalSymlinks, lstat = realEval, realLstat })
	evalSymlinks = func(p string) (string, error) { touched = append(touched, p); return realEval(p) }
	lstat = func(p string) (os.FileInfo, error) { touched = append(touched, p); return realLstat(p) }

	for _, p := range []string{
		"/net/somehost/share/x.txt",
		"/Volumes/Backup/x.txt",
		filepath.Join(tmp, "secret.txt"),
		filepath.Join(tmp, "nope", "x.txt"),
		root + "-other/x.txt",
		root + "/../secret.txt",
	} {
		touched = nil
		if _, err := Open([]string{root}, p); !errors.Is(err, ErrOutside) {
			t.Errorf("Open(%q) = %v, want ErrOutside", p, err)
		}
		if len(touched) != 0 {
			t.Errorf("Open(%q) touched the filesystem: %v", p, touched)
		}
	}

	// Inside a root the checks still run, and stay inside the root.
	touched = nil
	if _, err := Open([]string{root}, filepath.Join(root, "sub", "missing", "x.txt")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing inside = %v, want ErrNotFound", err)
	}
	for _, p := range touched {
		if p != root && !strings.HasPrefix(p, root+string(filepath.Separator)) {
			t.Errorf("an inside lookup touched %q, outside the root", p)
		}
	}
}
