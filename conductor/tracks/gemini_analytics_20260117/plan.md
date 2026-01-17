# Implementation Plan - Gemini Session Analytics

## Phase 1: Core Analytics Tracking
- [x] Task: Create `GeminiSessionAnalytics` struct and storage logic [a71922a]
    - [ ] Sub-task: Write tests for `GeminiSessionAnalytics` struct initialization and JSON marshaling/unmarshaling
    - [ ] Sub-task: Implement `GeminiSessionAnalytics` struct and `Save`/`Load` methods in `internal/session/analytics.go` (or similar)
- [x] Task: Integrate analytics tracking into Gemini session lifecycle [e1b535a]
    - [ ] Sub-task: Write tests for `Start`, `End`, and `Update` methods in `GeminiSession` to ensure they call analytics methods
    - [ ] Sub-task: Implement `Start`, `End`, and `Update` hooks in `internal/session/gemini.go` to capture start time, duration, and token usage
- [ ] Task: Implement cost calculation logic
    - [ ] Sub-task: Write tests for `CalculateCost` function with various model types and token counts
    - [ ] Sub-task: Implement `CalculateCost` function based on current Gemini pricing
- [ ] Task: Conductor - User Manual Verification 'Core Analytics Tracking' (Protocol in workflow.md)

## Phase 2: TUI Integration
- [ ] Task: Update Analytics Panel to support Gemini sessions
    - [ ] Sub-task: Write tests for `AnalyticsPanel` view model to ensure it can render Gemini analytics data
    - [ ] Sub-task: Update `internal/ui/analytics_panel.go` to fetch and display data from `GeminiSessionAnalytics`
- [ ] Task: Conductor - User Manual Verification 'TUI Integration' (Protocol in workflow.md)

## Phase 3: Persistence and Polish
- [ ] Task: Ensure analytics data is persisted to `sessions.json`
    - [ ] Sub-task: Write integration tests to verify that analytics data survives a session save/load cycle
    - [ ] Sub-task: Verify and adjust `internal/session/storage.go` to include analytics fields in the JSON serialization
- [ ] Task: Final Polish and Refactoring
    - [ ] Sub-task: Run full test suite and ensure >80% coverage
    - [ ] Sub-task: Refactor code for readability and consistency with Claude implementation
- [ ] Task: Conductor - User Manual Verification 'Persistence and Polish' (Protocol in workflow.md)
