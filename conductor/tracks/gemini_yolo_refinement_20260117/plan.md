# Implementation Plan - Gemini YOLO Mode Refinement

## Phase 1: Foundation and State Synchronization
- [x] Task: Create feature branch `feature/gemini-yolo-refinement` from `main` [488235]
- [x] Task: Implement `SetGeminiYoloMode` logic and state synchronization [TDD] [496558]
    - [x] Task: Write failing unit tests in `internal/session/instance_test.go` to verify `SetGeminiYoloMode` updates the struct and the tmux environment (`GEMINI_YOLO_MODE`)
    - [x] Task: Implement `SetGeminiYoloMode(enabled bool)` in `internal/session/instance.go`
    - [x] Task: Update `UpdateGeminiSession` in `internal/session/instance.go` to detect YOLO mode from the tmux environment variable or process command line
- [ ] Task: Conductor - User Manual Verification 'Foundation and State Synchronization' (Protocol in workflow.md)

## Phase 2: Command Building and Persistence
- [x] Task: Refine `buildGeminiCommand` for consistent flag application [TDD] [502598]
    - [x] Task: Write failing tests in `internal/session/gemini_yolo_test.go` verifying the `--yolo` flag and `GEMINI_YOLO_MODE` env var injection
    - [x] Task: Update `buildGeminiCommand` in `internal/session/instance.go` to handle environment injection and flag persistence across restarts
- [x] Task: Ensure YOLO state persistence in storage [504367]
    - [x] Task: Verify/Update `internal/session/storage.go` to correctly map the `GeminiYoloMode` field during save/load operations
- [ ] Task: Conductor - User Manual Verification 'Command Building and Persistence' (Protocol in workflow.md)

## Phase 3: UI Polish and Interactions
- [ ] Task: Implement `[YOLO]` badge and UI indicators
    - [ ] Task: Update `internal/ui/home.go` to render the `[YOLO]` badge in the session list and preview pane
    - [ ] Task: Update `internal/ui/newdialog.go` to support the refined YOLO toggle logic (`y` key) while maintaining the `geminiYoloMode` naming convention
- [ ] Task: Implement TUI hotkey and restart flow
    - [ ] Task: Add `y` hotkey handling in `internal/ui/home.go` to trigger the `ConfirmYoloRestart` dialog
    - [ ] Task: Implement the restart logic in `handleConfirmDialogKey` to apply the mode change and trigger a session restart
- [ ] Task: Conductor - User Manual Verification 'UI Polish and Interactions' (Protocol in workflow.md)

## Phase 4: Final Verification and Cleanup
- [ ] Task: Project-wide verification and quality check
    - [ ] Task: Run `make fmt` and `make lint` across the codebase
    - [ ] Task: Run all project tests `go test ./...` and ensure >80% coverage for new/modified code
- [ ] Task: Conductor - User Manual Verification 'Final Verification and Cleanup' (Protocol in workflow.md)
