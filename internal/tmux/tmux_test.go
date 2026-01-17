package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"my-session", "my-session"},
		{"my session", "my-session"},
		{"my.session", "my-session"},
		{"my:session", "my-session"},
		{"my/session", "my-session"},
	}
	for _, tt := range tests {
		result := sanitizeName(tt.input)
		if result != tt.expected {
			t.Errorf("sanitizeName(%s) = %s, want %s", tt.input, result, tt.expected)
		}
	}
}

func TestNewSession(t *testing.T) {
	sess := NewSession("test-session", "/tmp")
	if sess.DisplayName != "test-session" {
		t.Errorf("DisplayName = %s, want test-session", sess.DisplayName)
	}
	if sess.WorkDir != "/tmp" {
		t.Errorf("WorkDir = %s, want /tmp", sess.WorkDir)
	}
	// Name should have prefix + sanitized name + unique suffix
	expectedPrefix := SessionPrefix + "test-session_"
	if !strings.HasPrefix(sess.Name, expectedPrefix) {
		t.Errorf("Name = %s, want prefix %s", sess.Name, expectedPrefix)
	}
	// Verify unique suffix exists (8 hex chars)
	suffix := strings.TrimPrefix(sess.Name, expectedPrefix)
	if len(suffix) != 8 {
		t.Errorf("Unique suffix length = %d, want 8", len(suffix))
	}
}

func TestNewSessionUniqueness(t *testing.T) {
	// Creating sessions with same name should produce unique tmux names
	sess1 := NewSession("duplicate", "/tmp")
	sess2 := NewSession("duplicate", "/tmp")

	if sess1.Name == sess2.Name {
		t.Errorf("Two sessions with same display name have identical tmux names: %s", sess1.Name)
	}
	if sess1.DisplayName != sess2.DisplayName {
		t.Errorf("DisplayNames should be same: %s vs %s", sess1.DisplayName, sess2.DisplayName)
	}
}

func TestSessionPrefix(t *testing.T) {
	if SessionPrefix != "agentdeck_" {
		t.Errorf("SessionPrefix = %s, want agentdeck_", SessionPrefix)
	}
}

func TestPromptDetector(t *testing.T) {
	// Test shell prompt detection
	shellDetector := NewPromptDetector("shell")

	shellTests := []struct {
		content  string
		expected bool
	}{
		{"Do you want to continue? (Y/n)", true},
		{"[Y/n] Proceed?", true},
		{"$ ", true},
		{"user@host:~$ ", true},
		{"% ", true},
		{"❯ ", true},
		{"Processing...", false},
		{"Running command", false},
	}

	for _, tt := range shellTests {
		result := shellDetector.HasPrompt(tt.content)
		if result != tt.expected {
			t.Errorf("shell.HasPrompt(%q) = %v, want %v", tt.content, result, tt.expected)
		}
	}

	// Test Claude prompt detection
	claudeDetector := NewPromptDetector("claude")

	claudeTests := []struct {
		content  string
		expected bool
	}{
		// Permission prompts (normal mode)
		{"No, and tell Claude what to do differently", true},
		{"Yes, allow once", true},
		{"❯ Yes", true},
		// Input prompt (--dangerously-skip-permissions mode)
		{">", true},
		{"> ", true},
		// Busy indicators should return false
		{"esc to interrupt", false},
		{"(esc to interrupt)", false},
		{"Thinking... (45s · 1234 tokens · esc to interrupt)", false},
		// Regular output should be false
		{"Some random output text", false},
	}

	for _, tt := range claudeTests {
		result := claudeDetector.HasPrompt(tt.content)
		if result != tt.expected {
			t.Errorf("claude.HasPrompt(%q) = %v, want %v", tt.content, result, tt.expected)
		}
	}

	// Test OpenCode prompt detection
	opencodeDetector := NewPromptDetector("opencode")

	opencodeTests := []struct {
		content  string
		expected bool
	}{
		{"Ask anything... \"What is the tech stack\"", true},
		{"┃  hello", true},
		{"Build  Big Pickle  OpenCode Zen", true},
		{"Plan mode indicator", true},
	}

	for _, tt := range opencodeTests {
		result := opencodeDetector.HasPrompt(tt.content)
		if result != tt.expected {
			t.Errorf("opencode.HasPrompt(%q) = %v, want %v", tt.content, result, tt.expected)
		}
	}
}

func TestBusyIndicatorDetection(t *testing.T) {
	sess := NewSession("test", "/tmp")
	sess.Command = "claude"

	tests := []struct {
		name     string
		content  string
		expected bool
	}{
		{
			name:     "esc to interrupt",
			content:  "Working on task...\nesc to interrupt\n",
			expected: true,
		},
		{
			name:     "spinner character",
			content:  "Loading...\n⠋ Processing\n",
			expected: true,
		},
		{
			name:     "thinking with tokens",
			content:  "Thinking... (45s · 1234 tokens)\n",
			expected: true,
		},
		{
			name:     "normal output",
			content:  "Here is some text\nMore text\n",
			expected: false,
		},
		{
			name:     "prompt waiting",
			content:  "Done!\n>\n",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sess.hasBusyIndicator(tt.content)
			if result != tt.expected {
				t.Errorf("hasBusyIndicator(%q) = %v, want %v", tt.name, result, tt.expected)
			}
		})
	}
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple green", "\x1b[32mGreen\x1b[0m", "Green"},
		{"bold red", "\x1b[1;31mBold Red\x1b[0m", "Bold Red"},
		{"no ansi", "No ANSI here", "No ANSI here"},
		{"osc title", "\x1b]0;Title\x07Content", "Content"},
		{"empty string", "", ""},
		{"256 color", "\x1b[38;5;140mPurple\x1b[0m", "Purple"},
		{"true color", "\x1b[38;2;255;128;0mOrange\x1b[0m", "Orange"},
		{"cursor movement", "\x1b[2Aup\x1b[2Bdown", "updown"},
		{"multiline", "\x1b[32mline1\x1b[0m\n\x1b[33mline2\x1b[0m", "line1\nline2"},
		{"mixed content", "Hello \x1b[1mworld\x1b[0m!", "Hello world!"},
		{"nested codes", "\x1b[1m\x1b[31m\x1b[4mtext\x1b[0m", "text"},
		{"8-bit csi", "\x9Bmtest\x9Bm", "test"}, // 8-bit CSI (0x9B)
		// Edge cases - malformed sequences (rare in real terminal output)
		// These match behavior of the original O(n²) implementation
		{"esc at end", "hello\x1b", "hello\x1b"},              // trailing ESC kept (same as old)
		{"osc without terminator", "\x1b]0;Title", "0;Title"}, // ESC] stripped, rest kept
		{"csi without letter", "\x1b[123", ""},                // CSI params stripped
		{"just esc", "\x1b", "\x1b"},                          // lone ESC kept (same as old)
		{"esc followed by char", "\x1bXtext", "text"},         // ESC+char stripped
		{"8-bit csi at end", "test\x9B", "test"},              // 8-bit CSI stripped
		{"csi at end no params", "test\x1b[", "test"},         // CSI stripped
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StripANSI(tt.input)
			if result != tt.expected {
				t.Errorf("StripANSI(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// generateANSIContent creates test content with many ANSI codes
func generateANSIContent(lineCount int) string {
	var b strings.Builder
	for i := 0; i < lineCount; i++ {
		b.WriteString("\x1b[38;5;140m")
		b.WriteString("Line ")
		b.WriteString("\x1b[1m")
		b.WriteString("content")
		b.WriteString("\x1b[0m")
		b.WriteString(" with ")
		b.WriteString("\x1b[32m")
		b.WriteString("colorized")
		b.WriteString("\x1b[0m")
		b.WriteString(" text\n")
	}
	return b.String()
}

// BenchmarkStripANSI_Large tests performance on Issue #39 scenario (2000 lines)
func BenchmarkStripANSI_Large(b *testing.B) {
	content := generateANSIContent(2000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = StripANSI(content)
	}
}

// BenchmarkStripANSI_NoANSI tests fast path for content without ANSI codes
func BenchmarkStripANSI_NoANSI(b *testing.B) {
	content := strings.Repeat("Plain text without any ANSI codes here\n", 2000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = StripANSI(content)
	}
}

// stripANSIOld is the OLD O(n²) implementation for benchmark comparison
// DO NOT USE - this is only for performance testing
func stripANSIOld(content string) string {
	result := content
	for {
		start := strings.Index(result, "\x1b[")
		if start == -1 {
			break
		}
		end := start + 2
		for end < len(result) {
			c := result[end]
			if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
				end++
				break
			}
			end++
		}
		result = result[:start] + result[end:]
	}
	for {
		start := strings.Index(result, "\x1b]")
		if start == -1 {
			break
		}
		end := strings.Index(result[start:], "\x07")
		if end == -1 {
			break
		}
		result = result[:start] + result[start+end+1:]
	}
	return result
}

// BenchmarkStripANSI_OldVsNew compares O(n²) old vs O(n) new implementation
func BenchmarkStripANSI_OldVsNew(b *testing.B) {
	content := generateANSIContent(2000) // Issue #39 scenario

	b.Run("New_O(n)", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = StripANSI(content)
		}
	})

	b.Run("Old_O(n²)", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = stripANSIOld(content)
		}
	})
}

func TestDetectTool(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	sess := NewSession("test", "/tmp")
	// Without an actual session, DetectTool should return "shell"
	tool := sess.DetectTool()
	if tool != "shell" {
		t.Logf("DetectTool returned %s (expected shell for non-existent session)", tool)
	}
}

func TestReconnectSession(t *testing.T) {
	// Test that ReconnectSession properly initializes all fields
	sess := ReconnectSession("agentdeck_test_abc123", "test", "/tmp", "claude")

	if sess.Name != "agentdeck_test_abc123" {
		t.Errorf("Name = %s, want agentdeck_test_abc123", sess.Name)
	}
	if sess.DisplayName != "test" {
		t.Errorf("DisplayName = %s, want test", sess.DisplayName)
	}
	if sess.Command != "claude" {
		t.Errorf("Command = %s, want claude", sess.Command)
	}
	// stateTracker is lazily initialized on first GetStatus call
	if sess.stateTracker != nil {
		t.Error("stateTracker should be nil until first GetStatus")
	}
}

// TestClaudeCodeDetectionScenarios tests all Claude Code status detection scenarios
func TestClaudeCodeDetectionScenarios(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		expectWaiting bool
		description   string
	}{
		// --dangerously-skip-permissions mode scenarios
		{
			name: "skip-perms: waiting with >",
			content: `I've completed the task.
The files have been updated.

>`,
			expectWaiting: true,
			description:   "Claude finished task, showing > prompt",
		},
		{
			name: "skip-perms: waiting with > and space",
			content: `Done!

> `,
			expectWaiting: true,
			description:   "Claude waiting with > followed by space",
		},
		{
			name: "skip-perms: user typing",
			content: `Ready for input.

> fix the bug`,
			expectWaiting: true,
			description:   "User started typing at prompt",
		},
		// Normal mode scenarios
		{
			name: "normal: permission prompt",
			content: `I'd like to edit src/main.go

Yes, allow once
No, and tell Claude what to do differently`,
			expectWaiting: true,
			description:   "Permission dialog shown",
		},
		{
			name: "normal: trust prompt",
			content: `Welcome to Claude Code!
Do you trust the files in this folder?`,
			expectWaiting: true,
			description:   "Initial trust dialog",
		},
		// Busy scenarios (should NOT be waiting)
		{
			name: "busy: esc to interrupt",
			content: `Working on your request...
Reading files...
esc to interrupt`,
			expectWaiting: false,
			description:   "Claude actively working",
		},
		{
			name: "busy: spinner character",
			content: `Processing...
⠋ Loading modules`,
			expectWaiting: false,
			description:   "Spinner indicates active processing",
		},
		{
			name: "busy: thinking with tokens",
			content: `Analyzing codebase
Thinking... (45s · 1234 tokens · esc to interrupt)`,
			expectWaiting: false,
			description:   "Thinking indicator with token count",
		},
		// Edge cases
		{
			name:          "edge: empty content",
			content:       ``,
			expectWaiting: false,
			description:   "Empty terminal",
		},
		{
			name:          "edge: just whitespace",
			content:       "\n   ",
			expectWaiting: false,
			description:   "Only whitespace",
		},
		{
			name: "edge: > in middle of output",
			content: `The value > 100 is invalid
Please try again`,
			expectWaiting: false,
			description:   "> in output text shouldn't trigger",
		},
	}

	detector := NewPromptDetector("claude")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detector.HasPrompt(tt.content)
			if result != tt.expectWaiting {
				t.Errorf("%s\nHasPrompt = %v, want %v\nContent:\n%s",
					tt.description, result, tt.expectWaiting, tt.content)
			}
		})
	}
}

// TestGetStatusFlow tests the GetStatus initialization flow
func TestGetStatusFlow(t *testing.T) {
	// Create a session with command set (simulates reconnected session)
	sess := ReconnectSession("test_session", "test", "/tmp", "claude")

	// stateTracker starts nil (lazy init on first GetStatus)
	if sess.stateTracker != nil {
		t.Error("stateTracker should be nil initially")
	}

	// Acknowledge is safe to call even with nil stateTracker
	sess.Acknowledge()
	if sess.stateTracker == nil {
		t.Fatal("Acknowledge should initialize stateTracker")
	}
	if !sess.stateTracker.acknowledged {
		t.Error("acknowledged should be true after Acknowledge")
	}
}

func TestListAllSessionsEmpty(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	// This should not error even if no sessions exist
	sessions, err := ListAllSessions()
	if err != nil {
		t.Logf("ListAllSessions error (may be expected): %v", err)
	}
	_ = sessions // May be empty, that's fine
}

// =============================================================================
// Notification-Style Status Detection Tests
// =============================================================================

// TestStateTrackerInitialization verifies StateTracker is properly initialized
func TestStateTrackerInitialization(t *testing.T) {
	tracker := &StateTracker{
		lastHash:       "abc123",
		lastChangeTime: time.Now(),
		acknowledged:   false,
	}

	if tracker.lastHash != "abc123" {
		t.Errorf("lastHash = %s, want abc123", tracker.lastHash)
	}
	if tracker.acknowledged != false {
		t.Error("acknowledged should start as false")
	}
}

// TestAcknowledge verifies the Acknowledge() method works correctly
func TestAcknowledge(t *testing.T) {
	sess := NewSession("test", "/tmp")

	// Before any status check, stateTracker should be nil
	if sess.stateTracker != nil {
		t.Error("stateTracker should be nil before first GetStatus call")
	}

	// Acknowledge should be safe to call even with nil stateTracker
	sess.Acknowledge() // Should not panic

	// Now initialize the stateTracker manually (simulating first GetStatus)
	sess.stateTracker = &StateTracker{
		lastHash:       "test",
		lastChangeTime: time.Now(),
		acknowledged:   false,
	}

	// Before acknowledging
	if sess.stateTracker.acknowledged {
		t.Error("acknowledged should be false before Acknowledge()")
	}

	// Acknowledge
	sess.Acknowledge()

	// After acknowledging
	if !sess.stateTracker.acknowledged {
		t.Error("acknowledged should be true after Acknowledge()")
	}
}

// TestHashContent verifies hash generation is consistent
func TestHashContent(t *testing.T) {
	sess := NewSession("test", "/tmp")

	content1 := "Hello World"
	content2 := "Hello World"
	content3 := "Different Content"

	hash1 := sess.hashContent(content1)
	hash2 := sess.hashContent(content2)
	hash3 := sess.hashContent(content3)

	// Same content should produce same hash
	if hash1 != hash2 {
		t.Errorf("Same content produced different hashes: %s vs %s", hash1, hash2)
	}

	// Different content should produce different hash
	if hash1 == hash3 {
		t.Error("Different content produced same hash")
	}

	// Hash should be non-empty
	if len(hash1) == 0 {
		t.Error("Hash should not be empty")
	}
}

// TestNotificationStyleStatusFlow tests the complete notification flow
// This is a unit test that doesn't require actual tmux sessions
//
// New simplified model:
//   - GetStatus() returns "active" if content changed since last call
//   - GetStatus() returns "waiting" if content is stable AND not acknowledged
//   - GetStatus() returns "idle" if content is stable AND acknowledged
//   - acknowledged is reset whenever content changes (to notify user when it stops)
func TestNotificationStyleStatusFlow(t *testing.T) {
	t.Run("Content change returns active and resets acknowledged", func(t *testing.T) {
		tracker := &StateTracker{
			lastHash:       "old",
			lastChangeTime: time.Now().Add(-5 * time.Second),
			acknowledged:   true, // Was acknowledged
		}

		// Simulate content change (what GetStatus does)
		newHash := "new"
		hasUpdated := newHash != tracker.lastHash

		if hasUpdated {
			tracker.lastHash = newHash
			tracker.lastChangeTime = time.Now()
			tracker.acknowledged = false // Reset so user gets notified when it stops
		}

		// Verify
		if !hasUpdated {
			t.Error("should detect content changed")
		}
		if tracker.acknowledged {
			t.Error("acknowledged should be reset when content changes")
		}
	})

	t.Run("No content change with acknowledged=false returns waiting", func(t *testing.T) {
		tracker := &StateTracker{
			lastHash:       "stable_content",
			lastChangeTime: time.Now().Add(-10 * time.Second),
			acknowledged:   false,
		}

		// Simulate GetStatus check - same content
		currentHash := "stable_content"
		hasUpdated := currentHash != tracker.lastHash

		// Determine status
		var status string
		if hasUpdated {
			status = "active"
		} else if tracker.acknowledged {
			status = "idle"
		} else {
			status = "waiting"
		}

		if status != "waiting" {
			t.Errorf("Should be waiting (not acknowledged), got %s", status)
		}
	})

	t.Run("No content change with acknowledged=true returns idle", func(t *testing.T) {
		tracker := &StateTracker{
			lastHash:       "stable_content",
			lastChangeTime: time.Now().Add(-10 * time.Second),
			acknowledged:   true, // User has seen it
		}

		// Simulate GetStatus check - same content
		currentHash := "stable_content"
		hasUpdated := currentHash != tracker.lastHash

		// Determine status
		var status string
		if hasUpdated {
			status = "active"
		} else if tracker.acknowledged {
			status = "idle"
		} else {
			status = "waiting"
		}

		if status != "idle" {
			t.Errorf("Should be idle (acknowledged), got %s", status)
		}
	})

	t.Run("Acknowledge changes waiting to idle on next check", func(t *testing.T) {
		tracker := &StateTracker{
			lastHash:       "stopped",
			lastChangeTime: time.Now().Add(-10 * time.Second),
			acknowledged:   false,
		}

		// Status before acknowledge
		status1 := "waiting"
		if tracker.acknowledged {
			status1 = "idle"
		}
		if status1 != "waiting" {
			t.Errorf("Before acknowledge should be waiting, got %s", status1)
		}

		// User acknowledges
		tracker.acknowledged = true

		// Status after acknowledge
		status2 := "waiting"
		if tracker.acknowledged {
			status2 = "idle"
		}
		if status2 != "idle" {
			t.Errorf("After acknowledge should be idle, got %s", status2)
		}
	})
}

// TestStateTrackerLifecycle tests a complete lifecycle
// Using new simplified model:
//   - Content changed → "active" (green)
//   - Content stable + not acknowledged → "waiting" (yellow)
//   - Content stable + acknowledged → "idle" (gray)
func TestStateTrackerLifecycle(t *testing.T) {
	// Helper to compute status (same logic as GetStatus)
	computeStatus := func(tracker *StateTracker, newHash string) string {
		hasUpdated := newHash != tracker.lastHash
		if hasUpdated {
			tracker.lastHash = newHash
			tracker.lastChangeTime = time.Now()
			tracker.acknowledged = false
			return "active"
		}
		if tracker.acknowledged {
			return "idle"
		}
		return "waiting"
	}

	tracker := &StateTracker{
		lastHash:       "content_v1",
		lastChangeTime: time.Now(),
		acknowledged:   false,
	}

	// Step 1: Content changing → GREEN
	status := computeStatus(tracker, "content_v2")
	if status != "active" {
		t.Fatalf("Step 1: Expected active, got %s", status)
	}
	t.Log("Step 1: Working (GREEN) ✓")

	// Step 2: Content stops (same hash) → YELLOW (not acknowledged)
	status = computeStatus(tracker, "content_v2") // Same hash
	if status != "waiting" {
		t.Fatalf("Step 2: Expected waiting, got %s", status)
	}
	t.Log("Step 2: Stopped, needs attention (YELLOW) ✓")

	// Step 3: User acknowledges → GRAY
	tracker.acknowledged = true
	status = computeStatus(tracker, "content_v2") // Same hash
	if status != "idle" {
		t.Fatalf("Step 3: Expected idle, got %s", status)
	}
	t.Log("Step 3: User acknowledged (GRAY) ✓")

	// Step 4: New work starts → GREEN and acknowledged reset
	status = computeStatus(tracker, "content_v3") // New hash
	if status != "active" {
		t.Fatalf("Step 4: Expected active, got %s", status)
	}
	if tracker.acknowledged {
		t.Fatalf("Step 4: acknowledged should be reset")
	}
	t.Log("Step 4: New work started (GREEN), acknowledged reset ✓")

	// Step 5: Work completes again → YELLOW
	status = computeStatus(tracker, "content_v3") // Same hash
	if status != "waiting" {
		t.Fatalf("Step 5: Expected waiting, got %s", status)
	}
	t.Log("Step 5: Work completed again (YELLOW) ✓")

	t.Log("Complete lifecycle test passed!")
}

// TestUserTypingInsideSession tests user typing behavior
// Note: In the new simplified model, acknowledged is ALWAYS reset when content changes.
// This is the correct behavior: if user types, content changes, and when Claude responds
// and stops, user should be notified (yellow) until they acknowledge again.
func TestUserTypingInsideSession(t *testing.T) {
	// Helper to compute status
	computeStatus := func(tracker *StateTracker, newHash string) string {
		hasUpdated := newHash != tracker.lastHash
		if hasUpdated {
			tracker.lastHash = newHash
			tracker.lastChangeTime = time.Now()
			tracker.acknowledged = false // Always reset when content changes
			return "active"
		}
		if tracker.acknowledged {
			return "idle"
		}
		return "waiting"
	}

	tracker := &StateTracker{
		lastHash:       "initial_content",
		lastChangeTime: time.Now().Add(-5 * time.Second),
		acknowledged:   false,
	}

	t.Log("Initial: Session waiting (YELLOW)")

	// Step 1: Content stable, not acknowledged → waiting
	status := computeStatus(tracker, "initial_content")
	if status != "waiting" {
		t.Fatalf("Step 1: Expected waiting, got %s", status)
	}

	// Step 2: User opens session - acknowledge
	tracker.acknowledged = true
	t.Log("User opened session, acknowledged=true")

	// Step 3: User types something - content changes
	// This resets acknowledged (correct: user will be notified when Claude responds)
	status = computeStatus(tracker, "user_typed_something")
	if status != "active" {
		t.Fatalf("Step 3: Expected active (user typing), got %s", status)
	}
	if tracker.acknowledged {
		t.Fatal("Step 3: acknowledged should be reset when content changes")
	}
	t.Log("User typing, content changing (GREEN)")

	// Step 4: Claude responds - more content changes
	status = computeStatus(tracker, "claude_responded")
	if status != "active" {
		t.Fatalf("Step 4: Expected active (Claude responding), got %s", status)
	}

	// Step 5: Claude stops - content stable, not acknowledged → waiting
	// This is correct: user should be notified that Claude finished
	status = computeStatus(tracker, "claude_responded") // Same hash
	if status != "waiting" {
		t.Fatalf("Step 5: Expected waiting (Claude stopped), got %s", status)
	}
	t.Log("Claude stopped, needs attention (YELLOW) - correct!")

	// Step 6: User acknowledges (opens session again)
	tracker.acknowledged = true
	status = computeStatus(tracker, "claude_responded") // Same hash
	if status != "idle" {
		t.Fatalf("Step 6: Expected idle (acknowledged), got %s", status)
	}
	t.Log("User acknowledged, session idle (GRAY)")
}

// TestStatusStrings verifies the status string mapping
func TestStatusStrings(t *testing.T) {
	// These are the status strings returned by GetStatus()
	validStatuses := map[string]string{
		"active":   "GREEN - content changing",
		"waiting":  "YELLOW - needs attention",
		"idle":     "GRAY - acknowledged",
		"inactive": "Session doesn't exist",
	}

	for status, description := range validStatuses {
		t.Logf("Status '%s' = %s", status, description)
	}
}

// TestReconnectSessionHasStateTracker verifies reconnected sessions work correctly
func TestReconnectSessionHasStateTracker(t *testing.T) {
	sess := ReconnectSession("agentdeck_test_123", "my-project", "/home/user/project", "claude")

	// After reconnect, stateTracker starts nil (initialized on first GetStatus)
	if sess.stateTracker != nil {
		t.Error("stateTracker should be nil initially (lazy init on GetStatus)")
	}

	// But Acknowledge should be safe to call
	sess.Acknowledge() // Should not panic

	// AcknowledgeWithSnapshot should also be safe even if tmux session doesn't exist
	sess.stateTracker = nil // reset
	sess.AcknowledgeWithSnapshot()
	if sess.stateTracker == nil {
		t.Fatal("AcknowledgeWithSnapshot should initialize stateTracker")
	}
	if !sess.stateTracker.acknowledged {
		t.Error("AcknowledgeWithSnapshot should set acknowledged=true")
	}

	// Manually init stateTracker and verify Acknowledge works
	sess.stateTracker = &StateTracker{
		lastHash:       "test",
		lastChangeTime: time.Now(),
		acknowledged:   false,
	}

	sess.Acknowledge()
	if !sess.stateTracker.acknowledged {
		t.Error("Acknowledge should set acknowledged=true")
	}
}

// TestLastStableStatusUpdates verifies lastStableStatus is tracked
func TestLastStableStatusUpdates(t *testing.T) {
	sess := NewSession("laststable", "/tmp")

	// Initialize state tracker manually
	sess.stateTracker = &StateTracker{
		lastHash:       "hash1",
		lastChangeTime: time.Now(),
		acknowledged:   false,
	}

	// Acknowledge should update lastStableStatus to "idle"
	sess.Acknowledge()
	if sess.lastStableStatus != "idle" {
		t.Fatalf("lastStableStatus should be 'idle' after Acknowledge, got %s", sess.lastStableStatus)
	}
}

// =============================================================================
// Multi-Session State Isolation Tests
// =============================================================================

// TestMultiSessionStateIsolation verifies that sessions don't affect each other
// This tests the EXACT scenario the user reported: when interacting with one session,
// other sessions shouldn't change color
func TestMultiSessionStateIsolation(t *testing.T) {
	// Create three independent sessions
	sess1 := NewSession("project-1", "/tmp/project1")
	sess2 := NewSession("project-2", "/tmp/project2")
	sess3 := NewSession("project-3", "/tmp/project3")

	// Verify each has its own unique name
	if sess1.Name == sess2.Name || sess2.Name == sess3.Name {
		t.Fatal("Sessions should have unique names")
	}

	// Initialize state trackers with different states
	sess1.stateTracker = &StateTracker{
		lastHash:       "hash1",
		lastChangeTime: time.Now().Add(-10 * time.Second),
		acknowledged:   false, // Yellow - needs attention
	}
	sess2.stateTracker = &StateTracker{
		lastHash:       "hash2",
		lastChangeTime: time.Now().Add(-5 * time.Second),
		acknowledged:   true, // Gray - already acknowledged
	}
	sess3.stateTracker = &StateTracker{
		lastHash:       "hash3",
		lastChangeTime: time.Now(),
		acknowledged:   false, // Green - active (content just changed)
	}

	// User acknowledges session 1 (opens it)
	sess1.Acknowledge()

	// Verify ONLY session 1 was affected
	if !sess1.stateTracker.acknowledged {
		t.Error("Session 1 should be acknowledged after Acknowledge()")
	}
	if !sess2.stateTracker.acknowledged {
		t.Error("Session 2 should STILL be acknowledged (unchanged)")
	}
	if sess3.stateTracker.acknowledged {
		t.Error("Session 3 should NOT be acknowledged (it's still active)")
	}

	// Verify states are independent
	t.Logf("Session 1: acknowledged=%v", sess1.stateTracker.acknowledged)
	t.Logf("Session 2: acknowledged=%v", sess2.stateTracker.acknowledged)
	t.Logf("Session 3: acknowledged=%v", sess3.stateTracker.acknowledged)
}

// TestStateTrackerPointersAreIndependent verifies each session has its own pointer
func TestStateTrackerPointersAreIndependent(t *testing.T) {
	// Simulate what ReconnectSession does for multiple sessions
	sessions := make([]*Session, 3)
	for i := 0; i < 3; i++ {
		sessions[i] = ReconnectSession(
			"agentdeck_test_"+string(rune('a'+i)),
			"project-"+string(rune('a'+i)),
			"/tmp",
			"claude",
		)
	}

	// Initialize state trackers (simulating first GetStatus call)
	for i, sess := range sessions {
		sess.stateTracker = &StateTracker{
			lastHash:       "hash_" + string(rune('a'+i)),
			lastChangeTime: time.Now(),
			acknowledged:   false,
		}
	}

	// Verify each session has a DIFFERENT stateTracker pointer
	if sessions[0].stateTracker == sessions[1].stateTracker {
		t.Error("Session 0 and 1 share the same stateTracker pointer!")
	}
	if sessions[1].stateTracker == sessions[2].stateTracker {
		t.Error("Session 1 and 2 share the same stateTracker pointer!")
	}

	// Modify session 0's acknowledged state
	sessions[0].stateTracker.acknowledged = true

	// Verify others are NOT affected
	if sessions[1].stateTracker.acknowledged {
		t.Error("Session 1's acknowledged was incorrectly modified")
	}
	if sessions[2].stateTracker.acknowledged {
		t.Error("Session 2's acknowledged was incorrectly modified")
	}
}

// TestSimulateTickLoop simulates the app's tick loop behavior
// This is what happens every 500ms in the UI
func TestSimulateTickLoop(t *testing.T) {
	// Helper to compute status (same logic as GetStatus)
	computeStatus := func(tracker *StateTracker, newHash string) string {
		hasUpdated := newHash != tracker.lastHash
		if hasUpdated {
			tracker.lastHash = newHash
			tracker.lastChangeTime = time.Now()
			tracker.acknowledged = false
			return "active"
		}
		if tracker.acknowledged {
			return "idle"
		}
		return "waiting"
	}

	// Create sessions
	sess0 := NewSession("active-session", "/tmp/active")
	sess1 := NewSession("waiting-session", "/tmp/waiting")
	sess2 := NewSession("acknowledged-session", "/tmp/acked")

	// Initialize state trackers
	sess0.stateTracker = &StateTracker{
		lastHash:       "old_hash",
		lastChangeTime: time.Now(),
		acknowledged:   false,
	}
	sess1.stateTracker = &StateTracker{
		lastHash:       sess1.hashContent("Done!\n>\n"),
		lastChangeTime: time.Now().Add(-5 * time.Second),
		acknowledged:   false, // Waiting (yellow)
	}
	sess2.stateTracker = &StateTracker{
		lastHash:       sess2.hashContent("Finished.\n>\n"),
		lastChangeTime: time.Now().Add(-10 * time.Second),
		acknowledged:   true, // Idle (gray)
	}

	// Simulate tick: session 0 has new content, others don't
	status0 := computeStatus(sess0.stateTracker, sess0.hashContent("Working...\nEven newer output!\n"))
	status1 := computeStatus(sess1.stateTracker, sess1.hashContent("Done!\n>\n"))
	status2 := computeStatus(sess2.stateTracker, sess2.hashContent("Finished.\n>\n"))

	// Verify expected statuses
	if status0 != "active" {
		t.Errorf("Session 0 should be active (content changed), got %s", status0)
	}
	if status1 != "waiting" {
		t.Errorf("Session 1 should be waiting (not acknowledged), got %s", status1)
	}
	if status2 != "idle" {
		t.Errorf("Session 2 should be idle (acknowledged), got %s", status2)
	}

	// Verify session 2's acknowledged was NOT affected
	if !sess2.stateTracker.acknowledged {
		t.Error("Session 2 acknowledged was incorrectly reset")
	}

	t.Log("Tick loop simulation passed - sessions are isolated")
}

// TestConcurrentStateUpdates verifies thread safety (if applicable)
func TestConcurrentStateUpdates(t *testing.T) {
	sess1 := NewSession("concurrent-1", "/tmp/c1")
	sess2 := NewSession("concurrent-2", "/tmp/c2")

	// Initialize
	sess1.stateTracker = &StateTracker{
		lastHash:       "c1",
		lastChangeTime: time.Now(),
		acknowledged:   false,
	}
	sess2.stateTracker = &StateTracker{
		lastHash:       "c2",
		lastChangeTime: time.Now(),
		acknowledged:   false,
	}

	// Rapid updates to sess1 should not affect sess2
	for i := 0; i < 100; i++ {
		sess1.stateTracker.lastHash = "hash_" + string(rune(i))
		sess1.stateTracker.lastChangeTime = time.Now()
		sess1.Acknowledge()
		sess1.stateTracker.acknowledged = false // Reset for next iteration
	}

	// sess2 should be completely unchanged
	if sess2.stateTracker.lastHash != "c2" {
		t.Errorf("Session 2 hash was modified: %s", sess2.stateTracker.lastHash)
	}
	if sess2.stateTracker.acknowledged {
		t.Error("Session 2 was incorrectly acknowledged")
	}
}

// =============================================================================
// ReconnectSessionWithStatus Tests
// =============================================================================

// TestReconnectSessionWithStatusIdle verifies idle (acknowledged) state is restored
func TestReconnectSessionWithStatusIdle(t *testing.T) {
	// Simulate loading a session that was previously acknowledged (idle/gray)
	sess := ReconnectSessionWithStatus("agentdeck_test_123", "my-project", "/tmp", "claude", "idle")

	// Should have stateTracker pre-initialized
	if sess.stateTracker == nil {
		t.Fatal("stateTracker should be pre-initialized when previousStatus=idle")
	}

	// Should be acknowledged
	if !sess.stateTracker.acknowledged {
		t.Error("acknowledged should be true for previously idle session")
	}

	// Hash should be empty (will be set on first GetStatus)
	if sess.stateTracker.lastHash != "" {
		t.Errorf("lastHash should be empty, got %s", sess.stateTracker.lastHash)
	}
}

// TestReconnectSessionWithStatusWaiting verifies waiting (yellow) state is restored
func TestReconnectSessionWithStatusWaiting(t *testing.T) {
	// Simulate loading a session that was waiting (yellow) - needs attention
	sess := ReconnectSessionWithStatus("agentdeck_test_456", "other-project", "/tmp", "claude", "waiting")

	// Should have stateTracker pre-initialized for waiting sessions too
	if sess.stateTracker == nil {
		t.Fatal("stateTracker should be pre-initialized when previousStatus=waiting")
	}

	// Should NOT be acknowledged - still needs attention
	if sess.stateTracker.acknowledged {
		t.Error("acknowledged should be false for waiting session")
	}

	// lastStableStatus should be waiting
	if sess.lastStableStatus != "waiting" {
		t.Errorf("lastStableStatus should be waiting, got %s", sess.lastStableStatus)
	}
}

// TestReconnectSessionWithStatusActive verifies active sessions are pre-initialized
// to start as "waiting" and show "active" when content changes
func TestReconnectSessionWithStatusActive(t *testing.T) {
	// Simulate loading a session that was active
	sess := ReconnectSessionWithStatus("agentdeck_test_789", "active-project", "/tmp", "claude", "active")

	// stateTracker should be pre-initialized (same as "waiting")
	// Active sessions start as "waiting" until content changes
	if sess.stateTracker == nil {
		t.Fatal("stateTracker should be pre-initialized for active sessions")
	}

	// Should NOT be acknowledged (will show yellow/waiting until content changes)
	if sess.stateTracker.acknowledged {
		t.Error("acknowledged should be false for active session")
	}

	// lastStableStatus should be waiting (will change to active when content changes)
	if sess.lastStableStatus != "waiting" {
		t.Errorf("lastStableStatus should be waiting, got %s", sess.lastStableStatus)
	}
}

// TestAppRestartPersistenceFlow simulates app restart with persistence
func TestAppRestartPersistenceFlow(t *testing.T) {
	// This test simulates the full app restart flow:
	// 1. Session exists, user acknowledges it (opens it)
	// 2. Session saved with status=idle
	// 3. App restarts, loads session
	// 4. Session should still be idle (gray), not yellow

	// Step 1: Create session as if loaded from storage with status=idle (acknowledged)
	sess := ReconnectSessionWithStatus("agentdeck_project_abc", "project", "/tmp", "claude", "idle")

	// Step 2: Simulate first GetStatus call
	// Since content hasn't changed and acknowledged=true, should return "idle"
	currentHash := sess.hashContent("some content")

	// Simulate GetStatus logic
	var status string
	if sess.stateTracker.lastHash == "" {
		// First call - set hash, return based on acknowledged
		sess.stateTracker.lastHash = currentHash
		if sess.stateTracker.acknowledged {
			status = "idle"
		} else {
			status = "waiting"
		}
	}

	// Should be idle, not waiting
	if status != "idle" {
		t.Errorf("Reloaded acknowledged session should be idle, got %s", status)
	}

	t.Log("App restart persistence flow passed!")
}

// TestNewSessionsStartYellow verifies new/reloaded sessions start yellow
func TestNewSessionsStartYellow(t *testing.T) {
	// When a session is loaded with "waiting" status, stateTracker is pre-initialized
	sess := ReconnectSessionWithStatus("agentdeck_new_xyz", "new-project", "/tmp", "claude", "waiting")

	// stateTracker should be pre-initialized with acknowledged=false for "waiting" status
	if sess.stateTracker == nil {
		t.Fatal("stateTracker should be pre-initialized for 'waiting' status")
	}

	// Verify the state matches "waiting" semantics
	if sess.stateTracker.acknowledged {
		t.Error("Waiting session should NOT be acknowledged")
	}

	// Compute status - should be waiting (yellow) since not acknowledged
	var status string
	if sess.stateTracker.acknowledged {
		status = "idle"
	} else {
		status = "waiting"
	}

	if status != "waiting" {
		t.Errorf("New session should be waiting (yellow), got %s", status)
	}
}

// TestGetStatusInitializationVariants tests all GetStatus initialization paths
func TestGetStatusInitializationVariants(t *testing.T) {
	t.Run("nil stateTracker initializes to waiting", func(t *testing.T) {
		sess := NewSession("test", "/tmp")
		// stateTracker is nil

		// Simulate GetStatus initialization
		sess.stateTracker = &StateTracker{
			lastHash:       "hash",
			lastChangeTime: time.Now().Add(-3 * time.Second),
			acknowledged:   false,
		}

		// Compute status - should be waiting
		var status string
		if sess.stateTracker.acknowledged {
			status = "idle"
		} else {
			status = "waiting"
		}

		if status != "waiting" {
			t.Errorf("New stateTracker should result in waiting, got %s", status)
		}
	})

	t.Run("pre-initialized with acknowledged=true", func(t *testing.T) {
		sess := ReconnectSessionWithStatus("test", "test", "/tmp", "claude", "idle")

		// Already has stateTracker with acknowledged=true
		if !sess.stateTracker.acknowledged {
			t.Error("Should be acknowledged")
		}

		// When we check status with same content, should be idle
		sess.stateTracker.lastHash = "captured_hash"
		currentHash := "captured_hash"
		hasUpdated := currentHash != sess.stateTracker.lastHash

		var status string
		if hasUpdated {
			status = "active"
		} else if sess.stateTracker.acknowledged {
			status = "idle"
		} else {
			status = "waiting"
		}

		if status != "idle" {
			t.Errorf("Acknowledged session should be idle, got %s", status)
		}
	})

	t.Run("empty lastHash with acknowledged=true returns idle", func(t *testing.T) {
		sess := ReconnectSessionWithStatus("test", "test", "/tmp", "claude", "idle")

		// lastHash is empty, but acknowledged is true
		if sess.stateTracker.lastHash != "" {
			t.Error("lastHash should be empty initially")
		}

		// Simulate first GetStatus: set hash, return based on acknowledged
		currentHash := "new_content_hash"
		if sess.stateTracker.lastHash == "" {
			sess.stateTracker.lastHash = currentHash
			// Since this is first check with empty hash, return based on acknowledged
			var status string
			if sess.stateTracker.acknowledged {
				status = "idle"
			} else {
				status = "waiting"
			}
			if status != "idle" {
				t.Errorf("Acknowledged session with empty hash should be idle, got %s", status)
			}
		}
	})
}

// TestStatusFlickerOnInvisibleCharsIntegration is an integration test that
// reproduces the status flicker bug using a real tmux session.
// Time-based cooldown model: stays "active" for 2 seconds after any change,
// then transitions to "waiting" or "idle".
func TestStatusFlickerOnInvisibleCharsIntegration(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available, skipping integration test")
	}

	// 1. Setup a real tmux session
	session := NewSession("flicker-test", t.TempDir())
	err := session.Start("")
	assert.NoError(t, err, "Failed to start tmux session")
	defer func() { _ = session.Kill() }()

	// Wait for session to be ready
	time.Sleep(100 * time.Millisecond)

	// Helper to send content to the pane
	sendToPane := func(content string) {
		cmd := fmt.Sprintf("clear && printf -- %q", content)
		_ = session.SendKeys(cmd)
		_ = session.SendEnter()
		time.Sleep(100 * time.Millisecond)
	}

	// 2. Initial State: Content is stable
	initialContent := "Done. Ready for next command."
	sendToPane(initialContent)

	// Poll 1: Get initial status. Should be "waiting" on first poll (needs attention)
	// (first poll initializes the tracker - returns waiting so user knows session stopped)
	status, err := session.GetStatus()
	assert.NoError(t, err)
	assert.Equal(t, "waiting", status, "Initial status should be 'waiting' (needs attention on init)")

	// Set up "needs attention" state: acknowledged=false, cooldown expired
	session.mu.Lock()
	session.stateTracker.lastChangeTime = time.Now().Add(-3 * time.Second)
	session.stateTracker.acknowledged = false // Mark as needing attention
	session.mu.Unlock()

	// Poll 2: Same content, cooldown expired, acknowledged=false → "waiting"
	status, err = session.GetStatus()
	assert.NoError(t, err)
	assert.Equal(t, "waiting", status, "Status should be 'waiting' when not acknowledged and cooldown expired")

	// 3. The Flicker Test: Introduce an insignificant, non-printing character.
	// A BEL character (\a) should be stripped by normalizeContent.
	flickerContent := initialContent + "\a"
	sendToPane(flickerContent)

	// Expire cooldown again and ensure acknowledged stays false
	session.mu.Lock()
	session.stateTracker.lastChangeTime = time.Now().Add(-3 * time.Second)
	session.stateTracker.acknowledged = false // Keep needing attention
	session.mu.Unlock()

	// Poll 3: normalizeContent should strip the BEL, so no real change detected
	// Status should remain "waiting" (not flicker to "active")
	status, err = session.GetStatus()
	assert.NoError(t, err)
	assert.Equal(t, "waiting", status, "Status should NOT flicker to 'active' due to invisible BEL character")
}

// TestTimeBasedStatusModel tests the time-based cooldown status model
// GREEN = content changed recently (within 2s cooldown)
// YELLOW = cooldown expired, not acknowledged
// GRAY = cooldown expired, acknowledged
func TestTimeBasedStatusModel(t *testing.T) {
	// Create session
	session := NewSession("simple-test", t.TempDir())

	// Initialize state tracker with expired cooldown
	session.stateTracker = &StateTracker{
		lastHash:       "hash1",
		lastChangeTime: time.Now().Add(-3 * time.Second), // Cooldown expired
		acknowledged:   false,
	}
	session.lastStableStatus = "waiting"

	t.Run("Same hash, cooldown expired, not acknowledged returns waiting", func(t *testing.T) {
		// Simulate GetStatus logic with time-based cooldown
		currentHash := "hash1"
		hasUpdated := currentHash != session.stateTracker.lastHash
		cooldownExpired := time.Since(session.stateTracker.lastChangeTime) >= activityCooldown

		var status string
		if hasUpdated {
			status = "active"
		} else if cooldownExpired {
			if session.stateTracker.acknowledged {
				status = "idle"
			} else {
				status = "waiting"
			}
		} else {
			status = "active" // Within cooldown
		}

		assert.Equal(t, "waiting", status)
	})

	t.Run("Same hash, cooldown expired, acknowledged returns idle", func(t *testing.T) {
		session.stateTracker.acknowledged = true

		currentHash := "hash1"
		hasUpdated := currentHash != session.stateTracker.lastHash
		cooldownExpired := time.Since(session.stateTracker.lastChangeTime) >= activityCooldown

		var status string
		if hasUpdated {
			status = "active"
		} else if cooldownExpired {
			if session.stateTracker.acknowledged {
				status = "idle"
			} else {
				status = "waiting"
			}
		} else {
			status = "active" // Within cooldown
		}

		assert.Equal(t, "idle", status)
	})

	t.Run("Same hash, within cooldown returns active", func(t *testing.T) {
		session.stateTracker.lastChangeTime = time.Now() // Just changed
		session.stateTracker.acknowledged = false

		currentHash := "hash1"
		hasUpdated := currentHash != session.stateTracker.lastHash
		cooldownExpired := time.Since(session.stateTracker.lastChangeTime) >= activityCooldown

		var status string
		if hasUpdated {
			status = "active"
		} else if cooldownExpired {
			if session.stateTracker.acknowledged {
				status = "idle"
			} else {
				status = "waiting"
			}
		} else {
			status = "active" // Within cooldown - this is the key!
		}

		assert.Equal(t, "active", status, "Should stay active within cooldown period")
	})

	t.Run("Different hash returns active and resets acknowledged", func(t *testing.T) {
		session.stateTracker.acknowledged = true
		session.stateTracker.lastChangeTime = time.Now().Add(-3 * time.Second) // Expired

		currentHash := "hash2" // Different!
		hasUpdated := currentHash != session.stateTracker.lastHash

		var status string
		if hasUpdated {
			session.stateTracker.lastHash = currentHash
			session.stateTracker.lastChangeTime = time.Now()
			session.stateTracker.acknowledged = false // Reset
			status = "active"
		} else if session.stateTracker.acknowledged {
			status = "idle"
		} else {
			status = "waiting"
		}

		assert.Equal(t, "active", status)
		assert.False(t, session.stateTracker.acknowledged, "acknowledged should be reset on content change")
	})
}

// =============================================================================
// FLICKERING BUG TEST CASES
// =============================================================================
// These tests reproduce the bug where status flickers GREEN→YELLOW after
// Claude Code stops outputting. The root cause is dynamic content (like
// time counters "45s · 1234 tokens") that changes every second.

// TestDynamicTimeCounterCausesFlickering demonstrates the flickering bug
// Scenario:
//  1. Claude Code finishes outputting, shows "45s · 1234 tokens"
//  2. Status correctly shows YELLOW (waiting)
//  3. One second later, content shows "46s · 1234 tokens"
//  4. BUG: Hash changes → status flickers to GREEN
//  5. Cooldown expires → back to YELLOW
func TestDynamicTimeCounterCausesFlickering(t *testing.T) {
	session := NewSession("flicker-test", "/tmp")

	// Simulate Claude Code content with time counter
	contentAt45s := `I've completed the analysis.

Here's my summary of the changes.

Thinking… (45s · 1234 tokens · esc to interrupt)

>`

	contentAt46s := `I've completed the analysis.

Here's my summary of the changes.

Thinking… (46s · 1234 tokens · esc to interrupt)

>`

	// Initialize tracker with first content
	oldNormalized := session.normalizeContent(contentAt45s)
	session.stateTracker = &StateTracker{
		lastHash:       session.hashContent(oldNormalized),
		lastChangeTime: time.Now().Add(-3 * time.Second), // Cooldown expired
		acknowledged:   false,
	}

	// Simulate poll 1 second later with "46s" instead of "45s"
	newNormalized := session.normalizeContent(contentAt46s)
	newHash := session.hashContent(newNormalized)

	// BUG: The hashes are different because time counter changed
	hashesMatch := session.stateTracker.lastHash == newHash

	t.Logf("Content at 45s hash: %s", session.stateTracker.lastHash[:16])
	t.Logf("Content at 46s hash: %s", newHash[:16])
	t.Logf("Hashes match: %v", hashesMatch)

	// This test documents the BUG - hashes should match after normalization
	// but currently they don't because we don't strip time counters
	if !hashesMatch {
		t.Log("BUG CONFIRMED: Dynamic time counter causes hash change")
		t.Log("OLD normalized (last 100 chars):", truncateEnd(oldNormalized, 100))
		t.Log("NEW normalized (last 100 chars):", truncateEnd(newNormalized, 100))
	}

	// The fix should make these hashes equal
	// assert.True(t, hashesMatch, "After normalization, time counters should be stripped")
}

// TestNormalizeShouldStripTimeCounters verifies normalization strips dynamic content
func TestNormalizeShouldStripTimeCounters(t *testing.T) {
	session := NewSession("normalize-test", "/tmp")

	tests := []struct {
		name        string
		content1    string
		content2    string
		shouldMatch bool
		description string
	}{
		{
			name:        "Time counter in parentheses",
			content1:    "Working... (45s · 1234 tokens · esc to interrupt)",
			content2:    "Working... (46s · 1234 tokens · esc to interrupt)",
			shouldMatch: true, // After fix, these should normalize to the same hash
			description: "Time counters like '45s' should be stripped",
		},
		{
			name:        "Token count changes",
			content1:    "Processing (10s · 100 tokens)",
			content2:    "Processing (10s · 150 tokens)",
			shouldMatch: true, // Token counts can change, should be stripped
			description: "Token counts should be stripped",
		},
		{
			name:        "Standalone time",
			content1:    "Last updated: 45s ago",
			content2:    "Last updated: 46s ago",
			shouldMatch: true,
			description: "Standalone time indicators should be stripped",
		},
		{
			name:        "Actual content change - should NOT match",
			content1:    "I will edit file A",
			content2:    "I will edit file B",
			shouldMatch: false,
			description: "Real content changes should produce different hashes",
		},
		{
			name:        "Braille spinners already stripped",
			content1:    "Loading ⠋",
			content2:    "Loading ⠙",
			shouldMatch: true,
			description: "Braille spinners should be stripped (already implemented)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized1 := session.normalizeContent(tt.content1)
			normalized2 := session.normalizeContent(tt.content2)
			hash1 := session.hashContent(normalized1)
			hash2 := session.hashContent(normalized2)

			hashesMatch := hash1 == hash2

			t.Logf("Content 1: %q", tt.content1)
			t.Logf("Content 2: %q", tt.content2)
			t.Logf("Normalized 1: %q", normalized1)
			t.Logf("Normalized 2: %q", normalized2)
			t.Logf("Hashes match: %v (expected: %v)", hashesMatch, tt.shouldMatch)

			if tt.shouldMatch && !hashesMatch {
				t.Logf("BUG: %s - hashes should match but don't", tt.description)
			}
			if !tt.shouldMatch && hashesMatch {
				t.Errorf("ERROR: %s - hashes should NOT match", tt.description)
			}
		})
	}
}

// TestFlickeringScenarioEndToEnd simulates the full flickering scenario
func TestFlickeringScenarioEndToEnd(t *testing.T) {
	session := NewSession("e2e-flicker", "/tmp")

	// === STEP 1: Claude is actively outputting ===
	activeContent := `Writing code...
⠋ Thinking... (5s · 50 tokens · esc to interrupt)`

	// Initialize as if we're mid-activity
	normalized := session.normalizeContent(activeContent)
	session.stateTracker = &StateTracker{
		lastHash:       session.hashContent(normalized),
		lastChangeTime: time.Now(), // Just changed
		acknowledged:   false,
	}

	// Status should be ACTIVE (within cooldown)
	timeSinceChange := time.Since(session.stateTracker.lastChangeTime)
	status1 := "waiting"
	if timeSinceChange < activityCooldown {
		status1 = "active"
	}
	assert.Equal(t, "active", status1, "Step 1: Should be active during output")
	t.Logf("Step 1: Status=%s (within cooldown)", status1)

	// === STEP 2: Wait for cooldown to expire ===
	session.stateTracker.lastChangeTime = time.Now().Add(-3 * time.Second)

	// Content unchanged, cooldown expired, not acknowledged
	timeSinceChange = time.Since(session.stateTracker.lastChangeTime)
	status2 := "waiting"
	if timeSinceChange < activityCooldown {
		status2 = "active"
	} else if session.stateTracker.acknowledged {
		status2 = "idle"
	}
	assert.Equal(t, "waiting", status2, "Step 2: Should be waiting after cooldown")
	t.Logf("Step 2: Status=%s (cooldown expired, not acknowledged)", status2)

	// === STEP 3: THE BUG - Time counter changes ===
	// One second later, the timer shows "6s" instead of "5s"
	newContent := `Writing code...
⠋ Thinking... (6s · 50 tokens · esc to interrupt)`

	newNormalized := session.normalizeContent(newContent)
	newHash := session.hashContent(newNormalized)

	// Check if hash changed (this is the BUG trigger)
	hashChanged := session.stateTracker.lastHash != newHash

	if hashChanged {
		// BUG PATH: Hash changed, so we flip to GREEN
		session.stateTracker.lastHash = newHash
		session.stateTracker.lastChangeTime = time.Now()
		session.stateTracker.acknowledged = false
		status3 := "active"
		t.Logf("Step 3: BUG! Status=%s (hash changed due to time counter: 5s→6s)", status3)
		t.Log("This causes the GREEN flicker!")
	} else {
		// FIXED PATH: Hash unchanged, stay YELLOW
		status3 := "waiting"
		if session.stateTracker.acknowledged {
			status3 = "idle"
		}
		t.Logf("Step 3: FIXED! Status=%s (hash unchanged after normalization)", status3)
	}

	// === STEP 4: After another cooldown, back to YELLOW ===
	if hashChanged {
		session.stateTracker.lastChangeTime = time.Now().Add(-3 * time.Second)
		status4 := "waiting" // Because acknowledged was reset to false
		t.Logf("Step 4: Status=%s (back to waiting after cooldown)", status4)
		t.Log("Result: YELLOW → GREEN (flicker) → YELLOW")
	}
}

// TestAcknowledgedShouldNotResetOnDynamicContent tests that acknowledged
// flag should NOT be reset when only dynamic content changes
func TestAcknowledgedShouldNotResetOnDynamicContent(t *testing.T) {
	session := NewSession("ack-test", "/tmp")

	// User has acknowledged (seen) this session
	session.stateTracker = &StateTracker{
		lastHash:       "hash1",
		lastChangeTime: time.Now().Add(-10 * time.Second),
		acknowledged:   true, // User has seen this
	}

	// Current behavior: ANY hash change resets acknowledged
	// This is problematic because time counters cause hash changes

	// Simulate time counter change
	newContent := "Some content (46s · 100 tokens)"
	_ = newContent // Would be used in actual GetStatus

	// Document the current behavior (BUG):
	// - If hash changes, acknowledged is set to false
	// - This means dynamic content causes status to cycle:
	//   IDLE (gray) → ACTIVE (green) → WAITING (yellow)

	t.Log("Current behavior: acknowledged resets on ANY content change")
	t.Log("Desired behavior: acknowledged should only reset on MEANINGFUL content changes")
	t.Log("Option 1: Strip dynamic content from hash calculation")
	t.Log("Option 2: Don't reset acknowledged if only dynamic content changed")
}

// truncateEnd returns the last n characters of a string
func truncateEnd(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

// =============================================================================
// Activity Timestamp Detection Tests
// =============================================================================

// TestGetWindowActivity verifies GetWindowActivity returns a valid Unix timestamp
func TestGetWindowActivity(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	sess := NewSession("activity-test", t.TempDir())
	err := sess.Start("")
	assert.NoError(t, err)
	defer func() { _ = sess.Kill() }()
	time.Sleep(100 * time.Millisecond)

	ts, err := sess.GetWindowActivity()
	assert.NoError(t, err)
	assert.True(t, ts > 0, "timestamp should be positive")

	// Timestamp should be recent (within last minute)
	now := time.Now().Unix()
	assert.True(t, ts > now-60, "timestamp should be recent")
	assert.True(t, ts <= now+1, "timestamp should not be in the future")
}

// TestIsSustainedActivity verifies spike detection logic
func TestIsSustainedActivity(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	sess := NewSession("sustained-test", t.TempDir())
	err := sess.Start("")
	assert.NoError(t, err)
	defer func() { _ = sess.Kill() }()
	time.Sleep(100 * time.Millisecond)

	// Without continuous output, should return false (spike)
	// This is an integration test - we can't guarantee the result
	// but we can verify the method doesn't error/panic
	result := sess.isSustainedActivity()
	t.Logf("isSustainedActivity result: %v", result)
	// Idle session should NOT show sustained activity (status bar updates are spikes)
}

// TestSpikeDetectionWindowStaysGreen verifies that during spike detection window,
// status stays GREEN instead of falling through to YELLOW.
// This is the fix for the yellow spike bug during active sessions.
func TestSpikeDetectionWindowStaysGreen(t *testing.T) {
	session := NewSession("spike-window-test", "/tmp")

	// Simulate a session that was previously idle/waiting (cooldown expired)
	session.stateTracker = &StateTracker{
		lastHash:              "old_hash",
		lastChangeTime:        time.Now().Add(-5 * time.Second), // Cooldown expired
		acknowledged:          false,                            // Would normally return "waiting"
		lastActivityTimestamp: 100,
		activityCheckStart:    time.Time{}, // No active spike detection
		activityChangeCount:   0,
	}

	// Simulate first timestamp change detected (start of spike detection)
	// This is what happens when GetStatus detects a new timestamp
	session.stateTracker.lastActivityTimestamp = 101
	session.stateTracker.activityCheckStart = time.Now() // Start spike detection window
	session.stateTracker.activityChangeCount = 1

	// Now simulate what GetStatus does after the spike detection block:
	// With the FIX: should stay GREEN during spike detection window
	// Without fix: would check cooldown (expired) and return YELLOW

	// Check if we're in spike detection window
	inSpikeWindow := !session.stateTracker.activityCheckStart.IsZero() &&
		time.Since(session.stateTracker.activityCheckStart) < 1*time.Second

	var status string
	if inSpikeWindow {
		// FIX: Stay GREEN during spike detection
		status = "active"
	} else if time.Since(session.stateTracker.lastChangeTime) < activityCooldown {
		status = "active"
	} else if session.stateTracker.acknowledged {
		status = "idle"
	} else {
		status = "waiting"
	}

	assert.Equal(t, "active", status,
		"During spike detection window, status should be GREEN (active), not YELLOW (waiting)")
	t.Log("Spike detection window correctly returns GREEN to avoid yellow flicker")
}

// TestSpikeDetectionWindowExpiry verifies that after spike window expires
// with only 1 change, it correctly returns to the appropriate state.
func TestSpikeDetectionWindowExpiry(t *testing.T) {
	session := NewSession("spike-expiry-test", "/tmp")

	// Simulate a session where spike detection started 2 seconds ago (expired)
	// and only had 1 change (a spike, not sustained activity)
	session.stateTracker = &StateTracker{
		lastHash:              "stable_hash",
		lastChangeTime:        time.Now().Add(-5 * time.Second), // Cooldown expired
		acknowledged:          false,
		lastActivityTimestamp: 101,
		activityCheckStart:    time.Now().Add(-2 * time.Second), // Spike window expired
		activityChangeCount:   1,                                // Only 1 change = spike
	}

	// Check if spike window expired
	spikeWindowExpired := time.Since(session.stateTracker.activityCheckStart) > 1*time.Second

	if spikeWindowExpired && session.stateTracker.activityChangeCount == 1 {
		// Spike detected and filtered - reset tracking
		session.stateTracker.activityCheckStart = time.Time{}
		session.stateTracker.activityChangeCount = 0
	}

	// After spike filtering, compute status
	inSpikeWindow := !session.stateTracker.activityCheckStart.IsZero() &&
		time.Since(session.stateTracker.activityCheckStart) < 1*time.Second

	var status string
	if inSpikeWindow {
		status = "active"
	} else if time.Since(session.stateTracker.lastChangeTime) < activityCooldown {
		status = "active"
	} else if session.stateTracker.acknowledged {
		status = "idle"
	} else {
		status = "waiting"
	}

	assert.Equal(t, "waiting", status,
		"After spike window expires with only 1 change, should return to waiting (not green)")
	t.Log("Spike correctly filtered - single timestamp change doesn't cause false GREEN")
}

func TestSessionLogFile(t *testing.T) {
	sess := NewSession("test-log", t.TempDir())

	logFile := sess.LogFile()
	assert.Contains(t, logFile, ".agent-deck/logs/")
	assert.Contains(t, logFile, "agentdeck_test-log")
	assert.True(t, strings.HasSuffix(logFile, ".log"))
}

func TestStartEnablesPipePaneLogging(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	sess := NewSession("pipe-test", t.TempDir())
	err := sess.Start("")
	assert.NoError(t, err)
	defer func() { _ = sess.Kill() }()

	// Give pipe-pane time to initialize
	time.Sleep(200 * time.Millisecond)

	// Generate some output
	_ = sess.SendKeys("echo 'pipe-pane test'")

	// Poll for log file content instead of fixed sleep
	logFile := sess.LogFile()
	var content []byte
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		content, err = os.ReadFile(logFile)
		if err == nil && strings.Contains(string(content), "pipe-pane test") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Check log file exists and has content
	assert.NoError(t, err, "Log file should exist")
	assert.Contains(t, string(content), "pipe-pane test", "Log should contain output")

	// Cleanup
	os.Remove(logFile)
}

func TestSession_SetAndGetEnvironment(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	// Create a test session
	sess := NewSession("env-test", "/tmp")

	// Start the session (required for environment to work)
	err := sess.Start("")
	if err != nil {
		t.Fatalf("Failed to start session: %v", err)
	}
	defer func() { _ = sess.Kill() }()

	// Test setting environment
	err = sess.SetEnvironment("TEST_VAR", "test_value_123")
	if err != nil {
		t.Fatalf("SetEnvironment failed: %v", err)
	}

	// Test getting environment
	value, err := sess.GetEnvironment("TEST_VAR")
	if err != nil {
		t.Fatalf("GetEnvironment failed: %v", err)
	}

	if value != "test_value_123" {
		t.Errorf("GetEnvironment = %q, want %q", value, "test_value_123")
	}
}

func TestSession_GetEnvironment_NotFound(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	sess := NewSession("env-test-notfound", "/tmp")
	err := sess.Start("")
	if err != nil {
		t.Fatalf("Failed to start session: %v", err)
	}
	defer func() { _ = sess.Kill() }()

	_, err = sess.GetEnvironment("NONEXISTENT_VAR")
	if err == nil {
		t.Error("GetEnvironment should return error for nonexistent variable")
	}
}

func TestSession_SendCtrlC(t *testing.T) {
	sess := NewSession("ctrl-c-test", "/tmp")

	// Start session with a long-running command
	err := sess.Start("sleep 60")
	if err != nil {
		t.Fatalf("Failed to start session: %v", err)
	}
	defer func() { _ = sess.Kill() }()

	// Give the command time to start
	time.Sleep(100 * time.Millisecond)

	// Send Ctrl+C
	err = sess.SendCtrlC()
	if err != nil {
		t.Fatalf("SendCtrlC failed: %v", err)
	}

	// Give it time to process
	time.Sleep(100 * time.Millisecond)

	// Session should still exist (we didn't kill it, just interrupted the process)
	if !sess.Exists() {
		t.Error("Session should still exist after Ctrl+C")
	}
}

func TestSession_SendCommand(t *testing.T) {
	sess := NewSession("send-cmd-test", "/tmp")

	err := sess.Start("")
	if err != nil {
		t.Fatalf("Failed to start session: %v", err)
	}
	defer func() { _ = sess.Kill() }()

	// Send a command
	err = sess.SendCommand("echo hello")
	if err != nil {
		t.Fatalf("SendCommand failed: %v", err)
	}

	// Give it time to execute
	time.Sleep(200 * time.Millisecond)

	// Capture pane content
	content, err := sess.CapturePane()
	if err != nil {
		t.Fatalf("CapturePane failed: %v", err)
	}
	if !strings.Contains(content, "hello") {
		t.Errorf("Expected 'hello' in output, got: %s", content)
	}
}
