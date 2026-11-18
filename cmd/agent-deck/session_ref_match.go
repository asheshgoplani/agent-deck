package main

import (
	"strings"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// sessionRefMinIDPrefix is the shortest id prefix that names a session.
const sessionRefMinIDPrefix = 4

const (
	sessionRefTiedTitle = "title"
	sessionRefTiedID    = "id"
)

// matchSessionRef is the one rule for which session an <id|title> argument
// names. ResolveSession (every `session ...` verb, rename, remove, open,
// browser, ask) and `switch` both go through it. In order:
//
//  1. the exact id
//  2. the exact title, as stored
//  3. an id prefix of at least sessionRefMinIDPrefix characters that one
//     session has
//  4. the title ignoring case, surrounding space and leading decoration
//     (session.TitleWithoutDecoration: the voice daemon's "🎙 " marker)
//
// It returns the index of the one match, or -1 with the indexes that tie
// and what they tie on (sessionRefTiedTitle or sessionRefTiedID). No match
// and no tie is -1, nil, "".
func matchSessionRef(ref string, n int, idOf, titleOf func(int) string) (index int, tied []int, tiedOn string) {
	if ref == "" {
		return -1, nil, ""
	}
	for i := 0; i < n; i++ {
		if idOf(i) == ref {
			return i, nil, ""
		}
	}
	var byTitle []int
	for i := 0; i < n; i++ {
		if titleOf(i) == ref {
			byTitle = append(byTitle, i)
		}
	}
	switch {
	case len(byTitle) == 1:
		return byTitle[0], nil, ""
	case len(byTitle) > 1:
		return -1, byTitle, sessionRefTiedTitle
	}
	trimmed := strings.TrimSpace(ref)
	var byPrefix []int
	if utf8.RuneCountInString(trimmed) >= sessionRefMinIDPrefix && !strings.ContainsAny(trimmed, " \t") {
		for i := 0; i < n; i++ {
			if strings.HasPrefix(idOf(i), trimmed) {
				byPrefix = append(byPrefix, i)
			}
		}
		if len(byPrefix) == 1 {
			return byPrefix[0], nil, ""
		}
	}
	if want := foldSessionTitle(ref); want != "" {
		for i := 0; i < n; i++ {
			if foldSessionTitle(titleOf(i)) == want {
				byTitle = append(byTitle, i)
			}
		}
	}
	switch {
	case len(byTitle) == 1:
		return byTitle[0], nil, ""
	case len(byTitle) > 1:
		return -1, byTitle, sessionRefTiedTitle
	case len(byPrefix) > 1:
		return -1, byPrefix, sessionRefTiedID
	}
	return -1, nil, ""
}

// foldSessionTitle is the form two titles are compared in when the stored
// spelling did not match: no leading decoration, no surrounding space, one
// case. Read-side only: the stored title is never changed.
func foldSessionTitle(title string) string {
	return strings.ToLower(strings.TrimSpace(session.TitleWithoutDecoration(title)))
}
