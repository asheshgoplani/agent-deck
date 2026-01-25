/**
 * useMacKeyBindings.js
 *
 * Factory functions for handling macOS-specific keyboard navigation shortcuts in the terminal.
 * Implements standard macOS text navigation patterns:
 * - Option+Left/Right: Jump by word
 * - Cmd+Left/Right: Jump to line start/end
 *
 * These shortcuts work by sending standard readline/bash escape sequences to the terminal.
 */

import { isMac } from '../utils/platform';

/**
 * Creates a macOS keyboard navigation handler for terminal input.
 *
 * @param {Function} writeToTerminal - Function to write data to the terminal (sessionId, data)
 * @param {string} sessionId - The terminal session ID
 * @returns {Function} Key event handler that returns true to allow default handling, false to prevent
 */
export function createMacKeyBindingHandler(writeToTerminal, sessionId) {
    /**
     * Handle keyboard events for macOS navigation shortcuts.
     *
     * @param {KeyboardEvent} e - The keyboard event
     * @returns {boolean} - True to allow xterm to handle, false to prevent default
     */
    return (e) => {
        // Only handle on macOS
        if (!isMac) {
            return true; // Let xterm handle it
        }

        // Only handle keydown events
        if (e.type !== 'keydown') {
            return true;
        }

        const { key, altKey, metaKey, ctrlKey, shiftKey } = e;

        // Option+Left: Jump word backward (ESC + b)
        if (altKey && !metaKey && !ctrlKey && key === 'ArrowLeft') {
            e.preventDefault();
            writeToTerminal(sessionId, '\x1bb'); // ESC + b
            return false;
        }

        // Option+Right: Jump word forward (ESC + f)
        if (altKey && !metaKey && !ctrlKey && key === 'ArrowRight') {
            e.preventDefault();
            writeToTerminal(sessionId, '\x1bf'); // ESC + f
            return false;
        }

        // Cmd+Left: Jump to line start (Ctrl+A)
        if (metaKey && !altKey && !ctrlKey && key === 'ArrowLeft') {
            e.preventDefault();
            writeToTerminal(sessionId, '\x01'); // Ctrl+A
            return false;
        }

        // Cmd+Right: Jump to line end (Ctrl+E)
        if (metaKey && !altKey && !ctrlKey && key === 'ArrowRight') {
            e.preventDefault();
            writeToTerminal(sessionId, '\x05'); // Ctrl+E
            return false;
        }

        // Not a Mac navigation shortcut, let xterm handle it
        return true;
    };
}

/**
 * Standalone utility to check if a keyboard event is a Mac navigation shortcut.
 * Useful for detecting these shortcuts in other contexts (e.g., command palette).
 *
 * @param {KeyboardEvent} e - The keyboard event
 * @returns {boolean} - True if this is a Mac navigation shortcut
 */
export function isMacNavigationShortcut(e) {
    if (!isMac || e.type !== 'keydown') {
        return false;
    }

    const { key, altKey, metaKey, ctrlKey } = e;

    // Option+Left/Right or Cmd+Left/Right
    if ((key === 'ArrowLeft' || key === 'ArrowRight')) {
        if (altKey && !metaKey && !ctrlKey) return true; // Option+Arrow
        if (metaKey && !altKey && !ctrlKey) return true; // Cmd+Arrow
    }

    return false;
}
