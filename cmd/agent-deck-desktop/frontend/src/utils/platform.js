/**
 * Platform detection and keyboard modifier utilities.
 *
 * On macOS, the convention is:
 * - Cmd+key = app-level shortcuts (close tab, open palette, etc.)
 * - Ctrl+key = passed through to terminal applications (nano, bash, etc.)
 *
 * On Windows/Linux, Ctrl is used for both, so some conflicts are expected.
 */

// Detect if running on macOS
export const isMac = navigator.platform.toUpperCase().indexOf('MAC') >= 0;

// Display character for the modifier key
export const modKey = isMac ? '⌘' : 'Ctrl+';

/**
 * Check if the event has the app-level modifier key pressed.
 * - macOS: metaKey (Cmd)
 * - Windows/Linux: ctrlKey
 *
 * @param {KeyboardEvent} e
 * @returns {boolean}
 */
export function hasAppModifier(e) {
    return isMac ? e.metaKey : e.ctrlKey;
}

/**
 * Check if the event has a modifier that should pass through to the terminal.
 * This is specifically Ctrl on macOS, which terminal apps use extensively.
 *
 * @param {KeyboardEvent} e
 * @returns {boolean}
 */
export function hasTerminalModifier(e) {
    return isMac && e.ctrlKey && !e.metaKey;
}

/**
 * Check if an app shortcut should be intercepted, considering terminal passthrough.
 *
 * In terminal view on macOS, we only intercept Cmd+key, not Ctrl+key.
 * In selector view (no terminal), we can intercept both.
 * On Windows/Linux, we always use Ctrl.
 *
 * @param {KeyboardEvent} e - The keyboard event
 * @param {boolean} inTerminalView - Whether currently in terminal view
 * @returns {boolean} - True if this should trigger an app shortcut
 */
export function shouldInterceptShortcut(e, inTerminalView) {
    if (isMac) {
        // On Mac in terminal view: only Cmd, not Ctrl (Ctrl passes through to terminal)
        if (inTerminalView) {
            return e.metaKey && !e.ctrlKey;
        }
        // In selector view: both work (no terminal to conflict with)
        return e.metaKey || e.ctrlKey;
    }
    // Windows/Linux: always use Ctrl
    return e.ctrlKey;
}
