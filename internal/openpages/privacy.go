package openpages

import (
	"bytes"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
)

// The phone policy, on top of the filelinks roots (which decide WHERE a file
// may be): WHAT may leave the computer for the phone. Deny by default.
//
//   - Private names never leave: any path component below the root that
//     starts with "." (.env, .git/, .ssh/, .aws/, .npmrc, .netrc, .claude/ …)
//     or looks like a secret (secret, credential, password, token, private
//     key, *.pem, *.key, *.p12, *.pfx, *.kdbx, id_rsa …). The check runs on
//     the symlink-resolved path, so a harmless-looking link to a private file
//     is private too. It applies to the page, every inlined asset and every
//     relative link in it.
//   - First, an allowlist of file types: pages are .html .htm .md .markdown
//     .txt only (PageKind); anything else stays Mac-only. Inlined assets are
//     images, stylesheets and fonts only (assetType), and their bytes must
//     match their type (an image sniffs as an image, a stylesheet parses as
//     CSS (isStylesheet), a font has a font signature). Scripts, frames,
//     objects and media files are never inlined.
//   - The private-name rules above are the second layer, on every page,
//     asset and link that passed the allowlist.
//   - What the rules cannot see: a secret WRITTEN INTO an allowed type under
//     an ordinary name (creds.txt, notes.md holding a password, a CSS value)
//     is shown when the session opens it, exactly like any other text the
//     session puts on a page. The rules stop files that are secret by kind
//     or by name, not secret content inside an ordinary page.

// ErrPrivate is returned for a dotfile or a secret-like path.
var ErrPrivate = errors.New("private file: dotfiles and secret-like names are never shown on the phone")

// ErrUnsupported is returned for a file kind the phone does not show.
var ErrUnsupported = errors.New("this kind of file is not shown on the phone")

// secretParts are lower-case substrings that make a name private.
var secretParts = []string{"secret", "credential", "password", "passwd", "token", "private_key", "privatekey", "private-key", "apikey", "api_key", "api-key", "htpasswd"}

// secretSuffixes are lower-case name endings that make a name private.
var secretSuffixes = []string{".pem", ".key", ".p12", ".pfx", ".kdbx", ".kdb", ".keystore", ".jks", ".ppk", ".gpg", ".pgp", ".asc", ".ovpn", ".tfstate", ".tfvars"}

// secretPrefixes are lower-case name beginnings that make a name private.
var secretPrefixes = []string{"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519"}

// PrivateName reports whether one path component is private.
func PrivateName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return true
	}
	n := strings.ToLower(name)
	for _, p := range secretPrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	for _, s := range secretSuffixes {
		if strings.HasSuffix(n, s) {
			return true
		}
	}
	for _, p := range secretParts {
		if strings.Contains(n, p) {
			return true
		}
	}
	return false
}

// Private reports whether real (a symlink-resolved absolute path) is private
// below the root that holds it. A path under no root is private.
func Private(roots []string, real string) bool {
	rel, ok := relUnderRoots(roots, real)
	if !ok {
		return true
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if PrivateName(part) {
			return true
		}
	}
	return false
}

// relUnderRoots is real relative to the deepest root (configured or
// resolved) that contains it.
func relUnderRoots(roots []string, real string) (string, bool) {
	best, found := "", false
	for _, root := range roots {
		cands := []string{filepath.Clean(root)}
		if r, err := filepath.EvalSymlinks(root); err == nil && r != cands[0] {
			cands = append(cands, r)
		}
		for _, c := range cands {
			rel, err := filepath.Rel(c, real)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
				continue
			}
			if !found || len(rel) < len(best) {
				best, found = rel, true
			}
		}
	}
	return best, found
}

// PageKind is the kind a file is shown as on the phone, or "" when the phone
// does not show it. This is the first layer of the phone policy, an
// ALLOWLIST of page types: .html .htm .md .markdown .txt, nothing else (no
// logs, CSV, JSON, YAML, config, images or files without an extension).
// The private-name rules (Private) are the second layer on top of it.
func PageKind(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".html", ".htm":
		return KindHTML
	case ".md", ".markdown":
		return KindMarkdown
	case ".txt":
		return KindText
	}
	return ""
}

// assetType is the data: type an inlinable asset is sent as, or "" when the
// extension is not on the allowlist.
func assetType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".avif":
		return "image/avif"
	case ".bmp":
		return "image/bmp"
	case ".ico":
		return "image/x-icon"
	case ".css":
		return "text/css"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".ttf":
		return "font/ttf"
	case ".otf":
		return "font/otf"
	}
	return ""
}

// contentMatches reports whether data really is of type t, so a secret
// renamed to logo.png or app.css is not inlined.
func contentMatches(t string, data []byte) bool {
	switch {
	case t == "text/css":
		return isStylesheet(data)
	case t == "image/avif":
		return len(data) >= 12 && string(data[4:8]) == "ftyp"
	case t == "image/x-icon":
		return len(data) >= 4 && data[0] == 0 && data[1] == 0 && (data[2] == 1 || data[2] == 2) && data[3] == 0
	case strings.HasPrefix(t, "image/"):
		return strings.HasPrefix(http.DetectContentType(data), "image/")
	case t == "font/woff2":
		return bytes.HasPrefix(data, []byte("wOF2"))
	case t == "font/woff":
		return bytes.HasPrefix(data, []byte("wOFF"))
	case t == "font/ttf", t == "font/otf":
		return bytes.HasPrefix(data, []byte{0, 1, 0, 0}) || bytes.HasPrefix(data, []byte("OTTO")) || bytes.HasPrefix(data, []byte("true"))
	}
	return false
}

// PageCSP is the policy every rendered page carries in a <meta> at its top
// (and the web route also sends as a header): the page may run its own
// inline scripts and styles and show inlined images and fonts, and nothing
// else. No network at all (no fetch, XHR, WebSocket, beacon, remote image,
// script, style or font, no frames, no form posts, no <base>).
const PageCSP = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline' data:; img-src data:; font-src data:; media-src data:; " +
	"connect-src 'none'; frame-src 'none'; child-src 'none'; worker-src 'none'; object-src 'none'; form-action 'none'; base-uri 'none'"
