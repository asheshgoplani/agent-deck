# Mobile Stacked Sessions Layout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the Preview pane and preview-fetch work from 50–79 column terminal layouts while retaining Preview at 80+ columns.

**Architecture:** Keep `LayoutModeStacked` for the existing wide-terminal `preview_orientation = "below"` preference. Add a visibility predicate that distinguishes that wide stacked layout from the mobile stacked layout; use it at renderer, sizing, and preview-fetch boundaries. The sessions-only mobile layout reuses the established single-column renderer.

**Tech Stack:** Go, Bubble Tea, Lip Gloss, Go standard testing package.

## Global Constraints

- At 50–79 columns the terminal UI shows Sessions only.
- Preview remains available at 80+ columns, including `preview_orientation = "below"`.
- Do not schedule preview work when the pane is hidden.
- Do not modify `internal/web/static/styles.css`; it contains unrelated pre-existing changes.

---

## File structure

- `internal/ui/home.go` owns layout selection, rendering, list sizing, and preview fetch scheduling.
- `internal/ui/home_test.go` owns rendering and layout boundary tests.
- `internal/ui/issue1366_preview_fetch_layout_test.go` owns no-hidden-preview-fetch coverage.

### Task 1: Define preview visibility and render mobile stacked layouts as sessions-only

**Files:**

- Modify: `internal/ui/home.go:1059-1075, 14251-14361`
- Test: `internal/ui/home_test.go:1998-2023`

**Interfaces:**

- Produces: `func (h *Home) hasPreviewPane() bool`, true only where Preview renders.
- Consumes: `h.width`, `h.getLayoutMode()`, `renderSingleColumnLayout(totalHeight int) string`.

- [ ] **Step 1: Write the failing rendering regression test**

In `TestHomeViewStackedLayout`, seed `home.previewCache[inst.ID]` with `"mobile preview must stay hidden"`. Assert a 65-column `home.View()` contains neither that text nor `"PREVIEW"`. Add a 100-column `PreviewOrientationBelow` case that retains `"PREVIEW"`.

```go
if strings.Contains(view, "PREVIEW") || strings.Contains(view, "mobile preview must stay hidden") {
    t.Fatalf("mobile stacked layout rendered Preview:\n%s", view)
}
```

- [ ] **Step 2: Run the focused test to verify it fails**

Run: `go test ./internal/ui -run 'TestHomeViewStackedLayout' -count=1`

Expected: FAIL because 65-column rendering includes the Preview title and cached content.

- [ ] **Step 3: Write the minimal implementation**

Add this method next to `getLayoutMode`:

```go
func (h *Home) hasPreviewPane() bool {
    return h.width >= layoutBreakpointStacked
}
```

At the top of `renderStackedLayout`, use the full-height session renderer for mobile widths:

```go
if !h.hasPreviewPane() {
    return h.renderSingleColumnLayout(totalHeight)
}
```

Keep the existing split renderer for wide `PreviewOrientationBelow` layouts.

- [ ] **Step 4: Run the focused test to verify it passes**

Run: `go test ./internal/ui -run 'TestHomeViewStackedLayout' -count=1`

Expected: PASS; mobile stacked output has Sessions only and wide below-orientation output retains Preview.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/home.go internal/ui/home_test.go
git commit -m "fix(tui): hide preview in mobile stacked layout"
```

### Task 2: Apply visibility to sizing and preview scheduling

**Files:**

- Modify: `internal/ui/home.go:2542-2555, 2785-2793, 3688-3707, 6761-6791`
- Test: `internal/ui/issue1366_preview_fetch_layout_test.go:31-65`

**Interfaces:**

- Consumes: `(*Home).hasPreviewPane() bool` from Task 1.
- Produces: full-height session list and nil preview commands where Preview is hidden.

- [ ] **Step 1: Write failing fetch and sizing regressions**

Extend `TestIssue1366_NoPreviewFetchInSingleColumnLayout` into a table for widths 45 and 65. Each case must assert `h.fetchSelectedPreview() == nil`. In `TestHomeViewStackedLayout`, render enough items at 65 columns to assert the full-height list does not show the `"more below"` marker solely because of a hidden Preview reservation.

```go
for _, width := range []int{45, 65} {
    h.width = width
    if cmd := h.fetchSelectedPreview(); cmd != nil {
        t.Fatalf("width=%d: hidden Preview scheduled a fetch", width)
    }
}
```

- [ ] **Step 2: Run focused tests to verify they fail**

Run: `go test ./internal/ui -run 'TestIssue1366_NoPreviewFetchInSingleColumnLayout|TestHomeViewStackedLayout' -count=1`

Expected: FAIL at width 65 because the previous stacked path schedules preview work and reserves list height.

- [ ] **Step 3: Write the minimal implementation**

Use `h.hasPreviewPane()` in the stacked branches of the visible-height and cursor-window calculations so a hidden Preview uses the full-height list calculation. Change the guard in `fetchSelectedPreview` to:

```go
if !h.hasPreviewPane() {
    return nil
}
```

In the periodic tick refresh, only construct local or remote preview-refresh commands when `h.hasPreviewPane()` is true. Preserve all fetch behavior in dual and wide below-orientation layouts.

- [ ] **Step 4: Run focused tests to verify they pass**

Run: `go test ./internal/ui -run 'TestIssue1366_NoPreviewFetchInSingleColumnLayout|TestIssue1366_PreviewFetchInDualLayout|TestHomeViewStackedLayout' -count=1`

Expected: PASS; widths 45 and 65 schedule no Preview work, while the 120-column dual case still schedules it.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/home.go internal/ui/home_test.go internal/ui/issue1366_preview_fetch_layout_test.go
git commit -m "fix(tui): skip hidden mobile preview work"
```

### Task 3: Verify boundaries and the UI package

**Files:**

- Modify: `internal/ui/home_test.go:995-1016`
- Test: `internal/ui/home_test.go:995-1016`

**Interfaces:**

- Consumes: `getLayoutMode()` and `hasPreviewPane()`.
- Produces: boundary coverage without changing existing layout-mode names.

- [ ] **Step 1: Write preview-visibility boundary assertions**

Extend `TestGetLayoutMode` with `previewVisible`. At widths 49, 50, and 79, expect false; at 80 expect true. Add a 100-column `PreviewOrientationBelow` assertion that the mode is stacked and Preview remains visible.

```go
if got := home.hasPreviewPane(); got != tt.previewVisible {
    t.Errorf("hasPreviewPane() at width %d = %v, want %v", tt.width, got, tt.previewVisible)
}
```

- [ ] **Step 2: Run the boundary test**

Run: `go test ./internal/ui -run '^TestGetLayoutMode$' -count=1`

Expected: PASS after Tasks 1 and 2.

- [ ] **Step 3: Run package verification and inspect scope**

Run: `go test ./internal/ui -count=1 && git diff --check && git diff -- internal/ui/home.go internal/ui/home_test.go internal/ui/issue1366_preview_fetch_layout_test.go`

Expected: PASS and a diff restricted to Preview visibility, list sizing, fetch scheduling, and their tests.

- [ ] **Step 4: Commit**

```bash
git add internal/ui/home_test.go
git commit -m "test(tui): cover mobile preview visibility boundaries"
```

