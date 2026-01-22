package session

import (
	"crypto/sha256"
	"encoding/hex"
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
	Error      error
	NewCount   int // Number of newly discovered sessions
	StaleCount int // Number of sessions removed (no longer on remote)
}

// agentDeckSessionPattern matches agentdeck_<title>_<8-hex-chars> tmux session names
var agentDeckSessionPattern = regexp.MustCompile(`^agentdeck_(.+)_([0-9a-f]{8})$`)

// DiscoverRemoteTmuxSessions discovers agentdeck_* sessions from all configured SSH hosts
// that have auto_discover enabled. It returns newly discovered instances, stale instance IDs,
// and any errors per host. Existing sessions (matched by deterministic ID) are skipped.
func DiscoverRemoteTmuxSessions(existing []*Instance) ([]*Instance, []string, map[string]error) {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return nil, nil, map[string]error{"config": err}
	}

	settings := GetRemoteDiscoverySettings()
	if !settings.Enabled {
		return nil, nil, nil
	}

	var discovered []*Instance
	var allStaleIDs []string
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

			sessions, staleIDs, err := DiscoverRemoteSessionsForHost(hID, existing)
			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				errors[hID] = err
				log.Printf("[REMOTE-DISCOVERY] Error discovering sessions on %s: %v", hID, err)
				return
			}

			discovered = append(discovered, sessions...)
			allStaleIDs = append(allStaleIDs, staleIDs...)
			if len(sessions) > 0 {
				log.Printf("[REMOTE-DISCOVERY] Discovered %d sessions on %s", len(sessions), hID)
			}
			if len(staleIDs) > 0 {
				log.Printf("[REMOTE-DISCOVERY] Found %d stale sessions on %s", len(staleIDs), hID)
			}
		}(hostID, hostDef)
	}

	wg.Wait()
	return discovered, allStaleIDs, errors
}

// DiscoverRemoteSessionsForHost discovers agentdeck_* sessions from a specific SSH host
// Returns discovered instances, stale instance IDs (sessions that no longer exist), and error
func DiscoverRemoteSessionsForHost(hostID string, existing []*Instance) ([]*Instance, []string, error) {
	// Get SSH executor
	sshExec, err := tmux.NewSSHExecutorFromPool(hostID)
	if err != nil {
		return nil, nil, err
	}

	// List remote tmux sessions with working directory info
	remoteSessions, err := listRemoteTmuxSessions(sshExec)
	if err != nil {
		return nil, nil, err
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

	var discovered []*Instance

	for _, rs := range remoteSessions {
		// Only process agentdeck_* sessions
		if !strings.HasPrefix(rs.Name, "agentdeck_") {
			continue
		}

		// Generate deterministic ID
		remoteID := GenerateRemoteInstanceID(hostID, rs.Name)

		// Skip if already exists
		if _, exists := existingByRemoteID[remoteID]; exists {
			continue
		}

		// Parse title from tmux session name
		title := ParseTitleFromTmuxName(rs.Name)

		// Create new instance for discovered session
		inst := &Instance{
			ID:             remoteID,
			Title:          title,
			ProjectPath:    rs.WorkingDir,
			GroupPath:      groupPrefix + "/" + hostID,
			Tool:           "claude", // Assume Claude since it's agentdeck_*
			Status:         StatusIdle,
			CreatedAt:      time.Now(),
			RemoteHost:     hostID,
			RemoteTmuxName: rs.Name,
		}

		// Set up tmux session with SSH executor
		tmuxSess := tmux.NewSessionWithExecutor(rs.Name, rs.WorkingDir, sshExec)
		tmuxSess.InstanceID = remoteID
		inst.SetTmuxSession(tmuxSess)

		discovered = append(discovered, inst)
		log.Printf("[REMOTE-DISCOVERY] Discovered: %s on %s -> %s", rs.Name, hostID, title)
	}

	return discovered, staleIDs, nil
}

// listRemoteTmuxSessions lists tmux sessions on a remote host with working directory info
func listRemoteTmuxSessions(exec *tmux.SSHExecutor) ([]RemoteTmuxSession, error) {
	// Use ListSessionsWithInfo to get session name and working directory
	sessionsInfo, err := exec.ListSessionsWithInfo()
	if err != nil {
		return nil, err
	}

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
	if matches == nil || len(matches) < 2 {
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
