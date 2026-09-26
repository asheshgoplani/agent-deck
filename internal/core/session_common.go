package core

import (
	"errors"
	"fmt"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Resolver finds one session by a user-typed identifier (title, id prefix,
// path or location). It returns the session, or nil plus a message and a
// stable code (CodeNotFound or CodeAmbiguous).
type Resolver func(identifier string, instances []*session.Instance) (inst *session.Instance, msg, code string)

// Deps are the collaborators the built-in commands need that do not live in
// internal/session yet. The CLI supplies its existing implementations; later
// slices move them into a shared package and this struct shrinks.
type Deps struct {
	// Resolve maps an identifier onto a session.
	Resolve Resolver
	// ApplyYolo applies a --yolo override to an instance before it starts.
	ApplyYolo func(inst *session.Instance, enabled bool) error
}

// SpawnVerifyWait bounds how long start/restart wait for a missing tmux
// session to appear or be explained by a spawn-failure record (#2099).
const SpawnVerifyWait = 2 * time.Second

// PostStartSyncWait bounds how long start/restart wait for the tool's session
// id after the process is spawned.
const PostStartSyncWait = 3 * time.Second

// sessionData is one loaded profile store.
type sessionData struct {
	storage   *session.Storage
	instances []*session.Instance
	groups    []*session.GroupData
}

// loadSessionData opens the profile store and loads every instance and group.
func loadSessionData(profile string) (*sessionData, error) {
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		return nil, &Error{Code: CodeStorage, Message: fmt.Sprintf("failed to initialize storage: %v", err), Cause: err}
	}
	instances, groups, err := storage.LoadWithGroups()
	if err != nil {
		return nil, &Error{Code: CodeStorage, Message: fmt.Sprintf("failed to load sessions: %v", err), Cause: err}
	}
	return &sessionData{storage: storage, instances: instances, groups: groups}, nil
}

// save persists every instance and the group tree, preserving stored group
// metadata (sort order).
func (d *sessionData) save() error {
	tree := session.NewGroupTreeWithGroups(d.instances, d.groups)
	return d.storage.SaveWithGroups(d.instances, tree)
}

// saveOr wraps a save failure as CodeInvalid with the given prefix.
func (d *sessionData) saveOr(prefix string) error {
	if err := d.save(); err != nil {
		return &Error{Code: CodeInvalid, Message: fmt.Sprintf("%s: %v", prefix, err), Cause: err}
	}
	return nil
}

func (deps Deps) resolve(identifier string, instances []*session.Instance) (*session.Instance, error) {
	if deps.Resolve == nil {
		return nil, Errorf(CodeInternal, "core: no session resolver configured")
	}
	inst, msg, code := deps.Resolve(identifier, instances)
	if inst == nil {
		if code == "" {
			code = CodeNotFound
		}
		return nil, &Error{Code: code, Message: msg}
	}
	return inst, nil
}

// SpawnFailure is the error data of a start/restart whose spawn could not be
// confirmed (#2099).
type SpawnFailure struct {
	ID     string                      `json:"id"`
	Title  string                      `json:"title"`
	Tmux   string                      `json:"tmux,omitempty"`
	Reason string                      `json:"reason"`
	Record *session.SpawnFailureRecord `json:"spawn_failure,omitempty"`
	// Verb is "start" or "restart".
	Verb string `json:"-"`
	// SaveErr is set when persisting the error status also failed.
	SaveErr error `json:"-"`
}

func newSpawnFailure(verb string, inst *session.Instance, err error) *SpawnFailure {
	sf := &SpawnFailure{ID: inst.ID, Title: inst.Title, Verb: verb}
	var spawnErr *session.SpawnFailedError
	if errors.As(err, &spawnErr) {
		sf.Tmux = spawnErr.TmuxName
		if spawnErr.Record != nil {
			sf.Reason = spawnErr.Record.Reason
			sf.Record = spawnErr.Record
		} else {
			sf.Reason = "tmux_session_missing"
		}
	} else {
		sf.Reason = "spawn_unverified"
	}
	return sf
}

func spawnFailureMessage(verb string, err error) string {
	return fmt.Sprintf("failed to %s session: %v", verb, err)
}

// failSpawn marks inst errored, persists it, and returns the spawn failure.
func (d *sessionData) failSpawn(verb string, inst *session.Instance, err error) error {
	inst.Status = session.StatusError
	sf := newSpawnFailure(verb, inst, err)
	sf.SaveErr = d.save()
	return &Error{Code: CodeSpawnFailed, Message: spawnFailureMessage(verb, err), Data: sf, Cause: err}
}
