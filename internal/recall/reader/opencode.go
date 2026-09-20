package reader

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall"
)

// OpenCode reads OpenCode's JSON storage tree: <data>/opencode/storage/
// session/<projectID>/<ses_id>.json, message/<ses_id>/<msg_id>.json and
// part/<msg_id>/<prt_id>.json. On the design machine the SQLite tables are
// empty and this tree is what is live; snapshot/ (820 of 822 MB, a git
// object store for checkpoints) is never entered. One source is one
// session: its size is the byte total of the session, message and part
// files and its mtime the newest of them, so any change to the tree is a
// full reparse of that session (CursorNone).
type OpenCode struct{}

// HarnessOpenCode is the harness name stored on every OpenCode row.
const HarnessOpenCode = "opencode"

func (OpenCode) Harness() string { return HarnessOpenCode }

// Cursor: a session tree, reparsed whole.
func (OpenCode) Cursor() CursorKind { return CursorNone }

// opencodeLayout: <storage>/session/<projectID>/<ses_id>.json, each sized
// with its message and part files; the resolved storage dir rides along
// as Aux.
var opencodeLayout = layout{
	harness: HarnessOpenCode,
	subdirs: []string{"session"},
	keep:    isJSON,
	ident: func(ref *SourceRef, storage string) bool {
		ref.NativeID = strings.TrimSuffix(filepath.Base(ref.Path), ".json")
		ref.Aux = storage
		size, mtime := opencodeTreeStat(storage, ref.NativeID)
		ref.Size += size
		ref.MtimeNS = max(ref.MtimeNS, mtime)
		return true
	},
}

// Discover lists every session json under storage/session and sizes its
// message and part files.
func (OpenCode) Discover(ctx context.Context, roots []Root, emit func(SourceRef) error) error {
	return opencodeLayout.discover(ctx, roots, emit)
}

// CheckRoots reports every OpenCode storage dir whose session/ tree
// exists but could not be listed.
func (OpenCode) CheckRoots(roots []Root) []RootIssue {
	return opencodeLayout.checkRoots(roots)
}

func isJSON(name string) bool { return filepath.Ext(name) == ".json" }

// opencodeTreeStat sums the message and part files of one session.
func opencodeTreeStat(storage, sessID string) (size, mtimeNS int64) {
	add := func(info os.FileInfo) {
		size += info.Size()
		mtimeNS = max(mtimeNS, info.ModTime().UnixNano())
	}
	msgs, _ := fsReadDir(filepath.Join(storage, "message", sessID))
	for _, d := range msgs {
		if d.IsDir() || !isJSON(d.Name()) {
			continue
		}
		if info, err := entryInfo(d); err == nil {
			add(info)
		}
		parts, _ := fsReadDir(filepath.Join(storage, "part", strings.TrimSuffix(d.Name(), ".json")))
		for _, p := range parts {
			if p.IsDir() || !isJSON(p.Name()) {
				continue
			}
			if info, err := entryInfo(p); err == nil {
				add(info)
			}
		}
	}
	return size, mtimeNS
}

type opencodeSession struct {
	ID        string `json:"id"`
	ParentID  string `json:"parentID"`
	Directory string `json:"directory"`
	Title     string `json:"title"`
	Version   string `json:"version"`
	Time      struct {
		Created int64 `json:"created"`
		Updated int64 `json:"updated"`
	} `json:"time"`
}

type opencodeMessage struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	ModelID string `json:"modelID"`
	Model   struct {
		ModelID string `json:"modelID"`
	} `json:"model"`
	Time struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
	Path struct {
		CWD string `json:"cwd"`
	} `json:"path"`
	Tokens struct {
		Input  int64 `json:"input"`
		Output int64 `json:"output"`
		Cache  struct {
			Read  int64 `json:"read"`
			Write int64 `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
}

type opencodePart struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Text  string `json:"text"`
	Tool  string `json:"tool"`
	State struct {
		Status string          `json:"status"`
		Input  json.RawMessage `json:"input"`
		Time   struct {
			Start int64 `json:"start"`
			End   int64 `json:"end"`
		} `json:"time"`
	} `json:"state"`
}

// Ingest reads the session, then every message with its parts in creation
// order. from is ignored (CursorNone); the cursor returned is src.Size.
func (OpenCode) Ingest(ctx context.Context, src SourceRef, from int64, sink Sink, b *Budget) (int64, error) {
	var sess opencodeSession
	if err := readJSONFile(src.Path, b, &sess); err != nil {
		return 0, err
	}
	native := firstNonEmpty(sess.ID, src.NativeID)
	sink.Session(Session{NativeID: native, CWD: sess.Directory, Version: sess.Version, ForkOf: sess.ParentID})
	if sess.Title != "" {
		sink.Session(Session{Title: sess.Title, TitleSrc: "title"})
	}
	storage := src.Aux
	if storage == "" {
		storage = filepath.Dir(filepath.Dir(filepath.Dir(src.Path)))
	}
	msgDir := filepath.Join(storage, "message", native)
	entries, _ := fsReadDir(msgDir)
	var msgs []opencodeMessage
	for _, d := range entries {
		if d.IsDir() || !isJSON(d.Name()) {
			continue
		}
		var m opencodeMessage
		if err := readJSONFile(filepath.Join(msgDir, d.Name()), b, &m); err != nil {
			if err := countSkip(sink, err); err != nil {
				return 0, err
			}
			continue
		}
		if m.ID == "" {
			m.ID = strings.TrimSuffix(d.Name(), ".json")
		}
		msgs = append(msgs, m)
	}
	sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].Time.Created < msgs[j].Time.Created })
	model := ""
	for i := range msgs {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		m := &msgs[i]
		ts := m.Time.Created / 1000
		msg := Msg{TS: ts, UUID: m.ID}
		switch m.Role {
		case "user":
			msg.Role = recall.RoleUser
		case "assistant":
			msg.Role = recall.RoleAssistant
			if id := firstNonEmpty(m.ModelID, m.Model.ModelID); id != "" && id != model {
				model = id
				sink.Session(Session{Model: id})
			}
			if m.Tokens.Input+m.Tokens.Output > 0 {
				if err := sink.Usage(Usage{UUID: m.ID, TS: time.UnixMilli(m.Time.Created), Model: model, In: m.Tokens.Input, Out: m.Tokens.Output,
					CacheR: m.Tokens.Cache.Read, CacheW: m.Tokens.Cache.Write}); err != nil {
					return 0, err
				}
			}
		default:
			continue
		}
		if m.Path.CWD != "" && sess.Directory == "" {
			sink.Session(Session{CWD: m.Path.CWD})
		}
		text, err := opencodeParts(storage, m.ID, ts, &msg, sink, b)
		if err != nil {
			return 0, err
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		msg.Text = text
		if err := sink.Msg(msg); err != nil {
			return 0, err
		}
	}
	return src.Size, nil
}

// opencodeParts joins a message's text parts and emits its tool parts.
func opencodeParts(storage, msgID string, ts int64, msg *Msg, sink Sink, b *Budget) (string, error) {
	dir := filepath.Join(storage, "part", msgID)
	entries, _ := fsReadDir(dir)
	names := make([]string, 0, len(entries))
	for _, d := range entries {
		if !d.IsDir() && isJSON(d.Name()) {
			names = append(names, d.Name())
		}
	}
	sort.Strings(names) // part ids are time-ordered
	var sb strings.Builder
	for _, name := range names {
		var p opencodePart
		if err := readJSONFile(filepath.Join(dir, name), b, &p); err != nil {
			if err := countSkip(sink, err); err != nil {
				return "", err
			}
			continue
		}
		switch p.Type {
		case "text":
			joinText(&sb, p.Text)
		case "tool":
			msg.ToolNames = append(msg.ToolNames, p.Tool)
			digest, touches := digestArgs(p.Tool, p.State.Input)
			call := ToolCall{Name: p.Tool, TS: ts, IsError: p.State.Status == "error", ArgDigest: digest, Touches: touches}
			if p.State.Time.End > p.State.Time.Start && p.State.Time.Start > 0 {
				call.DurationMS = p.State.Time.End - p.State.Time.Start
			}
			if err := sink.ToolCall(call); err != nil {
				return "", err
			}
		}
	}
	return sb.String(), nil
}

// readJSONFile decodes one small JSON file, charging its size to the budget.
func readJSONFile(path string, b *Budget, v any) error {
	f, err := fsOpen(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > MaxLineBytes {
		return errTooLong
	}
	if !b.Consume(info.Size()) {
		return ErrBudget
	}
	return json.NewDecoder(f).Decode(v)
}

// errTooLong is a record above MaxLineBytes: skipped and counted.
var errTooLong = errors.New("recall: record exceeds MaxLineBytes")

// countSkip tallies a skipped record on sink; budget exhaustion is not a
// skip and is returned to the caller.
func countSkip(sink Sink, err error) error {
	switch {
	case errors.Is(err, ErrBudget):
		return err
	case errors.Is(err, errTooLong):
		sink.Count(CountLineTooLong, 1)
	default:
		sink.Count(CountBadJSON, 1)
	}
	return nil
}
