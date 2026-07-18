package source

import (
	"bufio"
	"encoding/json"
	"os"
	"regexp"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/history/model"
)

// cmdWrapper matches Claude Code's injected local-command / caveat messages,
// so they aren't picked up as a session title.
var cmdWrapper = regexp.MustCompile(`^<[^>]*(command|caveat)`)

const maxScanLines = 5000
const maxLineBytes = 4 << 20 // 4MB per line

type rawLine struct {
	Type       string `json:"type"`
	CWD        string `json:"cwd"`
	GitBranch  string `json:"gitBranch"`
	AITitle    string `json:"aiTitle"`
	Summary    string `json:"summary"`
	LastPrompt string `json:"lastPrompt"`
	Message    *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				return b.Text
			}
		}
	}
	return ""
}

// ParseSessionMeta reads a bounded prefix of a session .jsonl and extracts
// display metadata. Malformed lines are skipped, not fatal.
func ParseSessionMeta(path string) (model.Session, error) {
	info, err := os.Stat(path)
	if err != nil {
		return model.Session{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return model.Session{}, err
	}
	defer f.Close()

	base := info.Name()
	s := model.Session{
		ID:       strings.TrimSuffix(base, ".jsonl"),
		Tool:     "claude-code",
		FilePath: path,
		ModTime:  info.ModTime(),
	}
	var aiTitle, summary, firstUser string

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for n := 0; sc.Scan() && n < maxScanLines; n++ {
		var r rawLine
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		switch r.Type {
		case "user", "assistant":
			s.MsgCount++
			if r.CWD != "" {
				s.CWD = r.CWD
			}
			if r.GitBranch != "" {
				s.GitBranch = r.GitBranch
			}
			if r.Type == "user" && firstUser == "" && r.Message != nil {
				txt := oneLine(contentText(r.Message.Content))
				if txt != "" && !cmdWrapper.MatchString(txt) {
					firstUser = txt
				}
			}
		case "ai-title":
			aiTitle = r.AITitle
		case "summary":
			summary = r.Summary
		case "last-prompt":
			s.LastPrompt = r.LastPrompt
		}
	}

	switch {
	case aiTitle != "":
		s.Title = aiTitle
	case summary != "":
		s.Title = summary
	case firstUser != "":
		s.Title = firstUser
	case s.LastPrompt != "":
		s.Title = s.LastPrompt
	}
	s.Title = oneLine(s.Title)
	s.LastPrompt = oneLine(s.LastPrompt)
	return s, nil
}

// oneLine collapses all runs of whitespace (including newlines) into single
// spaces so a pasted multi-line prompt can't spill across TUI rows.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
