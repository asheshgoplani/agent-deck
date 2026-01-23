package session

import (
	"testing"
)

func TestTransformRemoteGroupPath(t *testing.T) {
	tests := []struct {
		name            string
		remoteGroupPath string
		groupPrefix     string
		groupName       string
		expected        string
	}{
		{
			name:            "empty remote path maps to host root",
			remoteGroupPath: "",
			groupPrefix:     "remote",
			groupName:       "jeeves",
			expected:        "remote/jeeves",
		},
		{
			name:            "default group path maps to host root",
			remoteGroupPath: "my-sessions",
			groupPrefix:     "remote",
			groupName:       "jeeves",
			expected:        "remote/jeeves",
		},
		{
			name:            "simple remote group",
			remoteGroupPath: "production",
			groupPrefix:     "remote",
			groupName:       "jeeves",
			expected:        "remote/jeeves/production",
		},
		{
			name:            "nested remote group",
			remoteGroupPath: "jeeves/workers",
			groupPrefix:     "remote",
			groupName:       "jeeves",
			expected:        "remote/jeeves/jeeves/workers",
		},
		{
			name:            "custom prefix",
			remoteGroupPath: "dev",
			groupPrefix:     "ssh-hosts",
			groupName:       "server1",
			expected:        "ssh-hosts/server1/dev",
		},
		{
			name:            "deeply nested remote group",
			remoteGroupPath: "projects/frontend/components",
			groupPrefix:     "remote",
			groupName:       "dev-server",
			expected:        "remote/dev-server/projects/frontend/components",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TransformRemoteGroupPath(tt.remoteGroupPath, tt.groupPrefix, tt.groupName)
			if result != tt.expected {
				t.Errorf("TransformRemoteGroupPath(%q, %q, %q) = %q, want %q",
					tt.remoteGroupPath, tt.groupPrefix, tt.groupName, result, tt.expected)
			}
		})
	}
}

func TestTransformRemoteGroups(t *testing.T) {
	tests := []struct {
		name         string
		remoteGroups []*GroupData
		groupPrefix  string
		groupName    string
		expected     []*GroupData
	}{
		{
			name:         "nil input returns nil",
			remoteGroups: nil,
			groupPrefix:  "remote",
			groupName:    "host1",
			expected:     nil,
		},
		{
			name:         "empty input returns nil",
			remoteGroups: []*GroupData{},
			groupPrefix:  "remote",
			groupName:    "host1",
			expected:     nil,
		},
		{
			name: "default group is skipped",
			remoteGroups: []*GroupData{
				{Name: "My Sessions", Path: "my-sessions", Expanded: true, Order: 0},
			},
			groupPrefix: "remote",
			groupName:   "host1",
			expected:    []*GroupData{},
		},
		{
			name: "transforms single group",
			remoteGroups: []*GroupData{
				{Name: "Production", Path: "production", Expanded: true, Order: 1},
			},
			groupPrefix: "remote",
			groupName:   "jeeves",
			expected: []*GroupData{
				{Name: "Production", Path: "remote/jeeves/production", Expanded: true, Order: 1},
			},
		},
		{
			name: "transforms multiple groups",
			remoteGroups: []*GroupData{
				{Name: "My Sessions", Path: "my-sessions", Expanded: true, Order: 0},
				{Name: "Production", Path: "production", Expanded: true, Order: 1},
				{Name: "Workers", Path: "jeeves/workers", Expanded: false, Order: 2},
			},
			groupPrefix: "remote",
			groupName:   "jeeves",
			expected: []*GroupData{
				{Name: "Production", Path: "remote/jeeves/production", Expanded: true, Order: 1},
				{Name: "Workers", Path: "remote/jeeves/jeeves/workers", Expanded: false, Order: 2},
			},
		},
		{
			name: "preserves expanded state",
			remoteGroups: []*GroupData{
				{Name: "Collapsed", Path: "collapsed", Expanded: false, Order: 0},
				{Name: "Expanded", Path: "expanded", Expanded: true, Order: 1},
			},
			groupPrefix: "remote",
			groupName:   "server",
			expected: []*GroupData{
				{Name: "Collapsed", Path: "remote/server/collapsed", Expanded: false, Order: 0},
				{Name: "Expanded", Path: "remote/server/expanded", Expanded: true, Order: 1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TransformRemoteGroups(tt.remoteGroups, tt.groupPrefix, tt.groupName)

			if tt.expected == nil {
				if result != nil {
					t.Errorf("expected nil, got %v", result)
				}
				return
			}

			if len(result) != len(tt.expected) {
				t.Errorf("got %d groups, want %d", len(result), len(tt.expected))
				return
			}

			for i, g := range result {
				exp := tt.expected[i]
				if g.Name != exp.Name || g.Path != exp.Path || g.Expanded != exp.Expanded || g.Order != exp.Order {
					t.Errorf("group %d: got {%s, %s, %v, %d}, want {%s, %s, %v, %d}",
						i, g.Name, g.Path, g.Expanded, g.Order,
						exp.Name, exp.Path, exp.Expanded, exp.Order)
				}
			}
		})
	}
}

func TestGenerateRemoteInstanceID(t *testing.T) {
	// Test determinism - same inputs should produce same output
	id1 := GenerateRemoteInstanceID("host1", "agentdeck_test_12345678")
	id2 := GenerateRemoteInstanceID("host1", "agentdeck_test_12345678")
	if id1 != id2 {
		t.Errorf("GenerateRemoteInstanceID is not deterministic: %s != %s", id1, id2)
	}

	// Test prefix
	if id1[:7] != "remote-" {
		t.Errorf("ID should start with 'remote-', got %s", id1)
	}

	// Test different inputs produce different outputs
	id3 := GenerateRemoteInstanceID("host2", "agentdeck_test_12345678")
	if id1 == id3 {
		t.Error("Different hosts should produce different IDs")
	}

	id4 := GenerateRemoteInstanceID("host1", "agentdeck_other_87654321")
	if id1 == id4 {
		t.Error("Different session names should produce different IDs")
	}
}

func TestParseTitleFromTmuxName(t *testing.T) {
	tests := []struct {
		name     string
		tmuxName string
		expected string
	}{
		{
			name:     "standard agentdeck name",
			tmuxName: "agentdeck_my-project_a1b2c3d4",
			expected: "My Project",
		},
		{
			name:     "multi-word title",
			tmuxName: "agentdeck_revvie-sdlc-agent_12345678",
			expected: "Revvie Sdlc Agent",
		},
		{
			name:     "single word title",
			tmuxName: "agentdeck_test_abcdef01",
			expected: "Test",
		},
		{
			name:     "non-matching format fallback",
			tmuxName: "agentdeck_something",
			expected: "Something",
		},
		{
			name:     "non-agentdeck prefix with hyphens",
			tmuxName: "other-session-name",
			expected: "Other Session Name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseTitleFromTmuxName(tt.tmuxName)
			if result != tt.expected {
				t.Errorf("ParseTitleFromTmuxName(%q) = %q, want %q", tt.tmuxName, result, tt.expected)
			}
		})
	}
}
