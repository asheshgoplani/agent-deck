package tree

import (
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/history/model"
)

func subseq(hay, needle string) bool {
	h := strings.ToLower(hay)
	n := []rune(strings.ToLower(needle))
	i := 0
	for _, r := range h {
		if i < len(n) && n[i] == r {
			i++
		}
	}
	return i == len(n)
}

func matches(s model.Session, q string) bool {
	return subseq(s.Title, q) || subseq(s.CWD, q) || subseq(s.GitBranch, q)
}

func Filter(projects []model.Project, query string) []model.Project {
	if strings.TrimSpace(query) == "" {
		return projects
	}
	var out []model.Project
	for _, p := range projects {
		var kept []model.Session
		for _, s := range p.Sessions {
			if matches(s, query) {
				kept = append(kept, s)
			}
		}
		if len(kept) > 0 {
			p.Sessions = kept
			out = append(out, p)
		}
	}
	return out
}
