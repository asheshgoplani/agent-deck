// Queued start: a session queued at its group's max_concurrent cap starts the
// way a direct `agent-deck launch` would once a slot frees up. It gets the
// same Claude folder-trust seed, and the -m message it was queued with goes
// to the durable send queue before the spawn (core registers the sink), to be
// typed once the agent is idle. A queued session started any other way hands
// its message off the same way. The send worker applies the cost-budget gate
// when it types, as for any queued send; launch and session start check it
// before queueing too.
//
// The message is persisted in the tool_data extras zone ("queued_message",
// registered in statedb.toolDataBlob so a clear is not resurrected by
// MergeToolDataExtras), because the drain usually runs in another process
// (`session stop`, the daemon) than the launch that queued it.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/mattn/go-runewidth"
)

const toolDataQueuedMessageKey = "queued_message"

// WriteQueuedMessageToToolData merges the pending queued message into the
// tool_data blob, removing the key when msg is empty.
func WriteQueuedMessageToToolData(td json.RawMessage, msg string) json.RawMessage {
	return writeToolDataString(td, toolDataQueuedMessageKey, msg)
}

// ReadQueuedMessageFromToolData extracts the pending queued message. Returns
// "" for missing, malformed or legacy rows.
func ReadQueuedMessageFromToolData(td json.RawMessage) string {
	if len(td) == 0 {
		return ""
	}
	var blob struct {
		QueuedMessage string `json:"queued_message"`
	}
	_ = json.Unmarshal(td, &blob)
	return blob.QueuedMessage
}

// MarkQueued queues the instance for group capacity and records message as
// the initial prompt its eventual queued start delivers. An empty message
// clears any earlier one.
func (i *Instance) MarkQueued(message string) {
	i.Status = StatusQueued
	i.QueuedMessage = message
}

// queuedSpawn starts a drained instance; tests swap it to observe the call
// without spawning a real agent. StartQueued passes no message: the queued
// message goes through the send queue, never a ready-wait under the lock.
var queuedSpawn = func(i *Instance, message string) error {
	if message != "" {
		return i.StartWithMessage(message)
	}
	return i.Start()
}

// SetQueuedSpawnForTest replaces the spawn StartQueued uses and returns a
// restore func.
func SetQueuedSpawnForTest(fn func(i *Instance, message string) error) func() {
	prev := queuedSpawn
	queuedSpawn = fn
	return func() { queuedSpawn = prev }
}

// QueuedStart is the outcome of StartQueued.
type QueuedStart struct {
	// Started reports that this caller claimed the queued row and spawned it.
	// False with a nil error means another drainer claimed it first.
	Started bool
	// Warning says what happened to the queued -m message when it could not
	// simply be delivered (held for a session that failed to start, or not
	// queued at all), or that the final status could not be saved.
	Warning string
}

// queuedMessageSink puts a queued -m message on the durable send queue of the
// store the session lives in (profile is that store's profile, profileDir the
// directory holding its state.db) and starts the target's send worker. The
// sendqueue package imports session, so core registers it
// (core.EnqueueQueuedStartMessage), together with queuedMessageHold, how long
// the queued send waits for a session that is not running.
var (
	queuedMessageSink func(profile, profileDir string, inst *Instance, message string) error
	queuedMessageHold time.Duration
)

// SetQueuedMessageSink registers the sink and its hold window and returns a
// func restoring the previous ones.
func SetQueuedMessageSink(fn func(profile, profileDir string, inst *Instance, message string) error, hold time.Duration) func() {
	prevFn, prevHold := queuedMessageSink, queuedMessageHold
	queuedMessageSink, queuedMessageHold = fn, hold
	return func() { queuedMessageSink, queuedMessageHold = prevFn, prevHold }
}

// deliverQueuedMessage hands a message already taken off the queue
// (StateDB.TakeQueued) to the send queue. It returns whether it was queued
// and, when it was not, a warning. The message is never put back on the row.
func deliverQueuedMessage(profile, profileDir string, inst *Instance, message string) (queued bool, warning string) {
	err := fmt.Errorf("no send queue")
	if queuedMessageSink != nil && profileDir != "" {
		err = queuedMessageSink(profile, profileDir, inst, message)
	}
	if err != nil {
		return false, fmt.Sprintf("queued -m message NOT delivered for %s: %s (%v)", queuedMessageLabel(inst), queuedMessagePreview(message), err)
	}
	return true, ""
}

// owningStore is the profile and state.db directory of the store the
// instance was loaded from or saved to, or "" when it has none. The profile
// is the store's own, never derived from the directory name.
func (i *Instance) owningStore() (profile, dir string) {
	if snap := i.storageSnapshot; snap != nil && snap.dbPath != "" {
		return snap.profile, filepath.Dir(snap.dbPath)
	}
	return "", ""
}

// leftQueue reports whether the instance is queued or still holds a queued
// message: a start or restart then takes it off the queue.
func (i *Instance) leftQueue() bool {
	return i.Status == StatusQueued || i.QueuedMessage != ""
}

// takeOffQueueAfterStart is the start and restart side of the rule every path
// off the queue follows: once the spawn succeeded, take the session off the
// queue in one row write (status and queued message together,
// StateDB.TakeQueued), and only then hand the message to the send queue. A
// failed start leaves the row queued with its message, for a later start or
// drain to deliver once.
func (i *Instance) takeOffQueueAfterStart() {
	profile, dir := i.owningStore()
	if dir == "" {
		// Not loaded from or saved to a store: there is no row to take it
		// off and no send queue to hand it to. It stays on the instance.
		fmt.Fprintf(os.Stderr, "Warning: the queued -m message of %s stays queued: the session has no store\n", queuedMessageLabel(i))
		return
	}
	var message string
	var taken bool
	err := i.withOwningStateDB(func(db *statedb.StateDB) error {
		var status string
		var err error
		message, status, taken, err = db.TakeQueued(i.ID, i.Tool, false)
		if taken {
			i.noteTakenOffQueue(status)
		}
		return err
	})
	if err != nil {
		// The row keeps it; so does the instance, or its next save would
		// delete it from the row.
		fmt.Fprintf(os.Stderr, "Warning: the queued -m message of %s stays queued: taking it off the queue failed: %v\n", queuedMessageLabel(i), err)
		return
	}
	if !taken {
		return // no row (yet), or nothing left to take: keep what we have
	}
	i.QueuedMessage = ""
	if message == "" {
		return
	}
	if _, warning := deliverQueuedMessage(profile, dir, i, message); warning != "" {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", warning)
	}
}

// noteTakenOffQueue records what TakeQueued committed (status, and the
// queued message gone from tool_data) in the instance's storage baseline.
// Without it a later save of the instance reads the take as another
// process's write, and the snapshot merge keeps the take's "starting" over
// the status the save carries.
func (i *Instance) noteTakenOffQueue(status string) {
	snap := i.storageSnapshot
	if snap == nil {
		return
	}
	// Copy-on-write: a save in flight holds the current rows as its merge
	// baseline and must not see them change under it.
	next := &instanceStorageSnapshot{dbPath: snap.dbPath, profile: snap.profile,
		original: statedb.CloneInstanceRow(snap.original), stored: statedb.CloneInstanceRow(snap.stored)}
	for _, row := range []*statedb.InstanceRow{next.original, next.stored} {
		if row != nil {
			row.Status = status
			row.ToolData = WriteQueuedMessageToToolData(row.ToolData, "")
		}
	}
	i.storageSnapshot = next
}

func queuedMessageLabel(i *Instance) string {
	return fmt.Sprintf("%s (%s)", i.Title, i.ID)
}

// queuedMessagePreview is the message's first line, capped for a warning.
func queuedMessagePreview(message string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(message), "\n")
	if runewidth.StringWidth(line) > 80 {
		line = runewidth.Truncate(line, 80, "...")
	}
	return strconv.Quote(line)
}

// StartQueued starts inst, drained from its group queue, the way a direct
// launch would: it seeds Claude's folder trust and spawns the agent. It does
// not wait for the agent to be ready, since the stop that drains holds the
// profile mutation lock.
//
// The row is claimed and its queued message taken in one write
// (StateDB.TakeQueued with claim): of two drainers, or a drainer and a direct
// start, only one takes it. The message then goes to the send queue before
// the spawn, so a failed spawn leaves it waiting for the session. Only inst's
// row is written, never the full list.
//
// At most once, by design: the message leaves the row in the claim, so a crash
// or Ctrl-C between the claim and the enqueue loses it, and nothing sends it
// twice.
func (s *Storage) StartQueued(inst *Instance) (QueuedStart, error) {
	db := s.GetDB()
	if db == nil {
		return QueuedStart{}, fmt.Errorf("storage database not initialized")
	}
	message, status, claimed, err := db.TakeQueued(inst.ID, inst.Tool, true)
	if err != nil {
		return QueuedStart{}, fmt.Errorf("claiming queued start of %s: %w", inst.Title, err)
	}
	if !claimed {
		return QueuedStart{}, nil
	}
	inst.noteTakenOffQueue(status)
	inst.Status = StatusStarting
	inst.QueuedMessage = ""
	out, queued := QueuedStart{}, false
	if message != "" {
		queued, out.Warning = deliverQueuedMessage(s.Profile(), filepath.Dir(s.Path()), inst, message)
	}
	PreAcceptLaunchTrust(inst)
	spawnErr := queuedSpawn(inst, "")
	if spawnErr != nil {
		inst.Status = StatusError
	}
	saveErr := s.SaveWithGroups([]*Instance{inst}, nil)
	if spawnErr != nil {
		if queued {
			out.Warning = fmt.Sprintf("queued -m message held for %s: delivered if it starts within %s, then dropped: %s", queuedMessageLabel(inst), queuedMessageHold, queuedMessagePreview(message))
		}
		return out, errors.Join(spawnErr, saveErr)
	}
	out.Started = true
	if saveErr != nil {
		w := fmt.Sprintf("started queued session %s but could not save its status: %v", inst.Title, saveErr)
		if out.Warning != "" {
			w = out.Warning + "; " + w
		}
		out.Warning = w
	}
	return out, nil
}

// PreAcceptLaunchTrust seeds Claude Code's per-directory trust flag
// (projects[dir].hasTrustDialogAccepted) before the launch spawn, for
// claude-tool instances only — no other tool shows this prompt. Keyed by
// realpath, matching the loadout trust seed (#1149), since Claude resolves
// the cwd through symlinks. Best-effort: a failure is reported to stderr and
// never blocks launch, matching every other PreAcceptClaudeTrust call site.
//
// The flag is written to the .claude.json Claude will actually read. With
// an explicit config root (-account, conductor, group, env or profile
// config_dir) the session runs with CLAUDE_CONFIG_DIR=<dir> and reads
// <dir>/.claude.json, so seeding only ~/.claude.json left it stuck on the
// trust prompt (#2673). The home root is still seeded too, as before.
func PreAcceptLaunchTrust(inst *Instance) {
	if inst.Tool != "claude" {
		return
	}
	trustDir := inst.EffectiveWorkingDir()
	if real, err := filepath.EvalSymlinks(trustDir); err == nil {
		trustDir = real
	}
	targets := []string{GetUserMCPRootPath()}
	if IsClaudeConfigDirExplicitForInstance(inst) {
		if dir := GetClaudeConfigDirForInstance(inst); dir != "" {
			if p := filepath.Join(dir, ".claude.json"); p != targets[0] {
				targets = append(targets, p)
			}
		}
	}
	for _, claudeJSON := range targets {
		if claudeJSON == "" {
			continue
		}
		if err := PreAcceptClaudeTrust(claudeJSON, trustDir); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: folder trust pre-seed for %q in %s failed (Claude launch may stall on the trust prompt): %v\n", trustDir, claudeJSON, err)
		}
	}
}
