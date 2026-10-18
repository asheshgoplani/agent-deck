// Package filelinks serves file:// links from the web terminal (issue #2676).
//
// A browser on the deck's http(s) origin cannot navigate to file://, so the
// web terminal rewrites a link under a configured root to GET /api/files and
// the deck reads the file for it. `agent-deck file read` applies the same
// rules from the CLI. The policy, all of it enforced here:
//
//   - Off unless `[web] file_link_roots` names at least one directory.
//   - The requested path must be absolute. Symlinks are resolved before the
//     root check, and the file is then opened through os.Root on the resolved
//     root, so neither `..` nor a symlink (even one swapped in after the
//     check) can reach outside it.
//   - Regular files only: no directory listings, devices or FIFOs.
//   - At most MaxBytes.
//   - Only a fixed set of raster image types is served as itself. Other text
//     is served as text/plain and anything else as an attachment, so an HTML
//     or SVG file never runs script on the deck's origin.
package filelinks

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// evalSymlinks and lstat are the only filesystem calls Open makes on the
// requested path; tests swap them to prove an outside path never reaches
// them.
var (
	evalSymlinks = filepath.EvalSymlinks
	lstat        = os.Lstat
)

// MaxBytes caps a served file. Terminal links point at reports, logs and
// screenshots; anything bigger is copied by path instead.
const MaxBytes int64 = 32 << 20

// sniffBytes is how much of a file decides text vs. binary.
const sniffBytes = 8 << 10

var (
	ErrDisabled    = errors.New("file links are off: set [web] file_link_roots")
	ErrNotAbsolute = errors.New("path must be absolute")
	ErrOutside     = errors.New("path is not under a configured file_link_roots entry")
	ErrNotFound    = errors.New("file not found")
	ErrNotRegular  = errors.New("not a regular file")
	ErrTooLarge    = errors.New("file is larger than the file link limit")
)

// imageTypes are served inline as themselves. SVG is deliberately absent: it
// can carry script.
var imageTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".avif": "image/avif",
	".bmp":  "image/bmp",
	".ico":  "image/x-icon",
}

// Refused is a file_link_roots entry that was dropped, and why.
type Refused struct {
	Entry  string `json:"entry"`
	Reason string `json:"reason"`
}

// NormalizeRoots is CheckRoots without the refusals.
func NormalizeRoots(entries []string, expand func(string) string) []string {
	roots, _ := CheckRoots(entries, expand)
	return roots
}

// CheckRoots expands each entry with expand (for `~` and $VARS), keeps
// absolute ones, cleans them and drops duplicates in order. It refuses, with
// a reason:
//   - a relative entry (it would depend on the server's working directory);
//   - an entry naming an unset or empty $VAR (under launchd or systemd,
//     "$REPORTS/" would otherwise widen to "/");
//   - "/" and any root containing the home directory, ~/.ssh or agent-deck's
//     config or data directory (/home, /Users, $HOME/..), which would
//     expose ~/.ssh and the deck's own config and token.
func CheckRoots(entries []string, expand func(string) string) ([]string, []Refused) {
	sensitive := sensitiveDirs()
	out := make([]string, 0, len(entries))
	var refused []Refused
	seen := make(map[string]struct{}, len(entries))
	for _, raw := range entries {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if v := emptyVar(p); v != "" {
			refused = append(refused, Refused{raw, "$" + v + " is unset or empty"})
			continue
		}
		if expand != nil {
			p = expand(p)
		}
		if !filepath.IsAbs(p) {
			refused = append(refused, Refused{raw, "not an absolute path"})
			continue
		}
		p = filepath.Clean(p)
		if reason := tooWide(p, sensitive); reason != "" {
			refused = append(refused, Refused{raw, reason})
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if len(out) == 0 {
		out = nil
	}
	return out, refused
}

// emptyVar names the first $VAR in p that is unset or empty, or "".
func emptyVar(p string) string {
	missing := ""
	os.Expand(p, func(name string) string {
		v := os.Getenv(name)
		if v == "" && missing == "" {
			missing = name
		}
		return v
	})
	return missing
}

// sensitiveDirs are directories no root may contain: the home directory,
// ~/.ssh, and agent-deck's config and data directories (which hold the web
// token and push keys), wherever XDG puts them. Each comes cleaned and, when
// it exists, symlink-resolved too.
func sensitiveDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	dirs := []string{home, filepath.Join(home, ".ssh")}
	for _, dir := range []func() (string, error){agentpaths.LegacyDir, agentpaths.ConfigDir, agentpaths.DataDir} {
		if d, err := dir(); err == nil {
			dirs = append(dirs, d)
		}
	}
	out := make([]string, 0, 2*len(dirs))
	for _, d := range dirs {
		d = filepath.Clean(d)
		out = append(out, d)
		if real, err := filepath.EvalSymlinks(d); err == nil && real != d {
			out = append(out, real)
		}
	}
	return out
}

// tooWide says why a cleaned absolute root would expose too much, or "".
func tooWide(p string, sensitive []string) string {
	if p == string(filepath.Separator) {
		return "the filesystem root would expose every readable file"
	}
	for _, d := range sensitive {
		if _, ok := within(p, d); ok {
			return "it contains " + d + " (home, ~/.ssh or the deck's own config); list folders inside home instead"
		}
	}
	return ""
}

// File is an open, policy-checked file. The caller must Close it and read
// it through Reader, which stops at the size that passed the cap even if the
// file grows (or, like /proc files, reports size 0).
type File struct {
	*os.File
	// Path is the resolved (symlink-free) path that was opened.
	Path string
	Size int64
	// ContentType is what the file is served as.
	ContentType string
	// Inline is false when the file must be served as a download.
	Inline bool
}

// Reader reads the file up to the size that passed the cap.
func (f *File) Reader() *io.SectionReader {
	return io.NewSectionReader(f.File, 0, f.Size)
}

// Open resolves path against roots and opens it under the policy above.
func Open(roots []string, path string) (*File, error) {
	if len(roots) == 0 {
		return nil, ErrDisabled
	}
	if strings.ContainsRune(path, 0) || !filepath.IsAbs(path) {
		return nil, ErrNotAbsolute
	}
	realRoots := resolveRoots(roots)
	path = filepath.Clean(path)
	// Nothing outside the roots reaches the filesystem: no timing probe of
	// outside paths, and no waking autofs or network mounts (/net/<host>,
	// /Volumes). Only a path lexically under a configured or resolved root
	// is looked at; the checks below still decide.
	base, ok := lexicalRoot(path, roots, realRoots)
	if !ok {
		return nil, ErrOutside
	}
	real, err := evalSymlinks(path)
	if err != nil {
		return nil, missingAnswer(realRoots, base, path)
	}
	for _, realRoot := range realRoots {
		if rel, ok := within(realRoot, real); ok {
			return openUnder(realRoot, rel)
		}
	}
	return nil, ErrOutside
}

// resolveRoots resolves each root through symlinks, dropping missing ones and
// any that resolve to / or to a directory containing a sensitive one
// (CheckRoots refuses those by name; this also catches a root that is a
// symlink to them).
func resolveRoots(roots []string) []string {
	sensitive := sensitiveDirs()
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil || tooWide(realRoot, sensitive) != "" {
			continue
		}
		out = append(out, realRoot)
	}
	return out
}

// missingAnswer answers for a path that doesn't resolve. "Not found" only when
// the longest existing parent resolves inside a root; anything that exists but
// won't resolve (a dangling symlink, at any depth) or leads outside is
// "outside", so the 404/403 split never tells which outside paths exist.
//
// The walk never leaves base, the root path is lexically under.
func missingAnswer(realRoots []string, base, path string) error {
	for p := path; lexicallyWithin(base, p); p = filepath.Dir(p) {
		if real, err := evalSymlinks(p); err == nil {
			for _, realRoot := range realRoots {
				if _, ok := within(realRoot, real); ok {
					return ErrNotFound
				}
			}
			return ErrOutside
		}
		if _, err := lstat(p); err == nil {
			return ErrOutside
		}
	}
	return ErrOutside
}

// lexicalRoot returns the configured or resolved root that the cleaned path
// is lexically under, without touching the filesystem.
func lexicalRoot(path string, roots, realRoots []string) (string, bool) {
	for _, set := range [][]string{realRoots, roots} {
		for _, root := range set {
			root = filepath.Clean(root)
			if tooWide(root, nil) != "" {
				continue
			}
			if lexicallyWithin(root, path) {
				return root, true
			}
		}
	}
	return "", false
}

// lexicallyWithin reports whether the cleaned path p is base or below it.
func lexicallyWithin(base, p string) bool {
	return p == base || strings.HasPrefix(p, base+string(filepath.Separator))
}

// within reports whether p is root or inside it, and p relative to root.
func within(root, p string) (string, bool) {
	rel, err := filepath.Rel(root, p)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

func openUnder(realRoot, rel string) (*File, error) {
	root, err := os.OpenRoot(realRoot)
	if err != nil {
		return nil, ErrNotFound
	}
	defer root.Close()
	// Refuse anything but a regular file before opening it, so a device's
	// open-time side effects never run. The check after the open still
	// decides; O_NONBLOCK keeps a FIFO swapped in meanwhile from blocking the
	// open until a writer appears (it has no effect on a regular file).
	if info, err := root.Lstat(rel); err == nil && !info.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		// os.Root refuses a path that escapes the root at open time.
		return nil, ErrOutside
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, ErrNotFound
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, ErrNotRegular
	}
	if info.Size() > MaxBytes {
		_ = f.Close()
		return nil, fmt.Errorf("%w (%d bytes, limit %d)", ErrTooLarge, info.Size(), MaxBytes)
	}
	contentType, inline, err := classify(f, rel)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &File{File: f, Path: filepath.Join(realRoot, rel), Size: info.Size(), ContentType: contentType, Inline: inline}, nil
}

// classify picks the served type and rewinds f.
func classify(f *os.File, name string) (string, bool, error) {
	if t, ok := imageTypes[strings.ToLower(filepath.Ext(name))]; ok {
		return t, true, nil
	}
	head := make([]byte, sniffBytes)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", false, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", false, err
	}
	if isText(head[:n], n == sniffBytes) {
		return "text/plain; charset=utf-8", true, nil
	}
	return "application/octet-stream", false, nil
}

// isText reports whether b reads as UTF-8 text with no NUL bytes. When b was
// cut at sniffBytes, a multi-byte rune split at the end is allowed.
func isText(b []byte, truncated bool) bool {
	if bytes.IndexByte(b, 0) >= 0 {
		return false
	}
	if truncated {
		for i := 0; i < utf8.UTFMax && len(b) > 0 && !utf8.Valid(b); i++ {
			b = b[:len(b)-1]
		}
	}
	return utf8.Valid(b)
}
