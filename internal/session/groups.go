package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"unicode"

	"github.com/asheshgoplani/agent-deck/internal/git"
)

// ErrGroupAlreadyExists is returned by RenameGroup when the target path collides with an existing group.
var ErrGroupAlreadyExists = errors.New("group already exists at target path")

// ErrGroupNotFound is returned by RenameGroup when oldPath does not resolve to an existing group.
var ErrGroupNotFound = errors.New("group not found")

// DefaultGroupName is the display name for the default group where ungrouped sessions go
const DefaultGroupName = "My Sessions"

// DefaultGroupPath is the normalized path for the default group (used for lookups and protection)
const DefaultGroupPath = "my-sessions"

// ItemType represents the type of item in the flattened list
type ItemType int

const (
	ItemTypeGroup ItemType = iota
	ItemTypeSession
	ItemTypeRemoteGroup
	ItemTypeRemoteSession
	ItemTypeWindow
	ItemTypeDivider // Non-selectable separator between view-mode sections (running-on-top, etc.)
)

// Item represents a single item in the flattened group tree view
type Item struct {
	Type                ItemType
	Group               *Group
	Session             *Instance
	RemoteSession       *RemoteSessionInfo // Set for ItemTypeRemoteSession/ItemTypeRemoteGroup
	RemoteName          string             // Remote name for remote items
	Level               int                // Indentation level (0 for root groups, 1 for sessions)
	Path                string             // Group path for this item
	IsLastInGroup       bool               // True if this is the last session in its group (for tree rendering)
	RootGroupNum        int                // Pre-computed root group number for hotkey display (1-9, 0 if not a root group)
	IsSubSession        bool               // True if this session has a parent session
	IsLastSubSession    bool               // True if this is the last sub-session of its parent (for tree rendering)
	ParentIsLastInGroup bool               // True if parent session is last top-level item (for tree line rendering)
	IsWindow            bool               // True for ItemTypeWindow items
	IsLastWindow        bool               // True if last window of parent session
	WindowIndex         int                // Tmux window index (for ItemTypeWindow)
	WindowName          string             // Tmux window name (for ItemTypeWindow)
	WindowSessionID     string             // Parent session ID (for ItemTypeWindow)
	WindowTool          string             // Detected tool in this window (claude, gemini, etc.)
	CreatingID          string             // Non-empty for placeholder items (worktree creation in progress)
	CreatingTitle       string             // Display title for creating placeholder
	CreatingTool        string             // Tool for creating placeholder
	DividerLabel        string             // Label shown on an ItemTypeDivider row (e.g. "idle / done")
}

// Group represents a group of sessions
type Group struct {
	Name        string
	Path        string // Full path like "projects" or "projects/devops"
	Expanded    bool
	Sessions    []*Instance
	Order       int
	DefaultPath string // Explicit default path for new sessions in this group
	// MaxConcurrent caps simultaneous running sessions in this group (v1.9.1).
	// 0 = unlimited (legacy default for groups predating this field); 1 = serial
	// (default for newly-created groups); N>=2 = bounded parallelism. Negative
	// values are treated as unlimited (explicit opt-out).
	MaxConcurrent int
}

// GroupTree manages hierarchical session organization
type GroupTree struct {
	Groups    map[string]*Group // path -> group
	GroupList []*Group          // Ordered list of groups
	Expanded  map[string]bool   // Collapsed state persistence

	// DefaultMaxConcurrent is the max_concurrent value copied into groups
	// created via CreateGroup/CreateSubgroup. nil → built-in serial default (1),
	// preserving v1.9.1 behavior when [group_defaults] is unset. Seeded by the
	// command/UI layer from [group_defaults].max_concurrent before a create; an
	// explicit `group create --max-concurrent` flag still wins per-group.
	DefaultMaxConcurrent *int
}

// actionablePriority maps a session.Status to an "attention-needed" rank
// used by SortInstancesByActionable. Lower = more actionable = surfaces
// higher in the in-group list (issue #857).
//
// Buckets:
//
//	0  error                       (something broke, look at me)
//	1  waiting                     (model done, awaiting user input)
//	2  running, starting           (model actively working)
//	3  idle, queued, "" (unset)    (nothing to do; "" matches legacy
//	                                instances persisted before this field
//	                                was widely populated — TestSessionOrder*
//	                                rely on this neutral default)
//	4  stopped                     (user-parked, bottom of pile)
//
// Any future Status value not enumerated above defaults to 5 so it sorts
// after every known bucket rather than silently slotting into "idle".
func actionablePriority(s Status) int {
	switch s {
	case StatusError:
		return 0
	case StatusWaiting:
		return 1
	case StatusRunning, StatusStarting:
		return 2
	case StatusIdle, StatusQueued, "":
		return 3
	case StatusStopped:
		return 4
	}
	return 5
}

// pinZone maps a session to its outermost sort band (pin-sessions feature).
// Lower bands surface higher in the group's list.
//
//	-1 maestro      the fleet supervisor — a fixed point of reference that
//	                surfaces above everything, including pin-top
//	0  pin-top      fixed at the top, exempt from status/recency
//	1  normal       the within-group sort (creation Order, or actionable
//	                status → recency → Order — see group_sort config)
//	2  pin-bottom   fixed at the bottom, exempt from status/recency
func pinZone(inst *Instance) int {
	if inst.IsMaestro() {
		return -1
	}
	switch inst.Pin {
	case PinTop:
		return 0
	case PinBottom:
		return 2
	default:
		return 1
	}
}

// stablePinPartition reorders insts in place into pin-top, normal, and
// pin-bottom bands (pinZone), preserving the relative order of sessions within
// each band. Unlike SortInstancesByActionable it never reorders by
// status/recency, so it is safe to run on every render (Flatten): it moves only
// pinned rows, leaving the load-time order (creation or actionable, per the
// group_sort config) — and any live K/J manual order — of the normal band
// untouched. This is what makes a pin edit take effect live instead of only
// after a restart.
func stablePinPartition(insts []*Instance) {
	sort.SliceStable(insts, func(i, j int) bool {
		zi, zj := pinZone(insts[i]), pinZone(insts[j])
		if zi != zj {
			return zi < zj
		}
		// Maestro (-1), pin-top (0), and pin-bottom (2) bands are fully fixed by
		// Order, matching the load-time SortInstancesByActionable. Ordering them
		// here means a freshly pinned row lands in its correct Order slot live —
		// not wherever it happened to sit in slice order before the pin edit.
		if zi != 1 {
			return insts[i].Order < insts[j].Order
		}
		// Normal (1) band is already sorted at load (creation Order, or actionable
		// per group_sort); return false so SliceStable leaves its relative order
		// untouched.
		return false
	})
}

// SortInstancesByActionable sorts the given slice in place according to the
// active within-group sort mode (see SetGroupSortMode), while honoring
// per-session pins (pin-sessions feature). The outermost key is the pin zone
// (see pinZone); within the normal zone the sort depends on mode:
//
//   - "creation" (default): Order asc only — sessions keep their creation /
//     K/J manual order unchanged.
//   - "actionable" (issue #857): status→recency tiers apply before Order so
//     the most recently actionable sessions surface first.
//
// Pin-top and pin-bottom bands are always ordered by Order alone (fully fixed
// — status and recency are ignored, so K/J reordering still works inside a
// band).
//
// Actionable-mode normal-zone key precedence:
//
//  1. actionablePriority(Status)   asc  — error/waiting/running first
//  2. LastAccessedAt              desc  — recent attention first
//  3. Order                        asc  — preserves user-customized
//     position as a stable tie-breaker
//     (TestSessionOrderPersistence,
//     TestSessionOrderMigration)
func SortInstancesByActionable(insts []*Instance) {
	mode := currentGroupSortMode()
	sort.SliceStable(insts, func(i, j int) bool {
		// The outermost key is the band: maestro (the fleet supervisor, a fixed
		// point of reference that surfaces first regardless of status), then
		// pin-top, normal, and pin-bottom (see pinZone).
		zi, zj := pinZone(insts[i]), pinZone(insts[j])
		if zi != zj {
			return zi < zj
		}
		// Maestro (-1), pin-top (0), and pin-bottom (2) bands are fully fixed:
		// Order only.
		if zi != 1 {
			return insts[i].Order < insts[j].Order
		}
		// Normal band. In actionable mode (issue #857) the status→recency tiers
		// apply before Order; in creation mode (default) Order alone decides, so
		// sessions keep their creation order (or K/J manual order).
		if mode == "actionable" {
			pi, pj := actionablePriority(insts[i].Status), actionablePriority(insts[j].Status)
			if pi != pj {
				return pi < pj
			}
			ai, aj := insts[i].LastAccessedAt, insts[j].LastAccessedAt
			if !ai.Equal(aj) {
				return ai.After(aj)
			}
		}
		return insts[i].Order < insts[j].Order
	})
}

// groupSortMode caches the active within-group sort mode ("creation" or
// "actionable"). It is refreshed from LoadUserConfig on every config (re)load,
// so SortInstancesByActionable can read it without a disk hit and without
// threading a parameter through the tree constructors. Defaults to "creation"
// until SetGroupSortMode is first called.
var groupSortMode atomic.Value // holds string

// SetGroupSortMode updates the cached within-group sort mode. Any value other
// than "actionable" normalizes to "creation".
func SetGroupSortMode(mode string) {
	if mode != "actionable" {
		mode = "creation"
	}
	groupSortMode.Store(mode)
}

// currentGroupSortMode returns the cached mode, defaulting to "creation" when
// it has never been set.
func currentGroupSortMode() string {
	if v, ok := groupSortMode.Load().(string); ok && v != "" {
		return v
	}
	return "creation"
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
			groupPath = DefaultGroupPath
		}

		group, exists := tree.Groups[groupPath]
		if !exists {
			// Ensure parent groups exist for hierarchical paths
			tree.ensureParentGroupsExist(groupPath)
			// Use proper name for default group, otherwise extract name from path
			name := extractGroupName(groupPath)
			if groupPath == DefaultGroupPath {
				name = DefaultGroupName
			}
			group = &Group{
				Name:     name,
				Path:     groupPath,
				Expanded: true, // Default expanded
				Sessions: []*Instance{},
			}
			tree.Groups[groupPath] = group
			tree.Expanded[groupPath] = true
		}
		group.Sessions = append(group.Sessions, inst)
	}

	// Sort sessions within each group by actionability (issue #857).
	// Persisted Order is preserved as the stable tie-breaker, so
	// instances without a Status set behave identically to the prior
	// Order-only sort.
	for _, group := range tree.Groups {
		SortInstancesByActionable(group.Sessions)
	}

	// Sort groups alphabetically and assign order
	tree.rebuildGroupList()

	// Update default paths for all groups
	for groupPath := range tree.Groups {
		tree.updateGroupDefaultPath(groupPath)
	}

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
			Name:          gd.Name,
			Path:          gd.Path,
			Expanded:      gd.Expanded,
			Sessions:      []*Instance{},
			Order:         gd.Order,
			DefaultPath:   gd.DefaultPath,
			MaxConcurrent: gd.MaxConcurrent,
		}
		tree.Groups[gd.Path] = group
		tree.Expanded[gd.Path] = gd.Expanded
	}

	// Then add instances to their groups
	for _, inst := range instances {
		groupPath := inst.GroupPath
		if groupPath == "" {
			groupPath = DefaultGroupPath
		}

		group, exists := tree.Groups[groupPath]
		if !exists {
			// Ensure parent groups exist for hierarchical paths
			tree.ensureParentGroupsExist(groupPath)
			// Group doesn't exist in stored data, create it
			// Use proper name for default group, otherwise extract name from path
			name := extractGroupName(groupPath)
			if groupPath == DefaultGroupPath {
				name = DefaultGroupName
			}
			group = &Group{
				Name:     name,
				Path:     groupPath,
				Expanded: true,
				Sessions: []*Instance{},
			}
			tree.Groups[groupPath] = group
			tree.Expanded[groupPath] = true
		}
		group.Sessions = append(group.Sessions, inst)
	}

	// Sort sessions within each group by actionability (issue #857).
	// Persisted Order is preserved as the stable tie-breaker, so
	// instances without a Status set behave identically to the prior
	// Order-only sort.
	for _, group := range tree.Groups {
		SortInstancesByActionable(group.Sessions)
	}

	// Rebuild group list maintaining stored order
	tree.rebuildGroupList()

	// Update default paths for all groups (may override stored if sessions have newer paths)
	for groupPath := range tree.Groups {
		tree.updateGroupDefaultPath(groupPath)
	}

	return tree
}

// Note: GroupData is defined in storage.go in the same package

// rebuildGroupList rebuilds the ordered group list
func (t *GroupTree) rebuildGroupList() {
	t.GroupList = make([]*Group, 0, len(t.Groups))
	for _, g := range t.Groups {
		// Always pin the "conductor" group to the top
		if g.Path == "conductor" && g.Order >= 0 {
			g.Order = -1
		}
		// The group holding the fleet supervisor (Maestro) pins above
		// everything, including the legacy "conductor" pin.
		if g.Order >= -1 {
			for _, inst := range g.Sessions {
				if inst.IsMaestro() {
					g.Order = -2
					break
				}
			}
		}
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

		// Different parents - compare at root level to keep tree structure
		// This ensures subgroups stay grouped with their root ancestors
		rootI := getRootPath(pathI)
		rootJ := getRootPath(pathJ)

		if rootI == rootJ {
			// Same root - find the branch ancestors at the divergence point and compare as siblings
			// Example: comparing "a/b/c" with "a/d" - find "b" and "d" (children of common ancestor "a")
			partsI := strings.Split(pathI, "/")
			partsJ := strings.Split(pathJ, "/")

			// Find the first point where paths diverge
			divergeLevel := 0
			for divergeLevel < len(partsI) && divergeLevel < len(partsJ) {
				if partsI[divergeLevel] != partsJ[divergeLevel] {
					break
				}
				divergeLevel++
			}

			// Get the branch paths at the divergence point
			branchPathI := strings.Join(partsI[:divergeLevel+1], "/")
			branchPathJ := strings.Join(partsJ[:divergeLevel+1], "/")

			// Compare branch roots as siblings (by Order, then Name)
			branchI := t.Groups[branchPathI]
			branchJ := t.Groups[branchPathJ]

			if branchI != nil && branchJ != nil {
				if branchI.Order != branchJ.Order {
					return branchI.Order < branchJ.Order
				}
				return branchI.Name < branchJ.Name
			}

			// Fallback to path comparison if branches not found
			return pathI < pathJ
		}

		// Different root ancestors - compare roots by order then name
		// This ensures entire subtrees are kept together
		rootGroupI := t.Groups[rootI]
		rootGroupJ := t.Groups[rootJ]
		if rootGroupI != nil && rootGroupJ != nil {
			if rootGroupI.Order != rootGroupJ.Order {
				return rootGroupI.Order < rootGroupJ.Order
			}
			return rootGroupI.Name < rootGroupJ.Name
		}

		// Fallback to full path comparison if root groups not found
		return pathI < pathJ
	})
	// Note: Do NOT reassign Order values here - this would destroy user-customized
	// order stored in state.db. Order values are set:
	// 1. When loaded from storage (preserved)
	// 2. When creating new groups (Order = len(GroupList))
	// 3. When manually reordering with K/J keys (MoveGroupUp/Down)
}

// getParentPath returns the parent path of a group path
func getParentPath(path string) string {
	if idx := strings.LastIndex(path, "/"); idx != -1 {
		return path[:idx]
	}
	return "" // root level
}

// getRootPath returns the root-level path (first segment) of a hierarchical path
// e.g., "parent/child/grandchild" -> "parent", "root" -> "root"
func getRootPath(path string) string {
	if idx := strings.Index(path, "/"); idx != -1 {
		return path[:idx]
	}
	return path // already root level
}

// extractGroupName extracts the display name from a group path
// e.g., "parent/child" -> "child", "root" -> "root"
func extractGroupName(path string) string {
	if path == "" {
		return ""
	}
	if idx := strings.LastIndex(path, "/"); idx != -1 {
		return path[idx+1:]
	}
	return path // root level - path is the name
}

// ensureParentGroupsExist creates all parent groups for a given path if they don't exist
// e.g., for path "a/b/c", it creates groups "a" and "a/b" (but not "a/b/c")
func (t *GroupTree) ensureParentGroupsExist(path string) {
	parts := strings.Split(path, "/")
	if len(parts) <= 1 {
		return // No parents needed for root-level paths
	}

	// Create each parent level
	currentPath := ""
	for i := 0; i < len(parts)-1; i++ { // -1 to exclude the leaf
		if currentPath == "" {
			currentPath = parts[i]
		} else {
			currentPath = currentPath + "/" + parts[i]
		}

		if _, exists := t.Groups[currentPath]; !exists {
			name := extractGroupName(currentPath)
			group := &Group{
				Name:     name,
				Path:     currentPath,
				Expanded: true,
				Sessions: []*Instance{},
				Order:    len(t.GroupList),
			}
			t.Groups[currentPath] = group
			t.Expanded[currentPath] = true
		}
	}
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
			// Separate parent sessions from sub-sessions
			parentSessions := []*Instance{}
			subSessionsByParent := make(map[string][]*Instance) // parentID -> sub-sessions

			for _, sess := range group.Sessions {
				if sess.IsSubSession() {
					subSessionsByParent[sess.ParentSessionID] = append(subSessionsByParent[sess.ParentSessionID], sess)
				} else {
					parentSessions = append(parentSessions, sess)
				}
			}

			// Apply pin ordering live (pin-sessions): a pin edit mutates
			// Instance.Pin but does not rebuild the tree, so the load-time
			// SortInstancesByActionable has not re-run. Stable-partition the
			// display slices by pin zone here so a pinned session moves to the
			// top/bottom of its group immediately — without this, the pin only
			// takes effect after a restart. Operates on Flatten's local copies,
			// never the tree's group.Sessions, and preserves unpinned order.
			stablePinPartition(parentSessions)
			for parentID := range subSessionsByParent {
				stablePinPartition(subSessionsByParent[parentID])
			}

			// Count total top-level items (parent sessions + orphan sub-sessions whose parent is in different group)
			// For determining IsLastInGroup, we need to know how many top-level items there are
			topLevelCount := len(parentSessions)
			for parentID, subs := range subSessionsByParent {
				// Check if parent is in this group
				parentInGroup := false
				for _, p := range parentSessions {
					if p.ID == parentID {
						parentInGroup = true
						break
					}
				}
				if !parentInGroup {
					// Parent is not in this group, so sub-sessions appear as top-level
					topLevelCount += len(subs)
				}
			}

			topLevelIndex := 0
			for _, sess := range parentSessions {
				isLastTopLevel := topLevelIndex == topLevelCount-1

				// Get sub-sessions for this parent
				subs := subSessionsByParent[sess.ID]
				// If this session has sub-sessions, it's not the last in group visually
				isLastInGroup := isLastTopLevel && len(subs) == 0

				items = append(items, Item{
					Type:          ItemTypeSession,
					Session:       sess,
					Level:         groupLevel + 1,
					Path:          group.Path,
					IsLastInGroup: isLastInGroup,
				})

				// Add sub-sessions immediately after parent
				for subIdx, sub := range subs {
					isLastSub := subIdx == len(subs)-1
					// Sub-session is last in group if parent was last top-level and this is last sub
					isSubLastInGroup := isLastTopLevel && isLastSub

					items = append(items, Item{
						Type:                ItemTypeSession,
						Session:             sub,
						Level:               groupLevel + 2, // One more level of indentation
						Path:                group.Path,
						IsLastInGroup:       isSubLastInGroup,
						IsSubSession:        true,
						IsLastSubSession:    isLastSub,
						ParentIsLastInGroup: isLastTopLevel, // For tree line rendering (│ vs spaces)
					})
				}

				// Remove these subs from the map so we don't add them again
				delete(subSessionsByParent, sess.ID)

				topLevelIndex++
			}

			// Add any orphaned sub-sessions (parent not in this group). Collect
			// the remaining map entries into a slice and sort by Order so the
			// emission order is deterministic — iterating subSessionsByParent
			// directly would use Go's randomized map order and shuffle these
			// rows between renders.
			orphans := make([]*Instance, 0, len(subSessionsByParent))
			for _, subs := range subSessionsByParent {
				orphans = append(orphans, subs...)
			}
			sort.SliceStable(orphans, func(i, j int) bool {
				if orphans[i].Order != orphans[j].Order {
					return orphans[i].Order < orphans[j].Order
				}
				// Tie-break on ID so equal-Order orphans (collected from the
				// randomized subSessionsByParent map) still emit in a stable,
				// run-independent order rather than leaking map-iteration order.
				return orphans[i].ID < orphans[j].ID
			})
			for _, sub := range orphans {
				topLevelIndex++
				items = append(items, Item{
					Type:          ItemTypeSession,
					Session:       sub,
					Level:         groupLevel + 1,
					Path:          group.Path,
					IsLastInGroup: topLevelIndex == topLevelCount,
					IsSubSession:  true, // Still a sub-session, just orphaned in this group
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

// MoveSessionUp moves a session up among its visual siblings: top-level
// sessions (empty ParentSessionID) reorder among other top-level sessions
// in the same group; sub-sessions reorder among other sub-sessions of the
// same parent. Non-siblings interleaved in the flat slice are skipped.
//
// When a sub-session has no previous same-parent sibling (it is at the
// top of its parent's children block), it is promoted to top-level and
// inserted in the slice immediately before the parent. At the group's
// top boundary (no previous top-level peer) the call is a no-op;
// cross-group moves remain on the M shortcut.
func (t *GroupTree) MoveSessionUp(inst *Instance) {
	group, exists := t.Groups[inst.GroupPath]
	if !exists {
		return
	}

	currentIdx, prevSiblingIdx, parentIdx := -1, -1, -1
	for i, s := range group.Sessions {
		if s.ID == inst.ID {
			currentIdx = i
			continue
		}
		if currentIdx < 0 && s.ParentSessionID == inst.ParentSessionID {
			prevSiblingIdx = i
		}
		if inst.ParentSessionID != "" && s.ID == inst.ParentSessionID {
			parentIdx = i
		}
	}
	if currentIdx < 0 {
		return
	}

	switch {
	case prevSiblingIdx >= 0:
		group.Sessions[currentIdx], group.Sessions[prevSiblingIdx] = group.Sessions[prevSiblingIdx], group.Sessions[currentIdx]
	case inst.ParentSessionID != "" && parentIdx >= 0:
		// Promote sub-session to top-level: clear parent and reposition
		// the slice entry immediately before the parent so the renderer
		// shows it as a top-level peer just above the parent's block.
		inst.ClearParent()
		s := group.Sessions[currentIdx]
		group.Sessions = append(group.Sessions[:currentIdx], group.Sessions[currentIdx+1:]...)
		if parentIdx > currentIdx {
			parentIdx--
		}
		group.Sessions = append(group.Sessions[:parentIdx], append([]*Instance{s}, group.Sessions[parentIdx:]...)...)
	default:
		return
	}

	for i, s := range group.Sessions {
		s.Order = i
	}
}

// MoveSessionDown moves a session down among its visual siblings.
// See MoveSessionUp for the sibling-aware semantics; this is the symmetric
// case that swaps with the next same-parent session in the slice.
//
// When a sub-session has no following same-parent sibling (it is at the
// bottom of its parent's children block), it is promoted to top-level
// at its current slice position so the renderer shows it as a top-level
// peer immediately after the parent's block. At the group's bottom
// boundary (no following top-level peer) the call is a no-op.
func (t *GroupTree) MoveSessionDown(inst *Instance) {
	group, exists := t.Groups[inst.GroupPath]
	if !exists {
		return
	}

	currentIdx, nextSiblingIdx := -1, -1
	for i, s := range group.Sessions {
		if s.ID == inst.ID {
			currentIdx = i
			continue
		}
		if currentIdx >= 0 && s.ParentSessionID == inst.ParentSessionID && nextSiblingIdx < 0 {
			nextSiblingIdx = i
		}
	}
	if currentIdx < 0 {
		return
	}

	switch {
	case nextSiblingIdx >= 0:
		group.Sessions[currentIdx], group.Sessions[nextSiblingIdx] = group.Sessions[nextSiblingIdx], group.Sessions[currentIdx]
	case inst.ParentSessionID != "":
		// Promote sub-session to top-level. The slice entry already sits
		// after the parent's other children, so just clearing the parent
		// pointer is enough — the renderer will place it as a top-level
		// peer immediately after the parent's block.
		inst.ClearParent()
	default:
		return
	}

	for i, s := range group.Sessions {
		s.Order = i
	}
}

// PromoteSession converts a sub-session into a top-level peer in the same
// group. Slice position is preserved so the renderer places the session as
// a top-level peer immediately after its former parent's children block.
// Top-level sessions are unchanged.
func (t *GroupTree) PromoteSession(inst *Instance) {
	if inst.ParentSessionID == "" {
		return
	}
	group, exists := t.Groups[inst.GroupPath]
	if !exists {
		return
	}
	inst.ClearParent()
	for i, s := range group.Sessions {
		s.Order = i
	}
}

// DemoteSession converts a top-level session into a sub-session of the
// previous top-level peer in the same group, inserting it as that peer's
// last child. No-op if there is no previous peer (group's first
// top-level), if the session is already a sub-session, or if it has its
// own children — single-level nesting only, mirroring the validation in
// `session set-parent`.
func (t *GroupTree) DemoteSession(inst *Instance) {
	if inst.ParentSessionID != "" {
		return
	}
	group, exists := t.Groups[inst.GroupPath]
	if !exists {
		return
	}

	for _, s := range group.Sessions {
		if s.ParentSessionID == inst.ID {
			return
		}
	}

	currentIdx, prevTopIdx := -1, -1
	for i, s := range group.Sessions {
		if s.ID == inst.ID {
			currentIdx = i
			break
		}
		if s.ParentSessionID == "" {
			prevTopIdx = i
		}
	}
	if currentIdx < 0 || prevTopIdx < 0 {
		return
	}

	parent := group.Sessions[prevTopIdx]
	inst.SetParentWithPath(parent.ID, parent.ProjectPath)

	insertIdx := prevTopIdx + 1
	for i := prevTopIdx + 1; i < len(group.Sessions); i++ {
		if i == currentIdx {
			continue
		}
		if group.Sessions[i].ParentSessionID == parent.ID {
			insertIdx = i + 1
		}
	}

	s := group.Sessions[currentIdx]
	group.Sessions = append(group.Sessions[:currentIdx], group.Sessions[currentIdx+1:]...)
	if insertIdx > currentIdx {
		insertIdx--
	}
	group.Sessions = append(group.Sessions[:insertIdx], append([]*Instance{s}, group.Sessions[insertIdx:]...)...)

	for i, s := range group.Sessions {
		s.Order = i
	}
}

// MoveSessionToGroup moves a session to a different group
func (t *GroupTree) MoveSessionToGroup(inst *Instance, newGroupPath string) {
	oldGroupPath := inst.GroupPath

	// Remove from old group
	if oldGroup, exists := t.Groups[oldGroupPath]; exists {
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
	inst.Order = len(newGroup.Sessions)
	newGroup.Sessions = append(newGroup.Sessions, inst)

	// Update default paths for both old and new groups
	t.updateGroupDefaultPath(oldGroupPath)
	t.updateGroupDefaultPath(newGroupPath)
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

// newGroupMaxConcurrent resolves the MaxConcurrent assigned to a group made
// via CreateGroup/CreateSubgroup. nil tree-default → 1 (serial, the v1.9.1
// built-in). A configured value is used as-is (0 = unlimited, N = cap).
func (t *GroupTree) newGroupMaxConcurrent() int {
	if t.DefaultMaxConcurrent != nil {
		return *t.DefaultMaxConcurrent
	}
	return 1
}

// CreateGroup creates a new empty group
func (t *GroupTree) CreateGroup(name string) *Group {
	// Sanitize name to prevent path traversal and security issues
	sanitizedName := sanitizeGroupName(name)
	path := strings.ReplaceAll(sanitizedName, " ", "-")
	if _, exists := t.Groups[path]; exists {
		return t.Groups[path]
	}

	// Count existing root-level groups to assign sibling-relative order
	rootCount := 0
	for p := range t.Groups {
		if getParentPath(p) == "" { // Root level
			rootCount++
		}
	}

	group := &Group{
		Name:     sanitizedName,
		Path:     path,
		Expanded: true,
		Sessions: []*Instance{},
		Order:    rootCount, // Order among root groups
		// v1.9.1: newly-created groups default to serial (max_concurrent=1)
		// to prevent the parallel-worker cascade observed on 2026-05-08.
		// Pre-existing groups loaded via NewGroupTreeWithGroups keep their
		// stored MaxConcurrent (0 → unlimited for backward compat).
		// [group_defaults].max_concurrent can override this default via the
		// DefaultMaxConcurrent the caller seeds; nil keeps the serial 1.
		MaxConcurrent: t.newGroupMaxConcurrent(),
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
	childPath := strings.ReplaceAll(sanitizedName, " ", "-")
	fullPath := parentPath + "/" + childPath

	if _, exists := t.Groups[fullPath]; exists {
		return t.Groups[fullPath]
	}

	// Count existing siblings to assign sibling-relative order
	siblingCount := 0
	for p := range t.Groups {
		if getParentPath(p) == parentPath {
			siblingCount++
		}
	}

	group := &Group{
		Name:     sanitizedName,
		Path:     fullPath,
		Expanded: true,
		Sessions: []*Instance{},
		Order:    siblingCount, // Order among siblings
		// v1.9.1: subgroups also default to serial. See CreateGroup.
		// [group_defaults].max_concurrent overrides via DefaultMaxConcurrent.
		MaxConcurrent: t.newGroupMaxConcurrent(),
	}
	t.Groups[fullPath] = group
	t.Expanded[fullPath] = true
	t.rebuildGroupList()
	return group
}

// CreateGroupPath ensures every level of a (possibly nested) group path exists,
// creating any missing intermediate groups, and returns the leaf group.
//
// Unlike CreateGroup, it treats "/" as a path separator instead of letting
// sanitizeGroupName flatten it into a hyphen, so "work/bar" creates "work" and
// "work/bar" rather than a single flat "work-bar" group (see issue #1357).
func (t *GroupTree) CreateGroupPath(path string) *Group {
	var parentPath string
	var leaf *Group
	for _, segment := range strings.Split(path, "/") {
		if strings.TrimSpace(segment) == "" {
			continue // tolerate leading/trailing/duplicate separators
		}
		if parentPath == "" {
			leaf = t.CreateGroup(segment)
		} else {
			leaf = t.CreateSubgroup(parentPath, segment)
		}
		if leaf == nil {
			return nil
		}
		parentPath = leaf.Path
	}
	return leaf
}

// RenameTargetPath returns the group path that RenameGroup(oldPath, newName)
// would move the group to, applying the same sanitization and parent-path
// preservation. Exposed so callers can detect a collision with an existing,
// different group at the target before renaming (see the reload-race reapply).
func (t *GroupTree) RenameTargetPath(oldPath, newName string) string {
	newBasePath := strings.ReplaceAll(sanitizeGroupName(newName), " ", "-")
	if parentPath := getParentPath(oldPath); parentPath != "" {
		return parentPath + "/" + newBasePath
	}
	return newBasePath
}

// RenameGroup renames a group and updates all subgroups.
// Returns ErrGroupNotFound if oldPath doesn't exist, or ErrGroupAlreadyExists if the target path collides.
func (t *GroupTree) RenameGroup(oldPath, newName string) error {
	group, exists := t.Groups[oldPath]
	if !exists {
		return fmt.Errorf("%w: %s", ErrGroupNotFound, oldPath)
	}

	// Sanitize name to prevent path traversal and security issues
	sanitizedName := sanitizeGroupName(newName)
	newPath := t.RenameTargetPath(oldPath, newName)

	if newPath == oldPath {
		group.Name = sanitizedName
		return nil
	}

	if _, clash := t.Groups[newPath]; clash {
		return fmt.Errorf("%w: %s", ErrGroupAlreadyExists, newPath)
	}
	for path := range t.Groups {
		if strings.HasPrefix(path, oldPath+"/") {
			newSubPath := newPath + path[len(oldPath):]
			if _, clash := t.Groups[newSubPath]; clash {
				return fmt.Errorf("%w: %s", ErrGroupAlreadyExists, newSubPath)
			}
		}
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
	return nil
}

// MoveGroupTo reparents a group (and its entire subtree) under destParentPath.
// An empty destParentPath promotes the group to root level. Returns an error
// for: unknown source, source == DefaultGroupPath, unknown destParent,
// destParent == source or its descendant (circular), or a collision at the
// target path. Same-parent call is a no-op.
//
// This is the engine behind the #447 "group change" CLI / TUI.
func (t *GroupTree) MoveGroupTo(sourcePath, destParentPath string) error {
	if sourcePath == "" {
		return fmt.Errorf("source group path is required")
	}
	if sourcePath == DefaultGroupPath {
		return fmt.Errorf("the default group %q cannot be moved", DefaultGroupPath)
	}

	src, ok := t.Groups[sourcePath]
	if !ok {
		return fmt.Errorf("source group %q does not exist", sourcePath)
	}

	if destParentPath != "" {
		if _, ok := t.Groups[destParentPath]; !ok {
			return fmt.Errorf("destination parent group %q does not exist", destParentPath)
		}
	}

	if destParentPath == sourcePath ||
		strings.HasPrefix(destParentPath, sourcePath+"/") {
		return fmt.Errorf("cannot move %q under itself or its descendant %q", sourcePath, destParentPath)
	}

	baseName := sourcePath
	if idx := strings.LastIndex(sourcePath, "/"); idx >= 0 {
		baseName = sourcePath[idx+1:]
	}
	currentParent := getParentPath(sourcePath)
	if currentParent == destParentPath {
		return nil
	}

	newPath := baseName
	if destParentPath != "" {
		newPath = destParentPath + "/" + baseName
	}
	if _, collide := t.Groups[newPath]; collide {
		return fmt.Errorf("target path %q already exists", newPath)
	}

	subgroupsToRewrite := make(map[string]*Group)
	for path, g := range t.Groups {
		if strings.HasPrefix(path, sourcePath+"/") {
			newSubPath := newPath + path[len(sourcePath):]
			if _, collide := t.Groups[newSubPath]; collide {
				return fmt.Errorf("target subpath %q already exists", newSubPath)
			}
			for _, sess := range g.Sessions {
				sess.GroupPath = newSubPath
			}
			g.Path = newSubPath
			subgroupsToRewrite[path] = g
		}
	}

	for _, sess := range src.Sessions {
		sess.GroupPath = newPath
	}
	src.Path = newPath

	delete(t.Groups, sourcePath)
	t.Groups[newPath] = src
	expanded := t.Expanded[sourcePath]
	delete(t.Expanded, sourcePath)
	t.Expanded[newPath] = expanded

	for oldSubPath, g := range subgroupsToRewrite {
		delete(t.Groups, oldSubPath)
		t.Groups[g.Path] = g
		e := t.Expanded[oldSubPath]
		delete(t.Expanded, oldSubPath)
		t.Expanded[g.Path] = e
	}

	t.rebuildGroupList()
	return nil
}

// DeleteGroup deletes a group, all its subgroups, and moves all sessions to default
func (t *GroupTree) DeleteGroup(path string) []*Instance {
	group, exists := t.Groups[path]
	if !exists || path == DefaultGroupPath {
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

	// Only touch the default group when there are sessions that need a new home.
	// Otherwise deleting an empty non-default group would spuriously materialize
	// a "My Sessions" group the user never asked for.
	if len(allMovedSessions) > 0 {
		for _, sess := range allMovedSessions {
			sess.GroupPath = DefaultGroupPath
		}

		defaultGroup, exists := t.Groups[DefaultGroupPath]
		if !exists {
			defaultGroup = &Group{
				Name:     DefaultGroupName,
				Path:     DefaultGroupPath,
				Expanded: true,
				Sessions: []*Instance{},
			}
			t.Groups[DefaultGroupPath] = defaultGroup
		}
		defaultGroup.Sessions = append(defaultGroup.Sessions, allMovedSessions...)
	}

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

// SessionCountForGroup returns session count for a group INCLUDING all its subgroups
// This enables hierarchical counts like "Project (5)" where 5 includes all nested sessions
func (t *GroupTree) SessionCountForGroup(groupPath string) int {
	count := 0
	for path, g := range t.Groups {
		// Count this group if it matches OR is a subgroup (prefix match)
		if path == groupPath || strings.HasPrefix(path, groupPath+"/") {
			count += len(g.Sessions)
		}
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
		groupPath = DefaultGroupPath
		inst.GroupPath = groupPath
	}

	group, exists := t.Groups[groupPath]
	if !exists {
		// Ensure parent groups exist for hierarchical paths
		t.ensureParentGroupsExist(groupPath)
		// Use proper name for default group, otherwise extract name from path
		name := extractGroupName(groupPath)
		if groupPath == DefaultGroupPath {
			name = DefaultGroupName
		}
		group = &Group{
			Name:     name,
			Path:     groupPath,
			Expanded: true,
			Sessions: []*Instance{},
			Order:    len(t.GroupList),
		}
		t.Groups[groupPath] = group
		t.Expanded[groupPath] = true
		t.rebuildGroupList()
	}
	inst.Order = len(group.Sessions)
	group.Sessions = append(group.Sessions, inst)
	t.updateGroupDefaultPath(groupPath)
}

// RemoveSession removes a session from its group
func (t *GroupTree) RemoveSession(inst *Instance) {
	groupPath := inst.GroupPath
	if groupPath == "" {
		groupPath = DefaultGroupPath
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
			groupPath = DefaultGroupPath
			inst.GroupPath = groupPath
		}

		group, exists := t.Groups[groupPath]
		if !exists {
			// Ensure parent groups exist for hierarchical paths
			t.ensureParentGroupsExist(groupPath)
			// Create new group for this session's path
			// Use proper name for default group, otherwise extract name from path
			name := extractGroupName(groupPath)
			if groupPath == DefaultGroupPath {
				name = DefaultGroupName
			}
			group = &Group{
				Name:     name,
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

	// Sort sessions within each group by persisted Order
	for _, group := range t.Groups {
		sort.SliceStable(group.Sessions, func(i, j int) bool {
			return group.Sessions[i].Order < group.Sessions[j].Order
		})
	}

	// Always rebuild GroupList at the end to ensure consistency between
	// Groups map and GroupList slice. This fixes the bug where flatItems
	// could be empty while instances has data (filter bar shows counts
	// but main panel shows "No Sessions Yet").
	t.rebuildGroupList()

	// Update default paths for all groups after syncing
	for groupPath := range t.Groups {
		t.updateGroupDefaultPath(groupPath)
	}
}

// ShallowCopyForSave creates a copy of the GroupTree that's safe to use
// from a goroutine for saving purposes. It deep copies the Group structs
// to prevent data races when the main thread modifies group fields
// (Name, Path, Expanded, Order) while a background goroutine reads them.
func (t *GroupTree) ShallowCopyForSave() *GroupTree {
	if t == nil {
		return nil
	}

	// Deep copy Group structs to prevent data races
	// The save goroutine reads Name, Path, Expanded, Order fields
	// which could be modified by the main thread (e.g., renaming, collapsing)
	groupListCopy := make([]*Group, len(t.GroupList))
	for i, g := range t.GroupList {
		groupListCopy[i] = &Group{
			Name:          g.Name,
			Path:          g.Path,
			Expanded:      g.Expanded,
			Order:         g.Order,
			DefaultPath:   g.DefaultPath,
			MaxConcurrent: g.MaxConcurrent,
			// Don't copy Sessions - not needed for save, only metadata is saved
		}
	}

	return &GroupTree{
		GroupList: groupListCopy,
		// Groups and Expanded maps not needed since only GroupList is iterated in save
	}
}

// mostRecentPathForSessions returns the project path from the most recently accessed session
// in the given list. Returns empty string if no sessions have paths.
func mostRecentPathForSessions(sessions []*Instance) string {
	if len(sessions) == 0 {
		return ""
	}

	var mostRecent *Instance
	for _, s := range sessions {
		if s.ProjectPath == "" {
			continue
		}
		if mostRecent == nil || s.LastAccessedAt.After(mostRecent.LastAccessedAt) {
			mostRecent = s
		}
	}

	if mostRecent != nil {
		// Prefer the original repo root over a worktree path so the new-session
		// dialog doesn't pre-populate with a path that may not exist.
		if mostRecent.WorktreeRepoRoot != "" {
			return mostRecent.WorktreeRepoRoot
		}
		return mostRecent.ProjectPath
	}
	return ""
}

// resolveGroupDefaultPath normalizes a default path and maps linked git
// worktree paths to their base repository root.
func resolveGroupDefaultPath(defaultPath string) string {
	defaultPath = strings.TrimSpace(defaultPath)
	if defaultPath == "" {
		return ""
	}

	// Expand ~ for user-supplied paths.
	if defaultPath == "~" || strings.HasPrefix(defaultPath, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if defaultPath == "~" {
				defaultPath = home
			} else {
				defaultPath = filepath.Join(home, defaultPath[2:])
			}
		}
	}

	if !filepath.IsAbs(defaultPath) {
		if abs, err := filepath.Abs(defaultPath); err == nil {
			defaultPath = abs
		}
	}

	info, err := os.Stat(defaultPath)
	if err != nil || !info.IsDir() {
		return defaultPath
	}

	if !git.IsGitRepo(defaultPath) {
		return defaultPath
	}

	// Only collapse LINKED worktrees (`git worktree add`) to their base
	// repository root — a transient worktree path shouldn't become the stored
	// default. A plain subdirectory inside the main working tree is a
	// legitimate default path, so store it verbatim: GetWorktreeBaseRoot would
	// otherwise map it to the repo root via GetRepoRoot.
	if !git.IsLinkedWorktree(defaultPath) {
		return defaultPath
	}

	baseRoot, err := git.GetWorktreeBaseRoot(defaultPath)
	if err != nil || baseRoot == "" {
		return defaultPath
	}

	return baseRoot
}

// DefaultPathForGroup returns the effective default path for creating new sessions
// in the group: explicit configured default_path first, then most recent session path.
func (t *GroupTree) DefaultPathForGroup(groupPath string) string {
	group, exists := t.Groups[groupPath]
	if !exists {
		return ""
	}

	if group.DefaultPath != "" {
		return resolveGroupDefaultPath(group.DefaultPath)
	}

	return resolveGroupDefaultPath(mostRecentPathForSessions(group.Sessions))
}

// SetDefaultPathForGroup sets (or clears) an explicit default path for a group.
func (t *GroupTree) SetDefaultPathForGroup(groupPath, defaultPath string) bool {
	group, exists := t.Groups[groupPath]
	if !exists {
		return false
	}

	group.DefaultPath = resolveGroupDefaultPath(defaultPath)
	return true
}

// updateGroupDefaultPath normalizes persisted explicit default paths.
// Derived fallback paths are computed on demand in DefaultPathForGroup().
func (t *GroupTree) updateGroupDefaultPath(groupPath string) {
	group, exists := t.Groups[groupPath]
	if !exists {
		return
	}

	if group.DefaultPath != "" {
		group.DefaultPath = resolveGroupDefaultPath(group.DefaultPath)
	}
}
