import { useState, useEffect, useRef, useCallback } from 'react';
import './ShortcutEditor.css';
import { createLogger } from './logger';
import { formatShortcut } from './utils/shortcuts';

const logger = createLogger('ShortcutEditor');

// Reserved shortcuts that can't be overridden
const RESERVED_SHORTCUTS = [
    'cmd+k',      // Command menu
    'cmd+f',      // Search
    'cmd+w',      // Close
    'cmd+,',       // Settings
    'cmd+escape',  // Back to selector
    'cmd+q',      // Quit app
    'cmd+c',      // Copy
    'cmd+v',      // Paste
    'cmd+x',      // Cut
    'cmd+a',      // Select all
    'cmd+z',      // Undo
    'cmd+shift+z', // Redo
];

export default function ShortcutEditor({
    projectName,
    projectPath,
    currentShortcut,
    existingShortcuts, // Map of shortcut -> project name (for conflict detection)
    onSave,
    onCancel,
}) {
    const [shortcut, setShortcut] = useState(currentShortcut || '');
    const [recording, setRecording] = useState(false);
    const [error, setError] = useState(null);
    const inputRef = useRef(null);

    useEffect(() => {
        logger.info('Shortcut editor opened', { projectName, currentShortcut });
    }, [projectName, currentShortcut]);

    const buildShortcutKey = useCallback((e) => {
        const parts = [];
        if (e.metaKey) parts.push('cmd');
        if (e.ctrlKey) parts.push('ctrl');
        if (e.shiftKey) parts.push('shift');
        if (e.altKey) parts.push('alt');
        if (e.key && e.key.length === 1) {
            parts.push(e.key.toLowerCase());
        }
        return parts.join('+');
    }, []);

    const handleKeyDown = useCallback((e) => {
        if (!recording) return;

        e.preventDefault();
        e.stopPropagation();

        // Escape cancels recording
        if (e.key === 'Escape') {
            setRecording(false);
            return;
        }

        // Need at least one modifier + letter
        if (!e.metaKey && !e.ctrlKey && !e.altKey) {
            setError('Shortcut needs Cmd, Ctrl, or Alt modifier');
            return;
        }

        if (!e.key || e.key.length !== 1) {
            // Modifier only, keep waiting
            return;
        }

        const newShortcut = buildShortcutKey(e);

        // Check if reserved
        if (RESERVED_SHORTCUTS.includes(newShortcut)) {
            setError(`${newShortcut} is reserved by the system`);
            return;
        }

        // Check for conflicts with other favorites
        if (existingShortcuts[newShortcut] && existingShortcuts[newShortcut] !== projectPath) {
            setError(`${newShortcut} is already used by "${existingShortcuts[newShortcut]}"`);
            return;
        }

        logger.info('Shortcut recorded', { shortcut: newShortcut });
        setShortcut(newShortcut);
        setRecording(false);
        setError(null);
    }, [recording, buildShortcutKey, existingShortcuts, projectPath]);

    useEffect(() => {
        if (recording) {
            document.addEventListener('keydown', handleKeyDown);
            return () => document.removeEventListener('keydown', handleKeyDown);
        }
    }, [recording, handleKeyDown]);

    const handleSave = () => {
        logger.info('Saving shortcut', { shortcut, projectPath });
        onSave(shortcut);
    };

    const handleClear = () => {
        setShortcut('');
        setError(null);
    };

    return (
        <div className="shortcut-editor-overlay" onClick={onCancel}>
            <div
                className="shortcut-editor-container"
                onClick={(e) => e.stopPropagation()}
            >
                <h3>Set Shortcut for "{projectName}"</h3>
                <p className="shortcut-editor-path">{projectPath}</p>

                <div className="shortcut-editor-input-area">
                    {recording ? (
                        <div className="shortcut-editor-recording">
                            Press your shortcut...
                            <span className="shortcut-editor-hint">
                                (Cmd/Ctrl/Alt + letter)
                            </span>
                        </div>
                    ) : (
                        <div className="shortcut-editor-display">
                            {shortcut ? (
                                <span className="shortcut-editor-shortcut">
                                    {formatShortcut(shortcut)}
                                </span>
                            ) : (
                                <span className="shortcut-editor-none">
                                    No shortcut set
                                </span>
                            )}
                        </div>
                    )}
                </div>

                {error && (
                    <div className="shortcut-editor-error">{error}</div>
                )}

                <div className="shortcut-editor-buttons">
                    <button
                        className="shortcut-editor-btn secondary"
                        onClick={() => setRecording(true)}
                        disabled={recording}
                    >
                        {recording ? 'Recording...' : 'Record New'}
                    </button>
                    <button
                        className="shortcut-editor-btn secondary"
                        onClick={handleClear}
                        disabled={recording || !shortcut}
                    >
                        Clear
                    </button>
                </div>

                <div className="shortcut-editor-actions">
                    <button
                        className="shortcut-editor-btn cancel"
                        onClick={onCancel}
                    >
                        Cancel
                    </button>
                    <button
                        className="shortcut-editor-btn primary"
                        onClick={handleSave}
                        disabled={recording}
                    >
                        Save
                    </button>
                </div>
            </div>
        </div>
    );
}
