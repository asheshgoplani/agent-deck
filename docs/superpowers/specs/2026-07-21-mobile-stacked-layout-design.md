# Mobile stacked layout design

## Goal

Keep the terminal UI focused on session navigation at mobile-sized terminal
widths by removing the Preview pane from the stacked layout.

## Scope

The terminal UI has three width-derived layouts:

- **single**: fewer than 50 columns; sessions only.
- **stacked**: 50 through 79 columns; sessions above Preview today.
- **dual**: 80 or more columns; Sessions and Preview side by side.

Change stacked to sessions-only. The Preview pane must not render and no
preview capture/fetch work must be scheduled in this mode. The single and dual
layouts retain their existing behavior.

## Design

The stacked layout remains a distinct layout mode for its existing input,
scrolling, and sizing behavior. Its renderer will use the full available
content height for Sessions, matching the single-column visual hierarchy.

Preview visibility becomes the explicit rule: Preview is available only in the
dual layout. All code paths that conditionally fetch or refresh preview content
will use that rule so invisible preview work cannot run at 50–79 columns.

## Acceptance criteria

1. Given a terminal 50–79 columns wide, when the home screen renders, then it
   contains the Sessions view without a Preview pane.
2. Given the same width range, when selection changes or the periodic refresh
   runs, then no preview fetch is scheduled.
3. Given a terminal at least 80 columns wide, when the home screen renders,
   then the dual Sessions-and-Preview layout and preview fetching remain
   available.
4. Given a terminal narrower than 50 columns, the existing sessions-only
   behavior remains unchanged.

## Verification

Extend the focused Go UI tests for layout selection, rendering, and
preview-fetch scheduling. Run the affected `internal/ui` test package after
the change.
