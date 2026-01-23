package session

import (
	"sync"
	"testing"
	"time"
)

// TestOpenCodeFullFlowSimulation simulates what happens when agent-deck loads
// sessions and triggers detection
func TestOpenCodeFullFlowSimulation(t *testing.T) {
	t.Log("=== Full Flow Simulation ===")

	// Step 1: Simulate loading from storage (like loadSessionsMsg)
	// In real code: h.instances = msg.instances
	instances := []*Instance{
		{
			ID:          "test-opencode-001",
			Title:       "test-opencode",
			Tool:        "opencode",
			ProjectPath: "/Users/ashesh/claude-deck",
			// OpenCodeSessionID is empty - simulating loaded from disk
		},
	}

	t.Logf("Step 1: Loaded %d instances from 'storage'", len(instances))
	t.Logf("  instances[0].OpenCodeSessionID = %q", instances[0].OpenCodeSessionID)

	// Step 2: Trigger detection for OpenCode sessions without IDs
	// In real code: for _, inst := range h.instances { go inst.DetectOpenCodeSession() }
	var wg sync.WaitGroup
	for _, inst := range instances {
		if inst.Tool == "opencode" && inst.OpenCodeSessionID == "" {
			wg.Add(1)
			go func(i *Instance) {
				defer wg.Done()
				t.Logf("Step 2: Triggering detection for %s", i.Title)
				i.detectOpenCodeSessionAsync()
			}(inst)
		}
	}

	// Step 3: Wait for detection to complete
	t.Log("Step 3: Waiting for detection to complete...")
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		t.Log("Step 3: Detection completed")
	case <-time.After(15 * time.Second):
		t.Fatal("Step 3: Detection timed out!")
	}

	// Step 4: Check if the SAME instance pointer has the session ID
	t.Logf("Step 4: Checking result on SAME instance pointer")
	t.Logf("  instances[0].OpenCodeSessionID = %q", instances[0].OpenCodeSessionID)
	t.Logf("  instances[0].OpenCodeDetectedAt = %v", instances[0].OpenCodeDetectedAt)

	if instances[0].OpenCodeSessionID == "" {
		t.Error("❌ FAILED: OpenCodeSessionID is empty on the original instance!")
		t.Error("This means the detection is updating a copy, not the original")
	} else {
		t.Logf("✅ SUCCESS: Session ID detected: %s", instances[0].OpenCodeSessionID)
	}

	// Step 5: Simulate what the UI does - access via item.Session
	// In real code: selected := item.Session (which is *Instance)
	type FakeItem struct {
		Session *Instance
	}
	item := FakeItem{Session: instances[0]}

	t.Logf("Step 5: Simulating UI access via item.Session")
	t.Logf("  item.Session.OpenCodeSessionID = %q", item.Session.OpenCodeSessionID)

	if item.Session.OpenCodeSessionID == "" {
		t.Error("❌ FAILED: UI would see empty session ID!")
	} else {
		t.Logf("✅ SUCCESS: UI would see session ID: %s", item.Session.OpenCodeSessionID)
	}
}

// TestOpenCodePointerReplacementScenario simulates the bug where storage watcher
// reload creates new instance pointers, breaking the async detection flow.
// This is the exact scenario that was causing the "Detecting session..." bug.
func TestOpenCodePointerReplacementScenario(t *testing.T) {
	t.Log("=== Pointer Replacement Scenario (Bug Reproduction) ===")

	instanceID := "test-opencode-002"

	// Step 1: Create original instances (Set A) - like initial loadSessionsMsg
	originalInstances := []*Instance{
		{
			ID:          instanceID,
			Title:       "test-opencode",
			Tool:        "opencode",
			ProjectPath: "/Users/ashesh/claude-deck",
		},
	}
	t.Logf("Step 1: Original instances (Set A) created")
	t.Logf("  Set A pointer: %p", originalInstances[0])

	// Step 2: Start detection on Set A pointer (async, like tea.Cmd)
	var detectedSessionID string
	var wg sync.WaitGroup
	wg.Add(1)
	go func(inst *Instance) {
		defer wg.Done()
		t.Logf("Step 2: Detection started on Set A pointer %p", inst)
		inst.detectOpenCodeSessionAsync()
		detectedSessionID = inst.OpenCodeSessionID
		t.Logf("Step 2: Detection found session ID: %s", detectedSessionID)
	}(originalInstances[0])

	// Step 3: Simulate storage watcher reload - creates NEW pointers (Set B)
	// This happens because saveInstances() triggers the storage watcher
	time.Sleep(100 * time.Millisecond) // Let detection start
	newInstances := []*Instance{
		{
			ID:          instanceID, // Same ID
			Title:       "test-opencode",
			Tool:        "opencode",
			ProjectPath: "/Users/ashesh/claude-deck",
			// OpenCodeSessionID is empty again!
		},
	}
	t.Logf("Step 3: New instances (Set B) created from 'storage reload'")
	t.Logf("  Set B pointer: %p (different from Set A: %p)", newInstances[0], originalInstances[0])

	// Step 4: Create instanceByID map using Set B (like h.instanceByID rebuild)
	instanceByID := make(map[string]*Instance)
	instanceByID[instanceID] = newInstances[0]
	t.Logf("Step 4: instanceByID map uses Set B pointer")

	// Step 5: Wait for detection to complete
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		t.Log("Step 5: Detection completed")
	case <-time.After(15 * time.Second):
		t.Fatal("Step 5: Detection timed out!")
	}

	// Step 6: BUG SCENARIO - Set B still has empty OpenCodeSessionID
	t.Logf("Step 6: Checking Set B (current) instance")
	t.Logf("  Set A OpenCodeSessionID = %q (detection ran here)", originalInstances[0].OpenCodeSessionID)
	t.Logf("  Set B OpenCodeSessionID = %q (current h.instances)", newInstances[0].OpenCodeSessionID)

	if newInstances[0].OpenCodeSessionID != "" {
		t.Error("❌ TEST SETUP WRONG: Set B should be empty at this point!")
	}

	// Step 7: THE FIX - When detection completes, update CURRENT instance by ID
	// This simulates what the fixed openCodeDetectionCompleteMsg handler does
	if detectedSessionID != "" {
		if inst := instanceByID[instanceID]; inst != nil {
			inst.OpenCodeSessionID = detectedSessionID
			inst.OpenCodeDetectedAt = time.Now()
			t.Logf("Step 7: FIX APPLIED - Updated Set B instance via instanceByID lookup")
		}
	}

	// Step 8: Verify the fix worked
	t.Logf("Step 8: Final verification")
	t.Logf("  Set B OpenCodeSessionID = %q", newInstances[0].OpenCodeSessionID)

	if newInstances[0].OpenCodeSessionID == "" {
		t.Error("❌ FAILED: Fix did not work - Set B still has empty session ID!")
	} else {
		t.Logf("✅ SUCCESS: Set B now has session ID: %s", newInstances[0].OpenCodeSessionID)
	}

	// Step 9: Verify UI would see the correct value
	type FakeItem struct {
		Session *Instance
	}
	item := FakeItem{Session: newInstances[0]} // UI uses Set B

	if item.Session.OpenCodeSessionID == "" {
		t.Error("❌ FAILED: UI would still see empty session ID!")
	} else {
		t.Logf("✅ SUCCESS: UI would see session ID: %s", item.Session.OpenCodeSessionID)
	}
}
