VERDICT: REJECT

REASON: Recovery succeeds only for the visible missing-tmux-session state. The selected group in `error-preview` lacks a clear selection marker, its recovery action is not primary, 80x24 is absent, and executable-missing states have no UI evidence. Symbol-only filters (`!@#&`, `%`, `^`) require decoding.

SEVERITY: BLOCKER

VIEWPORT_RESULTS: 80x24 — Missing evidence; first three attractions, selected object, state, actions, telemetry, and chrome cannot be verified.  
VIEWPORT_RESULTS: 100x30 — First attention: global `✕ 3 error`, status/filter strip, `platform (2)`; product state is three errors, selected object is ambiguous, primary recovery action absent, secondary group actions dominate, telemetry is CPU/disk; fails first scan path.  
VIEWPORT_RESULTS: 160x48 — First attention: global `✕ 3 error`, status/filter strip, `platform (2)`; extra width produces whitespace rather than stronger recovery hierarchy, while selection and primary recovery remain unclear.

CHROME_ROW_SHARE: 80x24 unavailable; 100x30 13.3% (4/30 rows before first data row); 160x48 8.3% (4/48).

DUPLICATED_SIGNALS: `✕ 3` appears in both title telemetry and status strip; `platform (2)` is repeated as `2 sessions`; session names appear in both tree and preview; rename/delete controls repeat in preview and footer.

LARGEST_GAP: In `error-preview` at both captured sizes, the scan path reaches global error totals and group counts before any unambiguous selected object, concrete failure, or recovery action.

NEXT_FIX: Replace the group preview summary with a selected-error recovery panel whose first line reads `api-review — tmux session missing — R Restart`.

SCREEN_VERDICT: error-preview 160x48 REJECT  
SCREEN_VERDICT: error-preview 100x30 REJECT  
SCREEN_VERDICT: missing-tmux-session 160x48 PASS  
SCREEN_VERDICT: missing-tmux-session 100x30 PASS  
SCREEN_VERDICT: missing-tmux-executable 160x48 REJECT  
SCREEN_VERDICT: missing-tmux-executable 100x30 REJECT  
SCREEN_VERDICT: missing-tool 160x48 REJECT  
SCREEN_VERDICT: missing-tool 100x30 REJECT