package session

import (
	"testing"
)

func TestGenerateRemoteInstanceID(t *testing.T) {
	tests := []struct {
		hostID   string
		tmuxName string
		wantLen  int // "remote-" + 16 hex chars = 23 chars
	}{
		{"host-195", "agentdeck_my-project_12345678", 23},
		{"server-1", "agentdeck_test_abcd1234", 23},
		{"prod", "agentdeck_long-project-name_fedcba98", 23},
	}

	for _, tt := range tests {
		t.Run(tt.hostID+"_"+tt.tmuxName, func(t *testing.T) {
			got := GenerateRemoteInstanceID(tt.hostID, tt.tmuxName)
			if len(got) != tt.wantLen {
				t.Errorf("GenerateRemoteInstanceID() length = %d, want %d", len(got), tt.wantLen)
			}
			if got[:7] != "remote-" {
				t.Errorf("GenerateRemoteInstanceID() should start with 'remote-', got %q", got[:7])
			}
		})
	}
}

func TestGenerateRemoteInstanceID_Deterministic(t *testing.T) {
	hostID := "host-195"
	tmuxName := "agentdeck_my-project_12345678"

	id1 := GenerateRemoteInstanceID(hostID, tmuxName)
	id2 := GenerateRemoteInstanceID(hostID, tmuxName)

	if id1 != id2 {
		t.Errorf("GenerateRemoteInstanceID() should be deterministic: %q != %q", id1, id2)
	}
}

func TestGenerateRemoteInstanceID_DifferentInputs(t *testing.T) {
	// Different hosts should produce different IDs
	id1 := GenerateRemoteInstanceID("host-1", "agentdeck_test_12345678")
	id2 := GenerateRemoteInstanceID("host-2", "agentdeck_test_12345678")

	if id1 == id2 {
		t.Error("Different hosts should produce different IDs")
	}

	// Different tmux names should produce different IDs
	id3 := GenerateRemoteInstanceID("host-1", "agentdeck_test_12345678")
	id4 := GenerateRemoteInstanceID("host-1", "agentdeck_other_12345678")

	if id3 == id4 {
		t.Error("Different tmux names should produce different IDs")
	}
}

func TestParseTitleFromTmuxName(t *testing.T) {
	tests := []struct {
		tmuxName string
		want     string
	}{
		// Standard agentdeck format: agentdeck_<title>_<8-hex>
		{"agentdeck_my-project_12345678", "My Project"},
		{"agentdeck_simple_abcd1234", "Simple"},
		{"agentdeck_multi-word-title_fedcba98", "Multi Word Title"},
		{"agentdeck_a-b-c_11223344", "A B C"},

		// Edge cases
		{"agentdeck_already-spaced_00000000", "Already Spaced"},
		{"agentdeck_UPPERCASE_ffffffff", "Uppercase"},
		{"agentdeck_single_99999999", "Single"},

		// Non-standard formats (fallback behavior)
		{"agentdeck_no-suffix", "No Suffix"},
		{"other-session", "Other Session"},
		{"plain", "Plain"},
	}

	for _, tt := range tests {
		t.Run(tt.tmuxName, func(t *testing.T) {
			got := ParseTitleFromTmuxName(tt.tmuxName)
			if got != tt.want {
				t.Errorf("ParseTitleFromTmuxName(%q) = %q, want %q", tt.tmuxName, got, tt.want)
			}
		})
	}
}

func TestToTitleCase(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello world", "Hello World"},
		{"HELLO WORLD", "Hello World"},
		{"hElLo WoRlD", "Hello World"},
		{"single", "Single"},
		{"", ""},
		{"a b c", "A B C"},
		{"already Title Case", "Already Title Case"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := toTitleCase(tt.input)
			if got != tt.want {
				t.Errorf("toTitleCase(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFindByRemoteSession(t *testing.T) {
	// Create test instances
	inst1 := &Instance{
		ID:             GenerateRemoteInstanceID("host-1", "agentdeck_test_12345678"),
		RemoteHost:     "host-1",
		RemoteTmuxName: "agentdeck_test_12345678",
	}
	inst2 := &Instance{
		ID:             GenerateRemoteInstanceID("host-2", "agentdeck_other_abcdef12"),
		RemoteHost:     "host-2",
		RemoteTmuxName: "agentdeck_other_abcdef12",
	}
	instances := []*Instance{inst1, inst2}

	// Test finding existing session
	found := FindByRemoteSession(instances, "host-1", "agentdeck_test_12345678")
	if found == nil {
		t.Error("FindByRemoteSession should find existing session")
	}
	if found != inst1 {
		t.Error("FindByRemoteSession found wrong instance")
	}

	// Test not finding non-existent session
	notFound := FindByRemoteSession(instances, "host-3", "agentdeck_missing_00000000")
	if notFound != nil {
		t.Error("FindByRemoteSession should return nil for non-existent session")
	}
}

func TestFindStaleRemoteSessions(t *testing.T) {
	// Create test instances - mix of remote and local
	inst1 := &Instance{
		ID:             "remote-aaaaaaaabbbbbbbb",
		RemoteHost:     "host-1",
		RemoteTmuxName: "agentdeck_active_12345678",
	}
	inst2 := &Instance{
		ID:             "remote-ccccccccdddddddd",
		RemoteHost:     "host-1",
		RemoteTmuxName: "agentdeck_stale_abcdef12", // Will be stale
	}
	inst3 := &Instance{
		ID:         "local-1234",
		RemoteHost: "", // Local session
	}
	inst4 := &Instance{
		ID:             "remote-eeeeeeeefffffff0",
		RemoteHost:     "host-2", // Different host - should not be affected
		RemoteTmuxName: "agentdeck_other_fedcba98",
	}

	instances := []*Instance{inst1, inst2, inst3, inst4}

	// Current remote sessions - inst2's tmux session is missing
	currentSessions := []RemoteTmuxSession{
		{Name: "agentdeck_active_12345678", WorkingDir: "/home/user/active"},
		{Name: "agentdeck_new_99999999", WorkingDir: "/home/user/new"},
	}

	staleIDs := FindStaleRemoteSessions(instances, "host-1", currentSessions)

	// Should find only inst2 as stale
	if len(staleIDs) != 1 {
		t.Errorf("FindStaleRemoteSessions() found %d stale IDs, want 1", len(staleIDs))
	}

	if len(staleIDs) > 0 && staleIDs[0] != inst2.ID {
		t.Errorf("FindStaleRemoteSessions() found wrong stale ID: %q, want %q", staleIDs[0], inst2.ID)
	}
}

func TestMergeDiscoveredSessions(t *testing.T) {
	// Existing sessions
	existing := []*Instance{
		{ID: "local-1", Title: "Local 1"},
		{ID: "remote-aabbccdd11223344", Title: "Remote 1"},
	}

	// Discovered sessions - one new, one duplicate
	discovered := []*Instance{
		{ID: "remote-aabbccdd11223344", Title: "Remote 1 (duplicate)"}, // Already exists
		{ID: "remote-55667788aabbccdd", Title: "Remote 2 (new)"},       // New
	}

	merged, newCount := MergeDiscoveredSessions(existing, discovered)

	if newCount != 1 {
		t.Errorf("MergeDiscoveredSessions() newCount = %d, want 1", newCount)
	}

	if len(merged) != 3 {
		t.Errorf("MergeDiscoveredSessions() merged length = %d, want 3", len(merged))
	}

	// Verify the new session was added
	found := false
	for _, inst := range merged {
		if inst.ID == "remote-55667788aabbccdd" {
			found = true
			break
		}
	}
	if !found {
		t.Error("MergeDiscoveredSessions() should add new session")
	}
}

func TestCleanupStaleRemoteSessions(t *testing.T) {
	// Create test instances
	inst1 := &Instance{
		ID:             "remote-active",
		RemoteHost:     "host-1",
		RemoteTmuxName: "agentdeck_active_12345678",
	}
	inst2 := &Instance{
		ID:             "remote-stale",
		RemoteHost:     "host-1",
		RemoteTmuxName: "agentdeck_stale_abcdef12",
	}
	inst3 := &Instance{
		ID:         "local-1",
		RemoteHost: "",
	}

	instances := []*Instance{inst1, inst2, inst3}

	// Only active session exists on remote
	currentSessions := []RemoteTmuxSession{
		{Name: "agentdeck_active_12345678"},
	}

	cleaned := CleanupStaleRemoteSessions(instances, "host-1", currentSessions)

	if len(cleaned) != 2 {
		t.Errorf("CleanupStaleRemoteSessions() length = %d, want 2", len(cleaned))
	}

	// Verify stale session was removed
	for _, inst := range cleaned {
		if inst.ID == "remote-stale" {
			t.Error("CleanupStaleRemoteSessions() should remove stale session")
		}
	}
}

func TestAgentDeckSessionPattern(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantMatch bool
		wantTitle string
	}{
		{"standard format", "agentdeck_my-project_12345678", true, "my-project"},
		{"single word", "agentdeck_test_abcdef12", true, "test"},
		{"multiple words", "agentdeck_my-long-project-name_fedcba98", true, "my-long-project-name"},
		{"uppercase hex", "agentdeck_test_ABCDEF12", false, ""}, // Only lowercase hex
		{"missing prefix", "other_test_12345678", false, ""},
		{"missing suffix", "agentdeck_test", false, ""},
		{"short suffix", "agentdeck_test_1234567", false, ""}, // 7 chars instead of 8
		{"long suffix", "agentdeck_test_123456789", false, ""}, // 9 chars instead of 8
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := agentDeckSessionPattern.FindStringSubmatch(tt.input)
			gotMatch := matches != nil

			if gotMatch != tt.wantMatch {
				t.Errorf("Pattern match for %q = %v, want %v", tt.input, gotMatch, tt.wantMatch)
			}

			if gotMatch && tt.wantTitle != "" {
				if len(matches) < 2 {
					t.Errorf("Pattern should capture title for %q", tt.input)
				} else if matches[1] != tt.wantTitle {
					t.Errorf("Pattern captured title = %q, want %q", matches[1], tt.wantTitle)
				}
			}
		})
	}
}
