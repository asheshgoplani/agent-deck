package statedb

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"time"
)

// InstanceSnapshot separates the caller's last submitted representation from
// the row it observed. Load-time normalization and omitted ToolData extras must
// not look like edits. Nil Stored and Original mean an insertion, never an
// unconditional replacement of an existing ID.
type InstanceSnapshot struct {
	Original *InstanceRow
	Stored   *InstanceRow
	Desired  *InstanceRow
}

// NativeHarnessSwitchIdentity is the deliberately small compare-and-swap
// boundary for a same-harness lifecycle switch. Status and display metadata
// are intentionally absent: monitors may update them while the replacement
// native process comes up. Account, command, path, tool, and native IDs instead
// bind the operation to the exact source that was stopped.
type NativeHarnessSwitchIdentity struct {
	ID              string
	Tool            string
	Account         string
	ProjectPath     string
	Command         string
	ClaudeSessionID string
	CodexSessionID  string
	// ParentSessionID binds a cross-harness replacement to the routing that
	// existed when its source and pending target were selected. It is not part
	// of same-harness identity because that operation never replaces a row.
	ParentSessionID string
}

// CloneInstanceRow returns an owned snapshot, including the mutable JSON bytes.
func CloneInstanceRow(row *InstanceRow) *InstanceRow {
	if row == nil {
		return nil
	}
	copy := *row
	copy.ToolData = append(json.RawMessage(nil), row.ToolData...)
	return &copy
}

// CommitNativeHarnessSwitch atomically verifies the source identity then
// changes only the account, tool, and native session-ID fields. It never writes
// status or unrelated metadata from a stale process snapshot.
func (s *StateDB) CommitNativeHarnessSwitch(source, target NativeHarnessSwitchIdentity) (*InstanceRow, error) {
	if source.ID == "" || target.ID != source.ID || target.Tool == "" {
		return nil, fmt.Errorf("invalid native harness switch identity")
	}
	var committed *InstanceRow
	err := withBusyRetry(func() error {
		var err error
		committed, err = s.commitNativeHarnessSwitchOnce(source, target)
		return err
	})
	if err != nil {
		return nil, err
	}
	return committed, nil
}

// ConfirmNativeHarnessSwitchTarget confirms a previous native-switch CAS is
// already at its exact target without writing any lifecycle or registry fields.
// It is used only to durably acknowledge a journal after a process reloaded the
// target identity following an acknowledgement-write failure.
func (s *StateDB) ConfirmNativeHarnessSwitchTarget(target NativeHarnessSwitchIdentity) (*InstanceRow, error) {
	if target.ID == "" || target.Tool == "" {
		return nil, fmt.Errorf("invalid native harness switch target identity")
	}
	var confirmed *InstanceRow
	err := withBusyRetry(func() error {
		var err error
		confirmed, err = s.confirmNativeHarnessSwitchTargetOnce(target)
		return err
	})
	if err != nil {
		return nil, err
	}
	return confirmed, nil
}

func (s *StateDB) confirmNativeHarnessSwitchTargetOnce(target NativeHarnessSwitchIdentity) (*InstanceRow, error) {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, err
	}
	defer func() { _, _ = conn.ExecContext(ctx, "ROLLBACK") }()

	rows, err := loadInstances(func(query string, args ...any) (*sql.Rows, error) {
		return conn.QueryContext(ctx, query, args...)
	})
	if err != nil {
		return nil, fmt.Errorf("read native switch target: %w", err)
	}
	var current *InstanceRow
	for _, row := range rows {
		if row.ID == target.ID {
			current = row
			break
		}
	}
	if current == nil {
		return nil, fmt.Errorf("native switch target deletion conflict for instance %s", target.ID)
	}
	claudeID, codexID, err := nativeSessionIDs(current.ToolData)
	if err != nil {
		return nil, err
	}
	if current.Tool != target.Tool || current.Account != target.Account || current.ProjectPath != target.ProjectPath || current.Command != target.Command || claudeID != target.ClaudeSessionID || codexID != target.CodexSessionID {
		return nil, fmt.Errorf("native switch target identity conflict for instance %s", target.ID)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, err
	}
	return current, nil
}

func (s *StateDB) commitNativeHarnessSwitchOnce(source, target NativeHarnessSwitchIdentity) (*InstanceRow, error) {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, err
	}
	defer func() { _, _ = conn.ExecContext(ctx, "ROLLBACK") }()

	rows, err := loadInstances(func(query string, args ...any) (*sql.Rows, error) {
		return conn.QueryContext(ctx, query, args...)
	})
	if err != nil {
		return nil, fmt.Errorf("read native switch source: %w", err)
	}
	var current *InstanceRow
	for _, row := range rows {
		if row.ID == source.ID {
			current = row
			break
		}
	}
	if current == nil {
		return nil, fmt.Errorf("native switch source deletion conflict for instance %s", source.ID)
	}
	claudeID, codexID, err := nativeSessionIDs(current.ToolData)
	if err != nil {
		return nil, err
	}
	if current.Tool != source.Tool || current.Account != source.Account || current.ProjectPath != source.ProjectPath || current.Command != source.Command || claudeID != source.ClaudeSessionID || codexID != source.CodexSessionID {
		return nil, fmt.Errorf("native switch source identity conflict for instance %s", source.ID)
	}

	merged := CloneInstanceRow(current)
	merged.Tool, merged.Account = target.Tool, target.Account
	fields, err := toolDataFields(merged.ToolData)
	if err != nil {
		return nil, err
	}
	fields["claude_session_id"], err = json.Marshal(target.ClaudeSessionID)
	if err != nil {
		return nil, err
	}
	fields["codex_session_id"], err = json.Marshal(target.CodexSessionID)
	if err != nil {
		return nil, err
	}
	merged.ToolData, err = json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	if err := writeSnapshotRow(ctx, conn, merged, true); err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, err
	}
	return merged, nil
}

// CommitCrossHarnessSupersession atomically turns a ready, archived target
// into the only active row and archives its untouched source. Both rows retain
// their native identities and tool_data; reversible lineage is the only added
// metadata. The target is created archived before launch, so pending/failed
// transfers leave the original source visible.
func (s *StateDB) CommitCrossHarnessSupersession(source, target NativeHarnessSwitchIdentity, archivedAt time.Time) (sourceRow, targetRow *InstanceRow, err error) {
	if source.ID == "" || target.ID == "" || source.ID == target.ID || archivedAt.IsZero() {
		return nil, nil, fmt.Errorf("invalid cross-harness supersession identity")
	}
	err = withBusyRetry(func() error {
		ctx := context.Background()
		conn, openErr := s.db.Conn(ctx)
		if openErr != nil {
			return openErr
		}
		defer conn.Close()
		if _, beginErr := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); beginErr != nil {
			return beginErr
		}
		defer func() { _, _ = conn.ExecContext(ctx, "ROLLBACK") }()
		rows, loadErr := loadInstances(func(query string, args ...any) (*sql.Rows, error) { return conn.QueryContext(ctx, query, args...) })
		if loadErr != nil {
			return fmt.Errorf("read cross-harness supersession rows: %w", loadErr)
		}
		var currentSource, currentTarget *InstanceRow
		for _, row := range rows {
			switch row.ID {
			case source.ID:
				currentSource = row
			case target.ID:
				currentTarget = row
			}
		}
		if currentSource == nil || currentTarget == nil {
			return fmt.Errorf("cross-harness supersession row was deleted")
		}
		if crossHarnessLineage(currentSource.ToolData, "cross_harness_superseded_by") == target.ID &&
			crossHarnessLineage(currentTarget.ToolData, "cross_harness_supersedes") == source.ID &&
			!currentSource.ArchivedAt.IsZero() && currentTarget.ArchivedAt.IsZero() {
			sourceRow, targetRow = currentSource, currentTarget
			if _, commitErr := conn.ExecContext(ctx, "COMMIT"); commitErr != nil {
				return commitErr
			}
			return nil
		}
		// This is the authoritative source/child-state guard at the replacement
		// boundary. An external watcher route can still change after its last
		// read, but a new dependent or conductor role in the registry can never
		// be silently archived by this transaction. A source's own parent link is
		// intentionally not a blocker: ordinary workers retain that routing.
		if currentSource.IsConductor {
			return fmt.Errorf("cross-harness source ownership conflict for instance %s: source is a managed conductor", source.ID)
		}
		for _, row := range rows {
			if row != nil && row.ID != source.ID && row.ParentSessionID == source.ID {
				return fmt.Errorf("cross-harness source ownership conflict for instance %s: dependent child %s was added", source.ID, row.ID)
			}
		}
		claudeID, codexID, identityErr := nativeSessionIDs(currentSource.ToolData)
		if identityErr != nil {
			return identityErr
		}
		if currentSource.ParentSessionID != source.ParentSessionID {
			return fmt.Errorf("cross-harness source routing conflict for instance %s", source.ID)
		}
		if currentSource.ArchivedAt.IsZero() == false || currentSource.Tool != source.Tool || currentSource.Account != source.Account || currentSource.ProjectPath != source.ProjectPath || currentSource.Command != source.Command || claudeID != source.ClaudeSessionID || codexID != source.CodexSessionID {
			return fmt.Errorf("cross-harness source identity conflict for instance %s", source.ID)
		}
		targetClaudeID, targetCodexID, targetIdentityErr := nativeSessionIDs(currentTarget.ToolData)
		if targetIdentityErr != nil {
			return targetIdentityErr
		}
		targetLineage := crossHarnessLineage(currentTarget.ToolData, "cross_harness_supersedes")
		// A ready journal from before pending-target archival can legitimately
		// carry an active target. It is accepted only after recovery persisted the
		// exact source lineage; ordinary pending targets must still be archived.
		legacyReadyTarget := currentTarget.ArchivedAt.IsZero() && currentSource.ArchivedAt.IsZero() && targetLineage == source.ID
		if currentTarget.ParentSessionID != target.ParentSessionID {
			return fmt.Errorf("cross-harness target routing conflict for instance %s", target.ID)
		}
		if (!legacyReadyTarget && currentTarget.ArchivedAt.IsZero()) || currentTarget.Tool != target.Tool || currentTarget.Account != target.Account || currentTarget.ProjectPath != target.ProjectPath || currentTarget.Command != target.Command || targetClaudeID != target.ClaudeSessionID || targetCodexID != target.CodexSessionID || (targetLineage != "" && targetLineage != source.ID) {
			return fmt.Errorf("cross-harness target identity conflict for instance %s", target.ID)
		}
		sourceRow, targetRow = CloneInstanceRow(currentSource), CloneInstanceRow(currentTarget)
		sourceRow.ArchivedAt = archivedAt.UTC()
		targetRow.ArchivedAt = time.Time{}
		// The destination replaces the visible slot and its parent routing, not
		// the native identity. The parent project path is denormalized at the
		// session layer from this durable parent ID.
		targetRow.Title, targetRow.GroupPath = sourceRow.Title, sourceRow.GroupPath
		targetRow.Order, targetRow.CreatedAt = sourceRow.Order, sourceRow.CreatedAt
		targetRow.ParentSessionID = sourceRow.ParentSessionID
		sourceRow.ToolData = withCrossHarnessLineage(sourceRow.ToolData, "cross_harness_superseded_by", target.ID)
		targetRow.ToolData = withCrossHarnessLineage(targetRow.ToolData, "cross_harness_supersedes", source.ID)
		if writeErr := writeSnapshotRow(ctx, conn, sourceRow, true); writeErr != nil {
			return writeErr
		}
		if writeErr := writeSnapshotRow(ctx, conn, targetRow, true); writeErr != nil {
			return writeErr
		}
		if _, commitErr := conn.ExecContext(ctx, "COMMIT"); commitErr != nil {
			return commitErr
		}
		return nil
	})
	return sourceRow, targetRow, err
}

func crossHarnessLineage(data json.RawMessage, key string) string {
	fields, err := toolDataFields(data)
	if err != nil {
		return ""
	}
	var value string
	_ = json.Unmarshal(fields[key], &value)
	return value
}

func withCrossHarnessLineage(data json.RawMessage, key, value string) json.RawMessage {
	fields, err := toolDataFields(data)
	if err != nil {
		fields = map[string]json.RawMessage{}
	}
	encoded, _ := json.Marshal(value)
	fields[key] = encoded
	encoded, err = json.Marshal(fields)
	if err != nil {
		return data
	}
	return encoded
}

func nativeSessionIDs(data json.RawMessage) (claudeID, codexID string, err error) {
	fields, err := toolDataFields(data)
	if err != nil {
		return "", "", err
	}
	for key, destination := range map[string]*string{"claude_session_id": &claudeID, "codex_session_id": &codexID} {
		value, ok := fields[key]
		if !ok || len(value) == 0 || string(value) == "null" {
			continue
		}
		if err := json.Unmarshal(value, destination); err != nil {
			return "", "", fmt.Errorf("invalid native session identity %s", key)
		}
	}
	return claudeID, codexID, nil
}

// MergeInstanceSnapshots reserves the SQLite writer before reading current
// values, then compares and writes within that reservation. Direct SQL UPDATEs
// and DELETEs obey the same SQLite lock, including writers in other processes.
// Every retry repeats the whole decision; no precomputed merge is replayed.
// Rows absent from updates are never swept. Group rows here are insertions;
// callers editing existing groups must supply baselines to MergeRegistrySnapshots.
func (s *StateDB) MergeInstanceSnapshots(updates []InstanceSnapshot, groups []*GroupRow) ([]*InstanceRow, error) {
	groupUpdates := make([]GroupSnapshot, len(groups))
	for i, group := range groups {
		groupUpdates[i] = GroupSnapshot{Desired: group}
	}
	result, err := s.MergeRegistrySnapshots(updates, groupUpdates)
	if err != nil {
		return nil, err
	}
	return result.Instances, nil
}

// LoadRegistrySnapshot reads both tables from one SQLite snapshot. A concurrent
// group rename cannot produce old members paired with new group metadata.
func (s *StateDB) LoadRegistrySnapshot() (*RegistrySnapshotResult, error) {
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	instances, err := loadInstances(tx.Query)
	if err != nil {
		return nil, fmt.Errorf("failed to load instances: %w", err)
	}
	groups, err := loadGroups(tx.Query)
	if err != nil {
		return nil, fmt.Errorf("failed to load groups: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &RegistrySnapshotResult{Instances: instances, Groups: groups}, nil
}

// MergeRegistrySnapshots commits instance and group snapshots together. Both
// returned slices preserve input order and are available only after COMMIT.
func (s *StateDB) MergeRegistrySnapshots(updates []InstanceSnapshot, groups []GroupSnapshot) (*RegistrySnapshotResult, error) {
	var committed *RegistrySnapshotResult
	err := withBusyRetry(func() error {
		var err error
		committed, err = s.mergeRegistrySnapshotsOnce(updates, groups)
		return err
	})
	if err != nil {
		return nil, err
	}
	return committed, nil
}

func (s *StateDB) mergeRegistrySnapshotsOnce(updates []InstanceSnapshot, groups []GroupSnapshot) (*RegistrySnapshotResult, error) {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, err
	}
	defer func() { _, _ = conn.ExecContext(ctx, "ROLLBACK") }()
	current, err := loadInstances(func(query string, args ...any) (*sql.Rows, error) {
		return conn.QueryContext(ctx, query, args...)
	})
	if err != nil {
		return nil, fmt.Errorf("read current instances: %w", err)
	}
	byID := make(map[string]*InstanceRow, len(current))
	for _, row := range current {
		byID[row.ID] = row
	}
	merged := make([]*InstanceRow, len(updates))
	seen := make(map[string]bool, len(updates))
	for i, update := range updates {
		if update.Desired == nil || update.Desired.ID == "" {
			return nil, fmt.Errorf("instance snapshot requires an ID")
		}
		id := update.Desired.ID
		if seen[id] {
			return nil, fmt.Errorf("duplicate instance snapshot: %s", id)
		}
		seen[id] = true
		merged[i], err = mergeInstanceSnapshot(update, byID[id])
		if err != nil {
			return nil, err
		}
	}
	currentGroups, err := loadGroups(func(query string, args ...any) (*sql.Rows, error) {
		return conn.QueryContext(ctx, query, args...)
	})
	if err != nil {
		return nil, fmt.Errorf("read current groups: %w", err)
	}
	mergedGroups, removedGroups, err := mergeGroupSnapshots(groups, currentGroups, current, merged)
	if err != nil {
		return nil, err
	}
	for _, path := range removedGroups {
		if _, err := conn.ExecContext(ctx, "DELETE FROM groups WHERE path = ?", path); err != nil {
			return nil, err
		}
	}
	for _, row := range merged {
		if err := writeSnapshotRow(ctx, conn, row, byID[row.ID] != nil); err != nil {
			return nil, err
		}
	}
	for _, group := range mergedGroups {
		if group == nil {
			continue
		}
		if _, err := conn.ExecContext(ctx, upsertGroupSQL, group.Path, group.Name, group.Expanded, group.Order, group.DefaultPath, group.MaxConcurrent); err != nil {
			return nil, fmt.Errorf("save group: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, err
	}
	return &RegistrySnapshotResult{Instances: merged, Groups: mergedGroups}, nil
}

func mergeInstanceSnapshot(update InstanceSnapshot, current *InstanceRow) (*InstanceRow, error) {
	id := update.Desired.ID
	if update.Stored == nil {
		if update.Original != nil || current != nil {
			return nil, fmt.Errorf("concurrent insertion conflict for instance %s", id)
		}
		row := CloneInstanceRow(update.Desired)
		if _, err := toolDataFields(row.ToolData); err != nil {
			return nil, err
		}
		return row, nil
	}
	if update.Original == nil || update.Stored.ID != id || update.Original.ID != id {
		return nil, fmt.Errorf("invalid snapshot identity for instance %s", id)
	}
	if current == nil {
		return nil, fmt.Errorf("stale concurrent deletion conflict for instance %s", id)
	}
	merged := CloneInstanceRow(current)
	// InstanceRow consists of scalar persisted columns plus ToolData. Iterating
	// the row type keeps newly added columns in the same conflict contract.
	want, original := reflect.ValueOf(update.Desired).Elem(), reflect.ValueOf(update.Original).Elem()
	stored, actual := reflect.ValueOf(update.Stored).Elem(), reflect.ValueOf(current).Elem()
	out := reflect.ValueOf(merged).Elem()
	for i := 0; i < want.NumField(); i++ {
		name := want.Type().Field(i).Name
		if name == "ToolData" {
			continue
		}
		if snapshotValueEqual(want.Field(i).Interface(), original.Field(i).Interface()) {
			continue
		}
		if !snapshotValueEqual(actual.Field(i).Interface(), stored.Field(i).Interface()) && !snapshotValueEqual(actual.Field(i).Interface(), want.Field(i).Interface()) {
			return nil, fmt.Errorf("stale concurrent %s conflict for instance %s", name, id)
		}
		out.Field(i).Set(want.Field(i))
	}
	var err error
	merged.ToolData, err = mergeSnapshotToolData(update, current)
	return merged, err
}

func snapshotValueEqual(a, b any) bool {
	if timestamp, ok := a.(time.Time); ok {
		// SQLite persists seconds, with no monotonic clock or location identity.
		return timestamp.Unix() == b.(time.Time).Unix()
	}
	return reflect.DeepEqual(a, b)
}

func toolDataFields(data json.RawMessage) (map[string]json.RawMessage, error) {
	fields := make(map[string]json.RawMessage)
	if len(data) == 0 {
		return fields, nil
	}
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("invalid instance tool_data: expected JSON object")
	}
	return fields, nil
}

func sameJSON(a, b json.RawMessage) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	var av, bv any
	// The enclosing object was validated when building the maps.
	ad, bd := json.NewDecoder(bytes.NewReader(a)), json.NewDecoder(bytes.NewReader(b))
	ad.UseNumber()
	bd.UseNumber()
	_ = ad.Decode(&av)
	_ = bd.Decode(&bv)
	return reflect.DeepEqual(av, bv)
}

func sameStoredToolValue(key string, a, b json.RawMessage) bool {
	if sameJSON(a, b) {
		return true
	}
	// Targeted identity clears use json_remove; full saves carry explicit
	// empty/zero markers. Those are the same committed clear, including when
	// this caller's write-through already removed the key before the full save.
	if stickyToolDataKeys()[key] {
		isClear := func(value json.RawMessage) bool {
			return value == nil || sameJSON(value, json.RawMessage(`""`)) || sameJSON(value, json.RawMessage(`0`))
		}
		return isClear(a) && isClear(b)
	}
	return false
}

func mergeSnapshotToolData(update InstanceSnapshot, current *InstanceRow) (json.RawMessage, error) {
	original, err := toolDataFields(update.Original.ToolData)
	if err != nil {
		return nil, err
	}
	// Keep the existing omission/explicit-empty protocol for sticky identity
	// and unknown extras, but derive intent from the caller's own snapshot.
	desired, err := toolDataFields(MergeToolDataExtras(update.Original.ToolData, update.Desired.ToolData))
	if err != nil {
		return nil, err
	}
	stored, err := toolDataFields(update.Stored.ToolData)
	if err != nil {
		return nil, err
	}
	actual, err := toolDataFields(current.ToolData)
	if err != nil {
		return nil, err
	}
	keys := make(map[string]bool, len(original))
	for key := range original {
		keys[key] = true
	}
	for key := range desired {
		keys[key] = true
	}
	for key := range keys {
		if sameJSON(original[key], desired[key]) {
			continue
		}
		if !sameStoredToolValue(key, actual[key], stored[key]) && !sameStoredToolValue(key, actual[key], desired[key]) {
			return nil, fmt.Errorf("stale concurrent tool_data.%s conflict for instance %s", key, current.ID)
		}
		if value, exists := desired[key]; exists {
			actual[key] = value
		} else {
			delete(actual, key)
		}
	}
	return json.Marshal(actual)
}

func writeSnapshotRow(ctx context.Context, conn *sql.Conn, row *InstanceRow, exists bool) error {
	data := row.ToolData
	if len(data) == 0 {
		data = json.RawMessage("{}")
	}
	args := []any{row.Title, row.ProjectPath, row.GroupPath, row.Order,
		row.Command, row.Wrapper, row.Tool, row.Status, row.TmuxSession, row.TmuxSocketName,
		row.CreatedAt.Unix(), row.LastAccessed.Unix(), row.ParentSessionID, row.IsConductor, row.NoTransitionNotify,
		row.WorktreePath, row.WorktreeRepo, row.WorktreeBranch, row.Account, archivedAtUnix(row.ArchivedAt),
		string(data), row.TitleLocked, row.AutoName, row.AutoNameDescription, row.Pin, row.ID}
	// UPDATE preserves columns not represented by InstanceRow, such as the
	// notification acknowledgement. REPLACE would reset their defaults.
	query := `UPDATE instances SET title=?, project_path=?, group_path=?, sort_order=?,
		command=?, wrapper=?, tool=?, status=?, tmux_session=?, tmux_socket_name=?,
		created_at=?, last_accessed=?, parent_session_id=?, is_conductor=?, no_transition_notify=?,
		worktree_path=?, worktree_repo=?, worktree_branch=?, account=?, archived_at=?,
		tool_data=?, title_locked=?, auto_name=?, auto_name_description=?, pin=? WHERE id=?`
	if !exists {
		query = `INSERT INTO instances (title, project_path, group_path, sort_order,
			command, wrapper, tool, status, tmux_session, tmux_socket_name,
			created_at, last_accessed, parent_session_id, is_conductor, no_transition_notify,
			worktree_path, worktree_repo, worktree_branch, account, archived_at,
			tool_data, title_locked, auto_name, auto_name_description, pin, id)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	}
	_, err := conn.ExecContext(ctx, query, args...)
	return err
}
