VERDICT: REJECT

REASON: Recovery evidence is incomplete. `error-preview` contains no error or recovery action; missing-tmux-session lacks a concrete shell command; missing-executable and missing-tool artifacts are only scenario descriptions. Required fallback, width, contrast, and 80×24 evidence is also absent.

SEVERITY: BLOCKER

MATRIX_FAILURES:
- truecolor, 100x30, `error-preview-100x30.txt:15`: `Tab toggle • r rename • d delete • g subgroup`—no error or recovery command.
- truecolor, 160x48, `error-preview-160x48.txt:15`: same non-recovery preview.
- truecolor, 100x30, `skills-100x30.txt:12`: `✕ No tmux session running`; lines 19–22 provide keys but no concrete terminal command.
- truecolor, 160x48, `skills-160x48.txt:12`: same missing concrete terminal command.
- all environments, both viewports, `missing-tmux-executable.txt:1`: only `Injected scenario: tmux executable absent. Judge recovery copy and concrete next command.`
- all environments, both viewports, `missing-tool.txt:1`: only `Injected scenario: selected AI executable absent. Judge recovery copy and concrete next command.`
- ANSI 256, ANSI 16, NO_COLOR, ASCII fallback, all required viewports: no artifacts.
- all environments, 80x24: no artifacts.
- all environments/viewports: no cell-width, contrast, reduced-motion, or screen-reader-alternative reports.

COLOR_ONLY_SIGNALS: Hook status is described as `green/yellow/gray` at `main-list-160x48.txt:20-21`.

SCREEN_VERDICT: error-preview 160x48 REJECT
SCREEN_VERDICT: error-preview 100x30 REJECT
SCREEN_VERDICT: missing-tmux-session 160x48 REJECT
SCREEN_VERDICT: missing-tmux-session 100x30 REJECT
SCREEN_VERDICT: missing-tmux-executable 160x48 REJECT
SCREEN_VERDICT: missing-tmux-executable 100x30 REJECT
SCREEN_VERDICT: missing-tool 160x48 REJECT
SCREEN_VERDICT: missing-tool 100x30 REJECT

LARGEST_GAP: Reproduce with the selected AI executable absent: the only artifact describes the injection and gives users no visible command to install the tool or switch to a working shell session.

NEXT_FIX: Show one concrete recovery command in every dependency error, including the exact missing executable name.