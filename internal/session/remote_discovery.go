package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// RemoteTmuxSession represents a discovered tmux session on a remote host
type RemoteTmuxSession struct {
	Name       string // tmux session name (e.g., "agentdeck_my-project_a1b2c3d4")
	WorkingDir string // Working directory of the session
	Activity   int64  // Last activity timestamp
}

// DiscoveryResult contains results from a single host discovery
type DiscoveryResult struct {
	HostID     string
	Sessions   []*Instance
	Groups     []*GroupData // Groups discovered from remote
	Error      error
	NewCount   int // Number of newly discovered sessions
	StaleCount int // Number of sessions removed (no longer on remote)
}

// UpdatedInstance tracks an instance whose metadata was updated during discovery
type UpdatedInstance struct {
	Instance     *Instance
	OldGroupPath string
	NewGroupPath string
}

// RemoteStorageSnapshot contains data fetched from remote sessions.json
type RemoteStorageSnapshot struct {
	Groups            []*GroupData      // Remote's group definitions
	SessionGroupPaths map[string]string // tmux_session name -> group_path mapping
	SessionTools      map[string]string // tmux_session name -> tool mapping
}

// agentDeckSessionPattern matches agentdeck_<title>_<8-hex-chars> tmux session names
var agentDeckSessionPattern = regexp.MustCompile(`^agentdeck_(.+)_([0-9a-f]{8})$`)

// FetchRemoteStorageSnapshot reads the remote's sessions.json to get group structure
// Returns nil on errors (gracefully degrades to flat structure)
func FetchRemoteStorageSnapshot(sshExec *tmux.SSHExecutor) *RemoteStorageSnapshot {
	// Read remote sessions.json
	output, err := sshExec.RunCommand("cat ~/.agent-deck/profiles/default/sessions.json 2>/dev/null || echo '{}'")
	if err != nil {
		log.Printf("[REMOTE-DISCOVERY] Failed to read remote sessions.json: %v", err)
		return nil
	}

	output = strings.TrimSpace(output)
	if output == "" || output == "{}" {
		return nil
	}

	// Parse JSON
	var data StorageData
	if err := json.Unmarshal([]byte(output), &data); err != nil {
		log.Printf("[REMOTE-DISCOVERY] Failed to parse remote sessions.json: %v", err)
		return nil
	}

	// Build session-to-group and session-to-tool mappings
	// IMPORTANT: Skip sessions that are themselves remote (prevents circular discovery)
	// If machine A discovers B, and B has remote sessions from A, we must not re-discover those
	sessionGroupPaths := make(map[string]string)
	sessionTools := make(map[string]string)
	for _, inst := range data.Instances {
		// Skip remote-of-remote sessions to prevent circular loops
		if inst.RemoteHost != "" {
			continue
		}
		if inst.TmuxSession != "" {
			if inst.GroupPath != "" {
				sessionGroupPaths[inst.TmuxSession] = inst.GroupPath
			}
			if inst.Tool != "" {
				sessionTools[inst.TmuxSession] = inst.Tool
			}
		}
	}

	return &RemoteStorageSnapshot{
		Groups:            data.Groups,
		SessionGroupPaths: sessionGroupPaths,
		SessionTools:      sessionTools,
	}
}

// TransformRemoteGroupPath converts a remote group path to local path
// Example: "jeeves/workers" with prefix="remote" and hostname="jeeves" -> "remote/jeeves/jeeves/workers"
// Empty remote path maps to just "{prefix}/{hostname}"
// Remote-of-remote paths (starting with groupPrefix/) are mapped to host's root group
// to prevent circular nesting like "remote/host/remote/otherhost"
func TransformRemoteGroupPath(remoteGroupPath, groupPrefix, groupName string) string {
	if remoteGroupPath == "" || remoteGroupPath == DefaultGroupPath {
		// Remote's default group maps to host's root group
		return groupPrefix + "/" + groupName
	}
	// Skip paths that are themselves remote groups - map to host's root group
	if strings.HasPrefix(remoteGroupPath, groupPrefix+"/") {
		return groupPrefix + "/" + groupName
	}
	return groupPrefix + "/" + groupName + "/" + remoteGroupPath
}

// TransformRemoteGroups converts remote groups to local groups with transformed paths
// Filters out groups that are themselves remote groups (e.g., "remote/*") to prevent
// creating empty nested remote group hierarchies
func TransformRemoteGroups(remoteGroups []*GroupData, groupPrefix, groupName string) []*GroupData {
	if len(remoteGroups) == 0 {
		return nil
	}

	transformed := make([]*GroupData, 0, len(remoteGroups))
	for _, rg := range remoteGroups {
		// Skip the default group - we don't need to create it locally
		if rg.Path == DefaultGroupPath {
			continue
		}

		// Skip groups that are themselves remote groups on the remote host
		// These would create empty "remote/host/remote/otherhost" hierarchies
		if strings.HasPrefix(rg.Path, groupPrefix+"/") {
			continue
		}

		newPath := TransformRemoteGroupPath(rg.Path, groupPrefix, groupName)
		transformed = append(transformed, &GroupData{
			Name:     rg.Name,
			Path:     newPath,
			Expanded: rg.Expanded,
			Order:    rg.Order,
		})
	}

	return transformed
}

// DiscoverRemoteTmuxSessions discovers agentdeck_* sessions from all configured SSH hosts
// that have auto_discover enabled. It returns newly discovered instances, updated instances
// (existing sessions whose group paths changed), stale instance IDs, remote groups
// (transformed to local paths), and any errors per host.
func DiscoverRemoteTmuxSessions(existing []*Instance) ([]*Instance, []*UpdatedInstance, []string, []*GroupData, map[string]error) {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return nil, nil, nil, nil, map[string]error{"config": err}
	}

	settings := GetRemoteDiscoverySettings()
	if !settings.Enabled {
		return nil, nil, nil, nil, nil
	}

	var discovered []*Instance
	var allUpdated []*UpdatedInstance
	var allStaleIDs []string
	var allGroups []*GroupData
	errors := make(map[string]error)
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Process hosts concurrently
	for hostID, hostDef := range config.SSHHosts {
		if !hostDef.AutoDiscover {
			continue
		}

		wg.Add(1)
		go func(hID string, hDef SSHHostDef) {
			defer wg.Done()

			sessions, updated, staleIDs, groups, err := DiscoverRemoteSessionsForHost(hID, existing)
			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				errors[hID] = err
				log.Printf("[REMOTE-DISCOVERY] Error discovering sessions on %s: %v", hID, err)
				return
			}

			discovered = append(discovered, sessions...)
			allUpdated = append(allUpdated, updated...)
			allStaleIDs = append(allStaleIDs, staleIDs...)
			allGroups = append(allGroups, groups...)
			if len(sessions) > 0 {
				log.Printf("[REMOTE-DISCOVERY] Discovered %d sessions on %s", len(sessions), hID)
			}
			if len(updated) > 0 {
				log.Printf("[REMOTE-DISCOVERY] Updated %d session group paths on %s", len(updated), hID)
			}
			if len(staleIDs) > 0 {
				log.Printf("[REMOTE-DISCOVERY] Found %d stale sessions on %s", len(staleIDs), hID)
			}
			if len(groups) > 0 {
				log.Printf("[REMOTE-DISCOVERY] Discovered %d groups on %s", len(groups), hID)
			}
		}(hostID, hostDef)
	}

	wg.Wait()
	return discovered, allUpdated, allStaleIDs, allGroups, errors
}

// DiscoverRemoteSessionsForHost discovers agentdeck_* sessions from a specific SSH host
// Returns discovered instances, updated instances (group paths changed), stale instance IDs
// (sessions that no longer exist), transformed groups, and error
func DiscoverRemoteSessionsForHost(hostID string, existing []*Instance) ([]*Instance, []*UpdatedInstance, []string, []*GroupData, error) {
	// Get SSH executor
	sshExec, err := tmux.NewSSHExecutorFromPool(hostID)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	// List remote tmux sessions with working directory info
	remoteSessions, err := listRemoteTmuxSessions(sshExec)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	// Find stale sessions (ones we have locally but no longer exist on remote)
	staleIDs := FindStaleRemoteSessions(existing, hostID, remoteSessions)

	// Build lookup map of existing instances by their deterministic remote ID
	existingByRemoteID := make(map[string]*Instance)
	for _, inst := range existing {
		if inst.RemoteHost == hostID && inst.RemoteTmuxName != "" {
			remoteID := GenerateRemoteInstanceID(hostID, inst.RemoteTmuxName)
			existingByRemoteID[remoteID] = inst
		}
	}

	settings := GetRemoteDiscoverySettings()
	groupPrefix := settings.GroupPrefix
	if groupPrefix == "" {
		groupPrefix = "remote"
	}

	// Get host definition for display names (use hostID as fallback if not found)
	groupName := hostID
	if hostDef := GetSSHHostDef(hostID); hostDef != nil {
		groupName = hostDef.GetGroupName(hostID)
	}

	// Fetch remote storage snapshot to get group structure
	remoteSnapshot := FetchRemoteStorageSnapshot(sshExec)

	// Transform remote groups to local paths
	var transformedGroups []*GroupData
	if remoteSnapshot != nil {
		transformedGroups = TransformRemoteGroups(remoteSnapshot.Groups, groupPrefix, groupName)
	}

	var discovered []*Instance
	var updated []*UpdatedInstance

	for _, rs := range remoteSessions {
		// Only process agentdeck_* sessions
		if !strings.HasPrefix(rs.Name, "agentdeck_") {
			continue
		}

		// Generate deterministic ID
		remoteID := GenerateRemoteInstanceID(hostID, rs.Name)

		// Check if this session already exists locally
		if existingInst, exists := existingByRemoteID[remoteID]; exists {
			// Session exists - check if group path or tool needs updating based on remote's current state
			if remoteSnapshot != nil {
				remoteGroupPath := remoteSnapshot.SessionGroupPaths[rs.Name]
				newLocalPath := TransformRemoteGroupPath(remoteGroupPath, groupPrefix, groupName)

				// Update if the local group path differs from what the remote says it should be
				if existingInst.GroupPath != newLocalPath {
					oldPath := existingInst.GroupPath
					existingInst.GroupPath = newLocalPath
					updated = append(updated, &UpdatedInstance{
						Instance:     existingInst,
						OldGroupPath: oldPath,
						NewGroupPath: newLocalPath,
					})
					log.Printf("[REMOTE-DISCOVERY] Updated group path: %s on %s: %s -> %s",
						rs.Name, hostID, oldPath, newLocalPath)
				}

				// Sync tool from remote - trust remote's authoritative value
				if remoteTool := remoteSnapshot.SessionTools[rs.Name]; remoteTool != "" && existingInst.Tool != remoteTool {
					log.Printf("[REMOTE-DISCOVERY] Updated tool: %s on %s: %s -> %s",
						rs.Name, hostID, existingInst.Tool, remoteTool)
					existingInst.Tool = remoteTool
				}
			}
			continue
		}

		// Parse title from tmux session name
		title := ParseTitleFromTmuxName(rs.Name)

		// Determine group path and tool - use remote's values if available
		remoteGroupPath := ""
		remoteTool := "shell" // Default to shell if not in remote storage
		if remoteSnapshot != nil {
			remoteGroupPath = remoteSnapshot.SessionGroupPaths[rs.Name]
			if tool := remoteSnapshot.SessionTools[rs.Name]; tool != "" {
				remoteTool = tool
			}
		}
		localGroupPath := TransformRemoteGroupPath(remoteGroupPath, groupPrefix, groupName)

		// Create new instance for discovered session
		inst := &Instance{
			ID:             remoteID,
			Title:          title,
			ProjectPath:    rs.WorkingDir,
			GroupPath:      localGroupPath,
			Tool:           remoteTool,
			Status:         StatusIdle,
			CreatedAt:      time.Now(),
			RemoteHost:     hostID,
			RemoteTmuxName: rs.Name,
		}

		// Set up tmux session with SSH executor
		// Use ReconnectSessionWithExecutor to preserve the original tmux name (don't add prefix again)
		tmuxSess := tmux.ReconnectSessionWithExecutor(rs.Name, title, rs.WorkingDir, "", sshExec)
		tmuxSess.InstanceID = remoteID
		inst.SetTmuxSession(tmuxSess)

		discovered = append(discovered, inst)
		log.Printf("[REMOTE-DISCOVERY] Discovered: %s on %s -> %s (group: %s)", rs.Name, hostID, title, localGroupPath)
	}

	return discovered, updated, staleIDs, transformedGroups, nil
}

// listRemoteTmuxSessions lists tmux sessions on a remote host with working directory info
func listRemoteTmuxSessions(exec *tmux.SSHExecutor) ([]RemoteTmuxSession, error) {
	// Use ListSessionsWithInfo to get session name and working directory
	sessionsInfo, err := exec.ListSessionsWithInfo()
	if err != nil {
		return nil, err
	}

	// Populate remote session cache to reduce future SSH calls
	cacheData := make(map[string]int64)
	for name, info := range sessionsInfo {
		cacheData[name] = info.Activity
	}
	tmux.RefreshRemoteSessionCache(exec.HostID(), cacheData)

	var sessions []RemoteTmuxSession
	for name, info := range sessionsInfo {
		sessions = append(sessions, RemoteTmuxSession{
			Name:       name,
			WorkingDir: info.WorkingDir,
			Activity:   info.Activity,
		})
	}

	return sessions, nil
}

// GenerateRemoteInstanceID generates a deterministic instance ID for a remote session
// The ID is consistent across all machines that discover the same remote session
func GenerateRemoteInstanceID(hostID, tmuxName string) string {
	key := hostID + ":" + tmuxName
	hash := sha256.Sum256([]byte(key))
	return "remote-" + hex.EncodeToString(hash[:8])
}

// ParseTitleFromTmuxName extracts a human-readable title from an agentdeck tmux session name
// Example: "agentdeck_my-project-name_a1b2c3d4" -> "My Project Name"
func ParseTitleFromTmuxName(tmuxName string) string {
	matches := agentDeckSessionPattern.FindStringSubmatch(tmuxName)
	if len(matches) < 2 {
		// Fallback: just strip prefix if present
		name := strings.TrimPrefix(tmuxName, "agentdeck_")
		return toTitleCase(strings.ReplaceAll(name, "-", " "))
	}

	// Extract the middle part (title with hyphens)
	titlePart := matches[1]

	// Replace hyphens with spaces
	titleSpaced := strings.ReplaceAll(titlePart, "-", " ")

	// Convert to title case
	return toTitleCase(titleSpaced)
}

// toTitleCase converts a string to title case (first letter of each word capitalized)
func toTitleCase(s string) string {
	words := strings.Fields(s)
	for i, word := range words {
		if len(word) > 0 {
			runes := []rune(word)
			runes[0] = unicode.ToUpper(runes[0])
			for j := 1; j < len(runes); j++ {
				runes[j] = unicode.ToLower(runes[j])
			}
			words[i] = string(runes)
		}
	}
	return strings.Join(words, " ")
}

// FindByRemoteSession finds an existing instance by host ID and tmux session name
func FindByRemoteSession(instances []*Instance, hostID, tmuxName string) *Instance {
	targetID := GenerateRemoteInstanceID(hostID, tmuxName)
	for _, inst := range instances {
		if inst.ID == targetID {
			return inst
		}
	}
	return nil
}

// FindStaleRemoteSessions finds instances that reference remote sessions that no longer exist
// Returns instance IDs that should be removed from storage
func FindStaleRemoteSessions(instances []*Instance, hostID string, currentRemoteSessions []RemoteTmuxSession) []string {
	// Build set of current remote session names
	currentNames := make(map[string]bool)
	for _, rs := range currentRemoteSessions {
		currentNames[rs.Name] = true
	}

	var staleIDs []string
	for _, inst := range instances {
		// Only check instances for this specific host
		if inst.RemoteHost != hostID {
			continue
		}

		// If remote tmux name is not in current sessions, it's stale
		if inst.RemoteTmuxName != "" && !currentNames[inst.RemoteTmuxName] {
			staleIDs = append(staleIDs, inst.ID)
			log.Printf("[REMOTE-DISCOVERY] Stale session: %s (%s) on %s", inst.Title, inst.RemoteTmuxName, hostID)
		}
	}

	return staleIDs
}

// CleanupStaleRemoteSessions removes instances for remote sessions that no longer exist
// This is called after discovery to keep local storage in sync with remote state
func CleanupStaleRemoteSessions(instances []*Instance, hostID string, currentRemoteSessions []RemoteTmuxSession) []*Instance {
	staleIDs := FindStaleRemoteSessions(instances, hostID, currentRemoteSessions)
	if len(staleIDs) == 0 {
		return instances
	}

	staleSet := make(map[string]bool)
	for _, id := range staleIDs {
		staleSet[id] = true
	}

	var cleaned []*Instance
	for _, inst := range instances {
		if !staleSet[inst.ID] {
			cleaned = append(cleaned, inst)
		}
	}

	log.Printf("[REMOTE-DISCOVERY] Removed %d stale sessions from %s", len(staleIDs), hostID)
	return cleaned
}

// MergeDiscoveredSessions merges newly discovered remote sessions with existing instances
// Returns the merged list and the count of newly added sessions
func MergeDiscoveredSessions(existing []*Instance, discovered []*Instance) ([]*Instance, int) {
	if len(discovered) == 0 {
		return existing, 0
	}

	// Build set of existing IDs
	existingIDs := make(map[string]bool)
	for _, inst := range existing {
		existingIDs[inst.ID] = true
	}

	// Add only truly new sessions
	var newCount int
	merged := make([]*Instance, len(existing))
	copy(merged, existing)

	for _, inst := range discovered {
		if !existingIDs[inst.ID] {
			merged = append(merged, inst)
			newCount++
		}
	}

	return merged, newCount
}
