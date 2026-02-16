package web

import (
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// BuildMenuSnapshot converts in-memory session/group state into a flattened web DTO.
func BuildMenuSnapshot(profile string, instances []*session.Instance, groupsData []*session.GroupData, generatedAt time.Time) *MenuSnapshot {
	if generatedAt.IsZero() {
		generatedAt = time.Now()
	}

	groupTree := session.NewGroupTreeWithGroups(instances, groupsData)
	expandedByPath := make(map[string]bool, len(groupTree.Groups))
	for path, group := range groupTree.Groups {
		if group == nil {
			continue
		}
		expandedByPath[path] = group.Expanded

		// Build a fully-expanded flattened view so descendants are always
		// available client-side, even when persisted state is collapsed.
		group.Expanded = true
		groupTree.Expanded[path] = true
	}
	flat := groupTree.Flatten()

	items := make([]MenuItem, 0, len(flat))
	totalGroups := 0
	totalSessions := 0

	for i, item := range flat {
		if item.Type == session.ItemTypeGroup && item.Group != nil {
			expanded := item.Group.Expanded
			if persisted, ok := expandedByPath[item.Group.Path]; ok {
				expanded = persisted
			}

			totalGroups++
			items = append(items, MenuItem{
				Index: i,
				Type:  MenuItemTypeGroup,
				Level: item.Level,
				Path:  item.Path,
				Group: &MenuGroup{
					Name:         item.Group.Name,
					Path:         item.Group.Path,
					Expanded:     expanded,
					Order:        item.Group.Order,
					SessionCount: groupTree.SessionCountForGroup(item.Group.Path),
				},
			})
			continue
		}

		if item.Type == session.ItemTypeSession && item.Session != nil {
			totalSessions++
			items = append(items, MenuItem{
				Index:               i,
				Type:                MenuItemTypeSession,
				Level:               item.Level,
				Path:                item.Path,
				IsLastInGroup:       item.IsLastInGroup,
				IsSubSession:        item.IsSubSession,
				IsLastSubSession:    item.IsLastSubSession,
				ParentIsLastInGroup: item.ParentIsLastInGroup,
				Session:             toMenuSession(item.Session),
			})
		}
	}

	return &MenuSnapshot{
		Profile:       profile,
		GeneratedAt:   generatedAt.UTC(),
		TotalGroups:   totalGroups,
		TotalSessions: totalSessions,
		Items:         items,
	}
}
