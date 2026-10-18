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

// NormalizeRoots expands each entry with expand (for `~` and $VARS), keeps
// absolute ones, cleans them and drops duplicates in order. Relative entries
// are dropped: they would depend on the server's working directory.
func NormalizeRoots(entries []string, expand func(string) string) []string {
	out := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, raw := range entries {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if expand != nil {
			p = expand(p)
		}
		if !filepath.IsAbs(p) {
			continue
		}
		p = filepath.Clean(p)
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
	path = filepath.Clean(path)
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		// Say "not found" only under a root, so the route can't be used to
		// probe which paths exist elsewhere. A path that is there but won't
		// resolve (a symlink to a missing target) is "outside": else a
		// planted symlink would tell existing outside targets from missing.
		if _, lerr := os.Lstat(path); lerr == nil {
			return nil, ErrOutside
		}
		for _, root := range roots {
			if _, ok := within(root, path); ok {
				return nil, ErrNotFound
			}
		}
		return nil, ErrOutside
	}
	for _, root := range roots {
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		rel, ok := within(realRoot, real)
		if !ok {
			continue
		}
		return openUnder(realRoot, rel)
	}
	return nil, ErrOutside
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
