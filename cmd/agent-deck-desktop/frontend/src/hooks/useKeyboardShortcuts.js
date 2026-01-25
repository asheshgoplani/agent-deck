/**
 * useKeyboardShortcuts.js
 *
 * Centralized keyboard shortcut handling for the Agent Deck application.
 * Coordinates different keyboard shortcut handlers including platform-specific
 * navigation shortcuts.
 */

import { createMacKeyBindingHandler } from './useMacKeyBindings';

/**
 * Creates unified keyboard shortcut handlers.
 * This can be extended to include more shortcut handlers as needed.
 *
 * @param {Function} writeToTerminal - Function to write data to terminal
 * @param {string} sessionId - Terminal session ID
 * @returns {Object} Object containing keyboard event handlers
 */
export function createKeyboardShortcutHandlers(writeToTerminal, sessionId) {
    const handleMacKeyBinding = createMacKeyBindingHandler(writeToTerminal, sessionId);

    return {
        handleMacKeyBinding,
    };
}
