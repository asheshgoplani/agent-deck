package tmux

import (
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestContainsBrailleChar(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty string", "", false},
		{"plain text", "hello world", false},
		{"done marker only", "✳ Working on tests", false},
		{"braille spinner frame", "⠋ Testing something", true},
		{"braille at end", "Testing ⠙", true},
		{"braille in middle", "task ⠹ running", true},
		{"range start U+2800", string(rune(0x2800)), true},
		{"range end U+28FF", string(rune(0x28FF)), true},
		{"just below range U+27FF", string(rune(0x27FF)), false},
		{"just above range U+2900", string(rune(0x2900)), false},
		{"all 10 spinner frames", "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏", true},
		{"unicode non-braille", "✨ Done", false},
		{"mixed braille and done", "⠋ ✳ mixed", true}, // braille takes priority in detection
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsBrailleChar(tt.input)
			if got != tt.want {
				t.Errorf("containsBrailleChar(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestContainsDoneMarker(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty string", "", false},
		{"plain text", "hello world", false},
		{"eight-spoked asterisk ✳", "✳ Worked for 54s", true},
		{"heavy asterisk ✻", "✻ Done", true},
		{"heavy teardrop-spoked asterisk ✽", "✽ Complete", true},
		{"six-pointed star ✶", "✶ Finished", true},
		{"four teardrop-spoked asterisk ✢", "✢ Ready", true},
		{"regular asterisk", "* not a marker", false},
		{"braille char only", "⠋ Testing", false},
		{"marker at end", "Done ✳", true},
		{"marker in middle", "task ✳ status", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsDoneMarker(tt.input)
			if got != tt.want {
				t.Errorf("containsDoneMarker(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestAnalyzePaneTitle(t *testing.T) {
	tests := []struct {
		name    string
		title   string
		command string
		want    TitleState
	}{
		// Working state: Braille spinner present
		{"braille spinner + claude", "⠂ Testing Papaorch", "claude", TitleStateWorking},
		{"braille spinner + bash", "⠋ Running tools", "bash", TitleStateWorking}, // bash during tool use
		{"braille spinner only", "⠹", "claude", TitleStateWorking},

		// Done state: done marker present (regardless of current command)
		{"done marker + claude", "✳ Worked for 54s", "claude", TitleStateDone},
		{"heavy asterisk + claude", "✻ Ready", "claude", TitleStateDone},
		{"done + node", "✳ Complete", "node", TitleStateDone},
		{"done marker + bash", "✳ Worked for 54s", "bash", TitleStateDone},
		{"done marker + zsh", "✻ Done", "zsh", TitleStateDone},
		{"done marker + fish", "✽ Complete", "fish", TitleStateDone},

		// Unknown state: no recognized pattern
		{"plain title", "my-session", "claude", TitleStateUnknown},
		{"empty title", "", "claude", TitleStateUnknown},
		{"empty both", "", "", TitleStateUnknown},
		{"regular asterisk", "* task", "bash", TitleStateUnknown},
		{"gemini title", "Gemini CLI", "gemini", TitleStateUnknown},

		// Edge cases
		{"braille + done marker", "⠋ ✳ mixed signals", "claude", TitleStateWorking}, // braille wins
		{"done + version string", "✳ claude v2.1.25", "claude", TitleStateDone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AnalyzePaneTitle(tt.title, tt.command)
			if got != tt.want {
				t.Errorf("AnalyzePaneTitle(%q, %q) = %v, want %v", tt.title, tt.command, got, tt.want)
			}
		})
	}
}

func TestRefreshPaneInfoCache(t *testing.T) {
	skipIfNoTmuxServer(t)

	// Create a test session to ensure there's at least one pane
	sessName := SessionPrefix + "title_detection_test"
	cmd := exec.Command("tmux", "new-session", "-d", "-s", sessName)
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to create test session: %v", err)
	}
	defer func() {
		_ = exec.Command("tmux", "kill-session", "-t", sessName).Run()
	}()

	// Refresh the cache
	RefreshPaneInfoCache()

	// Verify cache was populated
	info, ok := GetCachedPaneInfo(sessName)
	if !ok {
		t.Fatal("Expected to find test session in pane cache")
	}

	// The title might be empty or the session name; command should be a shell
	if info.CurrentCommand == "" {
		t.Error("Expected non-empty CurrentCommand for test session")
	}
	if info.CurrentPath == "" {
		t.Error("Expected non-empty CurrentPath for test session")
	}

	// Verify a non-existent session is not in cache
	_, ok = GetCachedPaneInfo("nonexistent_session_xyz")
	if ok {
		t.Error("Expected nonexistent session to not be in cache")
	}
}

func TestGetCachedPaneInfo_StaleCache(t *testing.T) {
	// Set cache data with a time far in the past (stale)
	paneCacheMu.Lock()
	paneCacheData = map[string]PaneInfo{
		"test_session": {Title: "✳ Done", CurrentCommand: "claude"},
	}
	paneCacheTime = time.Now().Add(-10 * time.Second) // 10 seconds ago (stale, threshold is 4s)
	paneCacheMu.Unlock()

	_, ok := GetCachedPaneInfo("test_session")
	if ok {
		t.Error("Expected stale cache to return false")
	}
}

func TestGetCachedWindows(t *testing.T) {
	// Seed window cache
	windowCacheMu.Lock()
	windowCacheData = map[string][]WindowInfo{
		"agentdeck_test_abc12345": {
			{Index: 0, Name: "main", Activity: 100},
			{Index: 1, Name: "tests", Activity: 200},
		},
	}
	windowCacheTime = time.Now()
	windowCacheMu.Unlock()

	// Seed tool cache — window 1 has claude
	windowToolCacheMu.Lock()
	windowToolCacheData = map[string]map[int]string{
		"agentdeck_test_abc12345": {1: "claude"},
	}
	windowToolCacheMu.Unlock()

	// Hit with tool merge
	wins := GetCachedWindows("agentdeck_test_abc12345")
	assert.Len(t, wins, 2)
	assert.Equal(t, "main", wins[0].Name)
	assert.Equal(t, "", wins[0].Tool)
	assert.Equal(t, 1, wins[1].Index)
	assert.Equal(t, "claude", wins[1].Tool)

	// Miss
	wins = GetCachedWindows("nonexistent")
	assert.Nil(t, wins)

	// Expired cache
	windowCacheMu.Lock()
	windowCacheTime = time.Now().Add(-5 * time.Second)
	windowCacheMu.Unlock()
	wins = GetCachedWindows("agentdeck_test_abc12345")
	assert.Nil(t, wins)

	// Cleanup
	windowToolCacheMu.Lock()
	windowToolCacheData = nil
	windowToolCacheMu.Unlock()
}

func TestGetCachedWindows_ReturnsCopy(t *testing.T) {
	windowCacheMu.Lock()
	windowCacheData = map[string][]WindowInfo{
		"sess": {{Index: 0, Name: "orig", Activity: 100}},
	}
	windowCacheTime = time.Now()
	windowCacheMu.Unlock()

	windowToolCacheMu.Lock()
	windowToolCacheData = nil
	windowToolCacheMu.Unlock()

	wins := GetCachedWindows("sess")
	assert.Len(t, wins, 1)

	// Mutate the returned slice
	wins[0].Name = "mutated"

	// Original cache must be unchanged
	windowCacheMu.RLock()
	assert.Equal(t, "orig", windowCacheData["sess"][0].Name)
	windowCacheMu.RUnlock()
}

func TestGetCachedWindows_ToolMergePartial(t *testing.T) {
	windowCacheMu.Lock()
	windowCacheData = map[string][]WindowInfo{
		"s1": {
			{Index: 0, Name: "a"},
			{Index: 1, Name: "b"},
			{Index: 2, Name: "c"},
		},
	}
	windowCacheTime = time.Now()
	windowCacheMu.Unlock()

	// Only window 2 has a tool
	windowToolCacheMu.Lock()
	windowToolCacheData = map[string]map[int]string{
		"s1": {2: "gemini"},
	}
	windowToolCacheMu.Unlock()

	wins := GetCachedWindows("s1")
	assert.Len(t, wins, 3)
	assert.Equal(t, "", wins[0].Tool)
	assert.Equal(t, "", wins[1].Tool)
	assert.Equal(t, "gemini", wins[2].Tool)

	// Cleanup
	windowToolCacheMu.Lock()
	windowToolCacheData = nil
	windowToolCacheMu.Unlock()
}

func TestUpdateWindowToolCache(t *testing.T) {
	// First write
	data1 := map[string]map[int]string{
		"s1": {0: "claude", 1: "gemini"},
	}
	updateWindowToolCache(data1)

	windowToolCacheMu.RLock()
	assert.Equal(t, "claude", windowToolCacheData["s1"][0])
	assert.Equal(t, "gemini", windowToolCacheData["s1"][1])
	windowToolCacheMu.RUnlock()

	// Full replacement — s1 data is gone
	data2 := map[string]map[int]string{
		"s2": {0: "aider"},
	}
	updateWindowToolCache(data2)

	windowToolCacheMu.RLock()
	assert.Nil(t, windowToolCacheData["s1"])
	assert.Equal(t, "aider", windowToolCacheData["s2"][0])
	windowToolCacheMu.RUnlock()

	// Cleanup
	windowToolCacheMu.Lock()
	windowToolCacheData = nil
	windowToolCacheMu.Unlock()
}
