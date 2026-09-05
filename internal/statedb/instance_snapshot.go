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

// CloneInstanceRow returns an owned snapshot, including the mutable JSON bytes.
func CloneInstanceRow(row *InstanceRow) *InstanceRow {
	if row == nil {
		return nil
	}
	copy := *row
	copy.ToolData = append(json.RawMessage(nil), row.ToolData...)
	return &copy
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
	defer tx.Rollback()
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
	keys := make(map[string]bool, len(original)+len(desired))
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
