package session

// Group concurrency control (v1.9.1).
//
// Motivation: on 2026-05-08, a 9-worker parallel launch into a single group
// triggered systemd-oomd to kill the entire conductor cgroup. The Factory
// Missions research established that serial-within-group is the working
// pattern; parallel agents conflict and duplicate work. v1.9.1 makes serial
// the default for newly-created groups while preserving backward compat for
// pre-existing groups (which keep their stored MaxConcurrent = 0 = unlimited).
//
// MaxConcurrent semantics:
//
//	<= 0  -> unlimited (legacy default; explicit opt-out)
//	   1  -> serial (default for groups created post-v1.9.1)
//	   N  -> cap at N simultaneous running sessions in the group

// IsAtCap reports whether the running count has reached the cap for a group.
// max <= 0 is treated as unlimited (never at cap).
func IsAtCap(running, max int) bool {
	if max <= 0 {
		return false
	}
	return running >= max
}

// CountRunningInGroup returns the number of instances in the given group whose
// status is StatusRunning. Queued, stopped, and other-group instances are not
// counted.
func CountRunningInGroup(instances []*Instance, groupPath string) int {
	n := 0
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		if inst.GroupPath == groupPath && inst.Status == StatusRunning {
			n++
		}
	}
	return n
}

// ShouldQueue reports whether a new launch into groupPath must be queued
// because the group is at its MaxConcurrent cap.
func ShouldQueue(instances []*Instance, groupPath string, maxConcurrent int) bool {
	return IsAtCap(CountRunningInGroup(instances, groupPath), maxConcurrent)
}

// FindNextQueued returns the oldest queued instance in the given group, or
// nil if none are queued. "Oldest" is by CreatedAt (FIFO drain order).
func FindNextQueued(instances []*Instance, groupPath string) *Instance {
	var oldest *Instance
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		if inst.GroupPath != groupPath || inst.Status != StatusQueued {
			continue
		}
		if oldest == nil || inst.CreatedAt.Before(oldest.CreatedAt) {
			oldest = inst
		}
	}
	return oldest
}

// GroupMaxConcurrent returns the effective max_concurrent cap for groupPath
// by looking it up in the group tree. Returns 0 (unlimited) when the group is
// not found, preserving legacy behavior for groups without persisted metadata.
func GroupMaxConcurrent(tree *GroupTree, groupPath string) int {
	if tree == nil || groupPath == "" {
		return 0
	}
	if g, ok := tree.Groups[groupPath]; ok && g != nil {
		return g.MaxConcurrent
	}
	return 0
}
