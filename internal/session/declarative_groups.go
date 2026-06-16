package session

import "strings"

// canonicalGroupPath returns the stored path CreateGroupPath produces for the
// given input.
func canonicalGroupPath(path string) string {
	segments := make([]string, 0)
	for _, segment := range strings.Split(path, "/") {
		if strings.TrimSpace(segment) == "" {
			continue
		}
		clean := strings.ReplaceAll(sanitizeGroupName(segment), " ", "-")
		segments = append(segments, clean)
	}
	return strings.Join(segments, "/")
}

// ReconcileDeclarativeGroups creates groups declared with create = true under
// [groups."<path>"] in cfg, and applies default_path to declared or existing
// groups. It never deletes a group and never clears a field config omits, and
// returns true if the tree was modified.
//
// Callers must pass a tree loaded with the full stored group set.
func ReconcileDeclarativeGroups(tree *GroupTree, cfg *UserConfig) bool {
	if tree == nil || cfg == nil {
		return false
	}

	changed := false
	for key, settings := range cfg.Groups {
		if !settings.Create && settings.DefaultPath == "" {
			continue
		}
		path := canonicalGroupPath(key)
		if path == "" {
			continue
		}

		if settings.Create {
			if _, exists := tree.Groups[path]; !exists {
				if tree.CreateGroupPath(path) == nil {
					continue
				}
				changed = true
			}
		}

		if settings.DefaultPath == "" {
			continue
		}
		resolved := resolveGroupDefaultPath(settings.DefaultPath)
		if group := tree.Groups[path]; group != nil && group.DefaultPath != resolved {
			tree.SetDefaultPathForGroup(path, settings.DefaultPath)
			changed = true
		}
	}
	return changed
}
