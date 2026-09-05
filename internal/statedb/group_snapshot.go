package statedb

import (
	"fmt"
	"reflect"
	"strings"
)

// GroupSnapshot carries a metadata edit, a path move, or an explicit deletion
// (nil Desired). Ensure permits unchanged groups derived from session paths to
// use existing metadata, rather than replacing it with synthesized defaults.
type GroupSnapshot struct {
	Original *GroupRow
	Stored   *GroupRow
	Desired  *GroupRow
	Ensure   bool
}

func CloneGroupRow(row *GroupRow) *GroupRow {
	if row == nil {
		return nil
	}
	copy := *row
	return &copy
}

type RegistrySnapshotResult struct {
	Instances []*InstanceRow
	Groups    []*GroupRow // Input order; nil for explicit deletions.
}

func mergeGroupSnapshots(updates []GroupSnapshot, current []*GroupRow, instances, mergedInstances []*InstanceRow) ([]*GroupRow, []string, error) {
	byPath := make(map[string]*GroupRow, len(current))
	for _, group := range current {
		byPath[group.Path] = group
	}
	merged := make([]*GroupRow, len(updates))
	removed := make(map[string]bool)
	seen := make(map[string]bool)
	effectiveInstances := make(map[string]*InstanceRow, len(instances)+len(mergedInstances))
	for _, row := range instances {
		effectiveInstances[row.ID] = row
	}
	for _, row := range mergedInstances {
		effectiveInstances[row.ID] = row
	}
	targets := make(map[string]bool)
	for i, update := range updates {
		var path string
		if update.Stored != nil {
			path = update.Stored.Path
		} else if update.Desired != nil {
			path = update.Desired.Path
		} else if update.Original != nil {
			path = update.Original.Path
		}
		if path == "" || seen[path] {
			return nil, nil, fmt.Errorf("invalid or duplicate group snapshot: %s", path)
		}
		seen[path] = true
		if update.Ensure && update.Stored == nil && update.Desired != nil && byPath[path] == nil && reflect.DeepEqual(update.Original, update.Desired) {
			hasMembers := false
			for _, row := range effectiveInstances {
				if inGroupSubtree(row.GroupPath, path) {
					hasMembers = true
					break
				}
			}
			// A fallback tree may describe a member's stale group path. An unchanged
			// implicit group without any authoritative members is not creation intent.
			if !hasMembers {
				continue
			}
		}
		row, err := mergeGroupSnapshot(update, byPath[path])
		if err != nil {
			return nil, nil, err
		}
		merged[i] = row
		if row != nil {
			if targets[row.Path] {
				return nil, nil, fmt.Errorf("duplicate group target conflict: %s", row.Path)
			}
			targets[row.Path] = true
		}
		if update.Stored != nil && (row == nil || row.Path != path) {
			removed[path] = true
		}
		if row != nil && row.Path != path && byPath[row.Path] != nil {
			return nil, nil, fmt.Errorf("concurrent group rename target conflict: %s", row.Path)
		}
	}
	// Renaming or removing a subtree may not sweep or orphan additions that
	// were absent from the caller's snapshot. Reloading makes them reviewable.
	requestedInstances := make(map[string]*InstanceRow, len(mergedInstances))
	for _, row := range mergedInstances {
		requestedInstances[row.ID] = row
	}
	for path := range removed {
		for _, group := range current {
			if inGroupSubtree(group.Path, path) && !removed[group.Path] {
				return nil, nil, fmt.Errorf("concurrent subgroup addition conflict: %s", group.Path)
			}
		}
		for _, row := range instances {
			if !inGroupSubtree(row.GroupPath, path) {
				continue
			}
			desired := requestedInstances[row.ID]
			if desired == nil || inGroupSubtree(desired.GroupPath, path) {
				return nil, nil, fmt.Errorf("concurrent group membership conflict for instance %s", row.ID)
			}
		}
	}
	paths := make([]string, 0, len(removed))
	for path := range removed {
		paths = append(paths, path)
	}
	return merged, paths, nil
}

func inGroupSubtree(candidate, path string) bool {
	return candidate == path || strings.HasPrefix(candidate, path+"/")
}

func mergeGroupSnapshot(update GroupSnapshot, current *GroupRow) (*GroupRow, error) {
	if update.Stored == nil {
		if update.Desired == nil {
			if current == nil {
				return nil, nil
			}
			return nil, fmt.Errorf("unversioned group deletion conflict: %s", current.Path)
		}
		if current == nil {
			return CloneGroupRow(update.Desired), nil
		}
		if reflect.DeepEqual(update.Desired, current) {
			return CloneGroupRow(current), nil
		}
		if update.Ensure && reflect.DeepEqual(update.Original, update.Desired) {
			return CloneGroupRow(current), nil
		}
		return nil, fmt.Errorf("concurrent group insertion conflict: %s", update.Desired.Path)
	}
	if update.Original == nil {
		return nil, fmt.Errorf("invalid group snapshot: %s", update.Stored.Path)
	}
	if update.Desired == nil {
		if current == nil {
			return nil, nil
		}
		if !reflect.DeepEqual(current, update.Stored) {
			return nil, fmt.Errorf("concurrent group deletion conflict: %s", update.Stored.Path)
		}
		return nil, nil
	}
	if current == nil {
		return nil, fmt.Errorf("stale concurrent group deletion conflict: %s", update.Stored.Path)
	}
	merged := CloneGroupRow(current)
	want, original := reflect.ValueOf(update.Desired).Elem(), reflect.ValueOf(update.Original).Elem()
	stored, actual := reflect.ValueOf(update.Stored).Elem(), reflect.ValueOf(current).Elem()
	out := reflect.ValueOf(merged).Elem()
	for i := 0; i < want.NumField(); i++ {
		if reflect.DeepEqual(want.Field(i).Interface(), original.Field(i).Interface()) {
			continue
		}
		if !reflect.DeepEqual(actual.Field(i).Interface(), stored.Field(i).Interface()) && !reflect.DeepEqual(actual.Field(i).Interface(), want.Field(i).Interface()) {
			return nil, fmt.Errorf("concurrent group %s conflict: %s", want.Type().Field(i).Name, current.Path)
		}
		out.Field(i).Set(want.Field(i))
	}
	return merged, nil
}
