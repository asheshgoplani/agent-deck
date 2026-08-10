# Preview width-only visibility design

## Motivation

The Preview pane should be visible only when the terminal is wide enough to
place it beside the Sessions pane. A Preview pane below Sessions consumes
vertical space and makes the interface read as one long column.

## Decisions

- Terminals narrower than 80 columns render Sessions only, at full height.
- Terminals 80 columns or wider render Sessions and Preview side by side.
- Remove the user-selectable `below` preview orientation and its `O` hotkey.
- Existing `preview_orientation` configuration is obsolete and must not make
  startup fail.
- Keep the configurable preview percentage for the side-by-side split.

## Architecture and interfaces

Layout selection depends only on terminal width. The existing layout modes may
remain internal implementation details, but no mode may render Preview beneath
Sessions. Preview visibility and preview-fetch scheduling continue to share the
same width-derived rule.

Remove orientation-specific UI state, persistence helpers, constants, help
text, and rendering branches. Configuration parsing remains tolerant of old
`preview_orientation` keys so existing installations continue to start.

## Verification

Focused tests will cover both sides of the 80-column boundary, prove that
narrow layouts neither render nor fetch Preview, prove that wide layouts render
Preview side by side, and ensure the removed orientation hotkey is absent.

## Out of scope

- Changing the 80-column breakpoint.
- Changing Preview content or session-list behavior.
- Changing the preview-width percentage controls.
- Adding configuration migration machinery.
