package session

import (
	"sort"
	"strings"
	"unicode"
)

// DefaultGroupName is the name used for the default group where ungrouped sessions go
const DefaultGroupName = "My Sessions"

// ItemType represents the type of item in the flattened list
type ItemType int

const (
	ItemTypeGroup ItemType = iota
	ItemTypeSession
)

// Item represents a single item in the flattened group tree view
type Item struct {
	Type          ItemType
	Group         *Group
	Session       *Instance
	Level         int    // Indentation level (0 for root groups, 1 for sessions)
	Path          string // Group path for this item
	IsLastInGroup bool   // True if this is the last session in its group (for tree rendering)
}

// Group represents a group of sessions
type Group struct {
	Name     string
	Path     string // Full path like "projects" or "projects/devops"
	Expanded bool
	Sessions []*Instance
	Order    int
}

// GroupTree manages hierarchical session organization
type GroupTree struct {
	Groups    map[string]*Group // path -> group
	GroupList []*Group          // Ordered list of groups
	Expanded  map[string]bool   // Collapsed state persistence
}

// NewGroupTree creates a new group tree from instances
func NewGroupTree(instances []*Instance) *GroupTree {
	tree := &GroupTree{
		Groups:   make(map[string]*Group),
		Expanded: make(map[string]bool),
	}

	// Build groups from instances
	for _, inst := range instances {
		groupPath := inst.GroupPath
		if groupPath == "" {
			groupPath = DefaultGroupName
		}

		group, exists := tree.Groups[groupPath]
		if !exists {
			group = &Group{
				Name:     groupPath,
				Path:     groupPath,
				Expanded: true, // Default expanded
				Sessions: []*Instance{},
			}
			tree.Groups[groupPath] = group
			tree.Expanded[groupPath] = true
		}
		group.Sessions = append(group.Sessions, inst)
	}

	// Sort groups alphabetically and assign order
	tree.rebuildGroupList()

	return tree
}

// NewGroupTreeWithGroups creates a group tree from instances and stored group data
func NewGroupTreeWithGroups(instances []*Instance, storedGroups []*GroupData) *GroupTree {
	tree := &GroupTree{
		Groups:   make(map[string]*Group),
		Expanded: make(map[string]bool),
	}

	// First, create groups from stored data (preserves empty groups)
	for _, gd := range storedGroups {
		group := &Group{
			Name:     gd.Name,
			Path:     gd.Path,
			Expanded: gd.Expanded,
			Sessions: []*Instance{},
			Order:    gd.Order,
		}
		tree.Groups[gd.Path] = group
		tree.Expanded[gd.Path] = gd.Expanded
	}

	// Then add instances to their groups
	for _, inst := range instances {
		groupPath := inst.GroupPath
		if groupPath == "" {
			groupPath = DefaultGroupName
		}

		group, exists := tree.Groups[groupPath]
		if !exists {
			// Group doesn't exist in stored data, create it
			group = &Group{
				Name:     groupPath,
				Path:     groupPath,
				Expanded: true,
				Sessions: []*Instance{},
			}
			tree.Groups[groupPath] = group
			tree.Expanded[groupPath] = true
		}
		group.Sessions = append(group.Sessions, inst)
	}

	// Rebuild group list maintaining stored order
	tree.rebuildGroupList()

	return tree
}

// Note: GroupData is defined in storage.go in the same package

// rebuildGroupList rebuilds the ordered group list
func (t *GroupTree) rebuildGroupList() {
	t.GroupList = make([]*Group, 0, len(t.Groups))
	for _, g := range t.Groups {
		t.GroupList = append(t.GroupList, g)
	}
	sort.Slice(t.GroupList, func(i, j int) bool {
		// Sort hierarchically: parents before children, siblings by order
		pathI := t.GroupList[i].Path
		pathJ := t.GroupList[j].Path

		// If one is a prefix of the other (parent-child), parent comes first
		if strings.HasPrefix(pathJ, pathI+"/") {
			return true // i is parent of j
		}
		if strings.HasPrefix(pathI, pathJ+"/") {
			return false // j is parent of i
		}

		// Get parent paths for comparison
		parentI := getParentPath(pathI)
		parentJ := getParentPath(pathJ)

		// If they have the same parent, sort by order then name
		if parentI == parentJ {
			if t.GroupList[i].Order != t.GroupList[j].Order {
				return t.GroupList[i].Order < t.GroupList[j].Order
			}
			return t.GroupList[i].Name < t.GroupList[j].Name
		}

		// Different parents - sort by full path to keep hierarchy together
		return pathI < pathJ
	})
	// Assign sequential orders (preserving relative order)
	for i, g := range t.GroupList {
		g.Order = i
	}
}

// getParentPath returns the parent path of a group path
func getParentPath(path string) string {
	if idx := strings.LastIndex(path, "/"); idx != -1 {
		return path[:idx]
	}
	return "" // root level
}

// GetGroupLevel returns the nesting level of a group (0 for root, 1 for child, etc.)
func GetGroupLevel(path string) int {
	if path == "" {
		return 0
	}
	return strings.Count(path, "/")
}

// Flatten returns a flat list of items for cursor navigation
func (t *GroupTree) Flatten() []Item {
	items := []Item{}

	for _, group := range t.GroupList {
		// Calculate group nesting level from path
		groupLevel := GetGroupLevel(group.Path)

		// Check if parent group is collapsed - if so, skip this group
		if groupLevel > 0 {
			idx := strings.LastIndex(group.Path, "/")
			if idx == -1 {
				continue // Malformed path, skip
			}
			parentPath := group.Path[:idx]
			if parentGroup, exists := t.Groups[parentPath]; exists && !parentGroup.Expanded {
				continue // Parent is collapsed, skip this subgroup
			}
		}

		// Add group header
		items = append(items, Item{
			Type:  ItemTypeGroup,
			Group: group,
			Level: groupLevel,
			Path:  group.Path,
		})

		// Add sessions if expanded
		if group.Expanded {
			sessionCount := len(group.Sessions)
			for i, sess := range group.Sessions {
				items = append(items, Item{
					Type:          ItemTypeSession,
					Session:       sess,
					Level:         groupLevel + 1,
					Path:          group.Path,
					IsLastInGroup: i == sessionCount-1, // Mark last session for tree rendering
				})
			}
		}
	}

	return items
}

// ToggleGroup toggles the expanded state of a group
func (t *GroupTree) ToggleGroup(path string) {
	if group, exists := t.Groups[path]; exists {
		group.Expanded = !group.Expanded
		t.Expanded[path] = group.Expanded
	}
}

// ExpandGroup expands a group
func (t *GroupTree) ExpandGroup(path string) {
	if group, exists := t.Groups[path]; exists {
		group.Expanded = true
		t.Expanded[path] = true
	}
}

// ExpandGroupWithParents expands a group and all its parent groups
// This ensures the group and its contents are visible in the flattened view
func (t *GroupTree) ExpandGroupWithParents(path string) {
	// Expand all parent groups first
	parts := strings.Split(path, "/")
	currentPath := ""
	for i := 0; i < len(parts); i++ {
		if currentPath == "" {
			currentPath = parts[i]
		} else {
			currentPath = currentPath + "/" + parts[i]
		}
		if group, exists := t.Groups[currentPath]; exists {
			group.Expanded = true
			t.Expanded[currentPath] = true
		}
	}
}

// CollapseGroup collapses a group
func (t *GroupTree) CollapseGroup(path string) {
	if group, exists := t.Groups[path]; exists {
		group.Expanded = false
		t.Expanded[path] = false
	}
}

// MoveGroupUp moves a group up in the order (only within siblings at same level)
func (t *GroupTree) MoveGroupUp(path string) {
	parentPath := getParentPath(path)

	for i, g := range t.GroupList {
		if g.Path == path && i > 0 {
			// Only swap if previous item is a sibling (same parent)
			prevParent := getParentPath(t.GroupList[i-1].Path)
			if prevParent == parentPath {
				t.GroupList[i], t.GroupList[i-1] = t.GroupList[i-1], t.GroupList[i]
				t.GroupList[i].Order = i
				t.GroupList[i-1].Order = i - 1
			}
			break
		}
	}
}

// MoveGroupDown moves a group down in the order (only within siblings at same level)
func (t *GroupTree) MoveGroupDown(path string) {
	parentPath := getParentPath(path)

	for i, g := range t.GroupList {
		if g.Path == path && i < len(t.GroupList)-1 {
			// Only swap if next item is a sibling (same parent)
			nextParent := getParentPath(t.GroupList[i+1].Path)
			if nextParent == parentPath {
				t.GroupList[i], t.GroupList[i+1] = t.GroupList[i+1], t.GroupList[i]
				t.GroupList[i].Order = i
				t.GroupList[i+1].Order = i + 1
			}
			break
		}
	}
}

// MoveSessionUp moves a session up within its group
func (t *GroupTree) MoveSessionUp(inst *Instance) {
	group, exists := t.Groups[inst.GroupPath]
	if !exists {
		return
	}

	for i, s := range group.Sessions {
		if s.ID == inst.ID && i > 0 {
			group.Sessions[i], group.Sessions[i-1] = group.Sessions[i-1], group.Sessions[i]
			break
		}
	}
}

// MoveSessionDown moves a session down within its group
func (t *GroupTree) MoveSessionDown(inst *Instance) {
	group, exists := t.Groups[inst.GroupPath]
	if !exists {
		return
	}

	for i, s := range group.Sessions {
		if s.ID == inst.ID && i < len(group.Sessions)-1 {
			group.Sessions[i], group.Sessions[i+1] = group.Sessions[i+1], group.Sessions[i]
			break
		}
	}
}

// MoveSessionToGroup moves a session to a different group
func (t *GroupTree) MoveSessionToGroup(inst *Instance, newGroupPath string) {
	// Remove from old group
	if oldGroup, exists := t.Groups[inst.GroupPath]; exists {
		for i, s := range oldGroup.Sessions {
			if s.ID == inst.ID {
				oldGroup.Sessions = append(oldGroup.Sessions[:i], oldGroup.Sessions[i+1:]...)
				break
			}
		}
		// NOTE: We do NOT delete empty groups here - user-created groups should persist
	}

	// Add to new group
	inst.GroupPath = newGroupPath
	newGroup, exists := t.Groups[newGroupPath]
	if !exists {
		newGroup = &Group{
			Name:     newGroupPath,
			Path:     newGroupPath,
			Expanded: true,
			Sessions: []*Instance{},
		}
		t.Groups[newGroupPath] = newGroup
		t.rebuildGroupList()
	}
	newGroup.Sessions = append(newGroup.Sessions, inst)
}

// sanitizeGroupName removes dangerous characters from group names
// to prevent path traversal and other security issues
func sanitizeGroupName(name string) string {
	// Remove or replace dangerous characters
	var result strings.Builder
	result.Grow(len(name))

	for _, r := range name {
		// Allow letters, digits, spaces, hyphens, and underscores
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '-' || r == '_' {
			result.WriteRune(r)
		} else if r == '/' || r == '\\' || r == '.' {
			// Replace path separators and dots with hyphens
			result.WriteRune('-')
		}
		// Other characters are dropped
	}

	// Clean up multiple consecutive hyphens
	cleaned := result.String()
	for strings.Contains(cleaned, "--") {
		cleaned = strings.ReplaceAll(cleaned, "--", "-")
	}

	// Trim leading/trailing hyphens and spaces
	cleaned = strings.Trim(cleaned, "- ")

	// If the result is empty after sanitization, use a default
	if cleaned == "" {
		return "unnamed"
	}

	return cleaned
}

// CreateGroup creates a new empty group
func (t *GroupTree) CreateGroup(name string) *Group {
	// Sanitize name to prevent path traversal and security issues
	sanitizedName := sanitizeGroupName(name)
	path := strings.ToLower(strings.ReplaceAll(sanitizedName, " ", "-"))
	if _, exists := t.Groups[path]; exists {
		return t.Groups[path]
	}

	group := &Group{
		Name:     sanitizedName,
		Path:     path,
		Expanded: true,
		Sessions: []*Instance{},
		Order:    len(t.GroupList),
	}
	t.Groups[path] = group
	t.Expanded[path] = true
	t.rebuildGroupList()
	return group
}

// CreateSubgroup creates a new empty group under a parent group
func (t *GroupTree) CreateSubgroup(parentPath, name string) *Group {
	// Sanitize name to prevent path traversal and security issues
	sanitizedName := sanitizeGroupName(name)
	childPath := strings.ToLower(strings.ReplaceAll(sanitizedName, " ", "-"))
	fullPath := parentPath + "/" + childPath

	if _, exists := t.Groups[fullPath]; exists {
		return t.Groups[fullPath]
	}

	group := &Group{
		Name:     sanitizedName,
		Path:     fullPath,
		Expanded: true,
		Sessions: []*Instance{},
		Order:    len(t.GroupList),
	}
	t.Groups[fullPath] = group
	t.Expanded[fullPath] = true
	t.rebuildGroupList()
	return group
}

// RenameGroup renames a group and updates all subgroups
func (t *GroupTree) RenameGroup(oldPath, newName string) {
	group, exists := t.Groups[oldPath]
	if !exists {
		return
	}

	// Sanitize name to prevent path traversal and security issues
	sanitizedName := sanitizeGroupName(newName)
	newPath := strings.ToLower(strings.ReplaceAll(sanitizedName, " ", "-"))
	if newPath == oldPath {
		group.Name = sanitizedName
		return
	}

	// Update all sessions in the group
	for _, sess := range group.Sessions {
		sess.GroupPath = newPath
	}

	// Update all subgroups (groups whose path starts with oldPath + "/")
	subgroupsToUpdate := make(map[string]*Group)
	for path, g := range t.Groups {
		if strings.HasPrefix(path, oldPath+"/") {
			newSubPath := newPath + path[len(oldPath):] // Replace prefix
			// Update sessions in subgroup
			for _, sess := range g.Sessions {
				sess.GroupPath = newSubPath
			}
			g.Path = newSubPath
			subgroupsToUpdate[path] = g
		}
	}

	// Remove old subgroup entries and add with new paths
	for oldSubPath, g := range subgroupsToUpdate {
		delete(t.Groups, oldSubPath)
		t.Groups[g.Path] = g
		expanded := t.Expanded[oldSubPath]
		delete(t.Expanded, oldSubPath)
		t.Expanded[g.Path] = expanded
	}

	// Update the main group
	group.Name = sanitizedName
	group.Path = newPath

	// Update maps for main group
	delete(t.Groups, oldPath)
	t.Groups[newPath] = group
	delete(t.Expanded, oldPath)
	t.Expanded[newPath] = group.Expanded

	t.rebuildGroupList()
}

// DeleteGroup deletes a group, all its subgroups, and moves all sessions to default
func (t *GroupTree) DeleteGroup(path string) []*Instance {
	group, exists := t.Groups[path]
	if !exists || path == DefaultGroupName {
		return nil
	}

	// Collect all sessions from this group and all subgroups
	allMovedSessions := []*Instance{}

	// Find and delete all subgroups first (groups whose path starts with this path + "/")
	subgroupPaths := []string{}
	for groupPath := range t.Groups {
		if strings.HasPrefix(groupPath, path+"/") {
			subgroupPaths = append(subgroupPaths, groupPath)
		}
	}

	// Collect sessions from subgroups and delete them
	for _, subPath := range subgroupPaths {
		if subGroup, exists := t.Groups[subPath]; exists {
			allMovedSessions = append(allMovedSessions, subGroup.Sessions...)
			delete(t.Groups, subPath)
			delete(t.Expanded, subPath)
		}
	}

	// Add sessions from the main group
	allMovedSessions = append(allMovedSessions, group.Sessions...)

	// Move all sessions to default group
	for _, sess := range allMovedSessions {
		sess.GroupPath = DefaultGroupName
	}

	// Ensure default group exists
	defaultGroup, exists := t.Groups[DefaultGroupName]
	if !exists {
		defaultGroup = &Group{
			Name:     DefaultGroupName,
			Path:     DefaultGroupName,
			Expanded: true,
			Sessions: []*Instance{},
		}
		t.Groups[DefaultGroupName] = defaultGroup
	}
	defaultGroup.Sessions = append(defaultGroup.Sessions, allMovedSessions...)

	// Remove the main group
	delete(t.Groups, path)
	delete(t.Expanded, path)
	t.rebuildGroupList()

	return allMovedSessions
}

// GetAllInstances returns all instances in order
func (t *GroupTree) GetAllInstances() []*Instance {
	instances := []*Instance{}
	for _, group := range t.GroupList {
		instances = append(instances, group.Sessions...)
	}
	return instances
}

// GetGroupNames returns all group names for selection
func (t *GroupTree) GetGroupNames() []string {
	names := make([]string, len(t.GroupList))
	for i, g := range t.GroupList {
		names[i] = g.Name
	}
	return names
}

// SessionCount returns total session count
func (t *GroupTree) SessionCount() int {
	count := 0
	for _, g := range t.Groups {
		count += len(g.Sessions)
	}
	return count
}

// GroupCount returns total group count
func (t *GroupTree) GroupCount() int {
	return len(t.Groups)
}

// AddSession adds a session to the appropriate group
func (t *GroupTree) AddSession(inst *Instance) {
	groupPath := inst.GroupPath
	if groupPath == "" {
		groupPath = DefaultGroupName
		inst.GroupPath = groupPath
	}

	group, exists := t.Groups[groupPath]
	if !exists {
		group = &Group{
			Name:     groupPath,
			Path:     groupPath,
			Expanded: true,
			Sessions: []*Instance{},
			Order:    len(t.GroupList),
		}
		t.Groups[groupPath] = group
		t.Expanded[groupPath] = true
		t.rebuildGroupList()
	}
	group.Sessions = append(group.Sessions, inst)
}

// RemoveSession removes a session from its group
func (t *GroupTree) RemoveSession(inst *Instance) {
	groupPath := inst.GroupPath
	if groupPath == "" {
		groupPath = DefaultGroupName
	}

	if group, exists := t.Groups[groupPath]; exists {
		for i, s := range group.Sessions {
			if s.ID == inst.ID {
				group.Sessions = append(group.Sessions[:i], group.Sessions[i+1:]...)
				break
			}
		}
		// NOTE: We do NOT delete empty groups - they persist until explicitly deleted
	}
}

// GetGroupPaths returns all group paths for selection
func (t *GroupTree) GetGroupPaths() []string {
	paths := make([]string, len(t.GroupList))
	for i, g := range t.GroupList {
		paths[i] = g.Path
	}
	return paths
}

// SyncWithInstances updates the tree with a new set of instances
// while preserving existing group structure (including empty groups)
func (t *GroupTree) SyncWithInstances(instances []*Instance) {
	// Clear all sessions from groups (but keep the groups)
	for _, group := range t.Groups {
		group.Sessions = []*Instance{}
	}

	// Re-add all instances to their groups
	for _, inst := range instances {
		groupPath := inst.GroupPath
		if groupPath == "" {
			groupPath = DefaultGroupName
			inst.GroupPath = groupPath
		}

		group, exists := t.Groups[groupPath]
		if !exists {
			// Create new group for this session's path
			group = &Group{
				Name:     groupPath,
				Path:     groupPath,
				Expanded: true,
				Sessions: []*Instance{},
				Order:    len(t.GroupList),
			}
			t.Groups[groupPath] = group
			t.Expanded[groupPath] = true
			t.rebuildGroupList()
		}
		group.Sessions = append(group.Sessions, inst)
	}
}
