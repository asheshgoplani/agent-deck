package source

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/history/model"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// safeID guards the session id that is later interpolated into a shell
// command and eval'd. Real Claude Code ids are UUIDs; this rejects any
// filename stem containing shell-dangerous characters.
var safeID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// recentThreshold: a session touched more recently than this (and not live in
// the registry) is shown as "recent".
const recentThreshold = 30 * time.Minute

type ClaudeCodeTool struct {
	root        string
	sessionsDir string
}

// NewClaudeCodeTool resolves the Claude projects + live-session directories
// via agent-deck's own config resolution (CLAUDE_CONFIG_DIR / account /
// group / profile / ~/.claude), not agenthop's AGENTHOP_* env vars.
// agent-hopdeck: replaces the ported env-var constructor.
func NewClaudeCodeTool() *ClaudeCodeTool {
	base := session.GetClaudeConfigDir()
	return &ClaudeCodeTool{
		root:        filepath.Join(base, "projects"),
		sessionsDir: filepath.Join(base, "sessions"),
	}
}

func (t *ClaudeCodeTool) Name() string { return "claude-code" }

func (t *ClaudeCodeTool) Command(s model.Session, fork bool) string {
	cmd := "claude --resume " + s.ID
	if fork {
		cmd += " --fork-session"
	}
	return cmd
}

func (t *ClaudeCodeTool) Delete(s model.Session) error {
	if s.FilePath == "" {
		return errors.New("session has no file path")
	}
	// remove the transcript and its sidecar dir (subagent transcripts).
	os.RemoveAll(strings.TrimSuffix(s.FilePath, ".jsonl"))
	return os.Remove(s.FilePath)
}

func (t *ClaudeCodeTool) Discover() ([]model.Project, error) {
	entries, err := os.ReadDir(t.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	byCWD := map[string]*model.Project{}
	for _, dir := range entries {
		if !dir.IsDir() {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(t.root, dir.Name(), "*.jsonl"))
		for _, f := range files {
			s, err := ParseSessionMeta(f)
			if err != nil || s.CWD == "" || !safeID.MatchString(s.ID) {
				continue
			}
			p := byCWD[s.CWD]
			if p == nil {
				p = &model.Project{Path: s.CWD, Name: filepath.Base(s.CWD), Tool: t.Name()}
				byCWD[s.CWD] = p
			}
			p.Sessions = append(p.Sessions, s)
		}
	}
	out := make([]model.Project, 0, len(byCWD))
	for _, p := range byCWD {
		sort.Slice(p.Sessions, func(i, j int) bool {
			return p.Sessions[i].ModTime.After(p.Sessions[j].ModTime)
		})
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })

	reg, _ := ReadRegistry(t.sessionsDir)
	applyStatus(out, reg, time.Now())
	return out, nil
}

func statusFor(s model.Session, reg map[string]Live, now time.Time) model.SessionStatus {
	if l, ok := reg[s.ID]; ok {
		switch l.RawStatus {
		case "waiting":
			return model.StatusWaiting
		case "busy":
			return model.StatusRunningBusy
		default: // idle, shell
			return model.StatusRunningIdle
		}
	}
	if now.Sub(s.ModTime) < recentThreshold {
		return model.StatusRecent
	}
	return model.StatusClosed
}

func applyStatus(projects []model.Project, reg map[string]Live, now time.Time) {
	for i := range projects {
		for j := range projects[i].Sessions {
			s := &projects[i].Sessions[j]
			s.Status = statusFor(*s, reg, now)
			if l, ok := reg[s.ID]; ok {
				s.PID, s.Name, s.WaitingFor = l.PID, l.Name, l.WaitingFor
			} else {
				s.PID = 0
				s.WaitingFor = ""
			}
		}
	}
}

// RefreshStatus re-reads the live-session registry and updates statuses in
// place, without re-parsing session files.
func (t *ClaudeCodeTool) RefreshStatus(projects []model.Project) {
	reg, _ := ReadRegistry(t.sessionsDir)
	applyStatus(projects, reg, time.Now())
}
