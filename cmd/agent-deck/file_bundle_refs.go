package main

import (
	"bytes"
	"net/url"
	"path"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// pageReferences lists the folder-relative files an HTML page loads: src,
// poster, data and srcset attributes, href except on <a> and <area> links, plus url() and @import in
// style attributes and <style> blocks. URLs with a scheme or host, root paths,
// fragments and paths leaving the folder are dropped. It is a best-effort
// list; files a script builds names for are not seen.
func pageReferences(page string, data []byte) []string {
	var refs []string
	tokens := html.NewTokenizer(bytes.NewReader(data))
	inStyle := false
	for {
		kind := tokens.Next()
		switch kind {
		case html.ErrorToken:
			return refs
		case html.StartTagToken, html.SelfClosingTagToken:
			token := tokens.Token()
			inStyle = token.Data == "style" && kind == html.StartTagToken
			for _, attr := range token.Attr {
				switch attr.Key {
				case "href":
					// <a> and <area> links are navigation, not loads: a linked
					// page may be as heavy as it likes (it can still be added
					// later, best effort, as one of the folder's other files).
					if token.Data != "a" && token.Data != "area" {
						refs = appendBundleRef(refs, page, attr.Val)
					}
				case "src", "poster", "data":
					refs = appendBundleRef(refs, page, attr.Val)
				case "srcset":
					for _, candidate := range strings.Split(attr.Val, ",") {
						if fields := strings.Fields(candidate); len(fields) > 0 {
							refs = appendBundleRef(refs, page, fields[0])
						}
					}
				case "style":
					refs = append(refs, cssReferences(page, attr.Val)...)
				}
			}
		case html.EndTagToken:
			inStyle = false
		case html.TextToken:
			if inStyle {
				refs = append(refs, cssReferences(page, string(tokens.Text()))...)
			}
		}
	}
}

var (
	cssURLPattern    = regexp.MustCompile(`url\(\s*(?:"([^"]*)"|'([^']*)'|([^'")\s]*))\s*\)`)
	cssImportPattern = regexp.MustCompile(`@import\s+(?:"([^"]*)"|'([^']*)')`)
)

// cssReferences lists the folder-relative files a stylesheet (or a style
// attribute of the file named from) loads through url() and @import.
func cssReferences(from, css string) []string {
	var refs []string
	for _, pattern := range []*regexp.Regexp{cssURLPattern, cssImportPattern} {
		for _, match := range pattern.FindAllStringSubmatch(css, -1) {
			for _, group := range match[1:] {
				if group != "" {
					refs = appendBundleRef(refs, from, group)
					break
				}
			}
		}
	}
	return refs
}

// appendBundleRef resolves raw against the folder of from and appends it when
// it names a file inside the bundle folder.
func appendBundleRef(refs []string, from, raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "#") {
		return refs
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" || u.Path == "" || strings.HasPrefix(u.Path, "/") {
		return refs
	}
	name := path.Clean(path.Join(path.Dir(from), u.Path))
	if name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return refs
	}
	return append(refs, name)
}
