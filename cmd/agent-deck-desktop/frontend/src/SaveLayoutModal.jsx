/**
 * SaveLayoutModal - Modal for saving the current layout as a template
 *
 * Allows users to name their layout and optionally assign a keyboard shortcut.
 */

import { useState, useRef, useEffect } from 'react';
import './SaveLayoutModal.css';
import { createLogger } from './logger';

const logger = createLogger('SaveLayoutModal');

export default function SaveLayoutModal({
    onSave,   // (name, shortcut) => void
    onClose,
}) {
    const [name, setName] = useState('');
    const [shortcut, setShortcut] = useState('');
    const [error, setError] = useState('');
    const inputRef = useRef(null);

    // Focus input on mount
    useEffect(() => {
        inputRef.current?.focus();
    }, []);

    const handleSubmit = (e) => {
        e.preventDefault();

        const trimmedName = name.trim();
        if (!trimmedName) {
            setError('Please enter a layout name');
            return;
        }

        logger.info('Saving layout', { name: trimmedName, shortcut: shortcut.trim() || null });
        onSave(trimmedName, shortcut.trim() || null);
    };

    const handleKeyDown = (e) => {
        if (e.key === 'Escape') {
            e.preventDefault();
            onClose();
        }
    };

    return (
        <div className="modal-overlay" onClick={onClose}>
            <div className="save-layout-modal" onClick={(e) => e.stopPropagation()}>
                <div className="modal-header">
                    <h3>Save Layout</h3>
                    <button className="modal-close" onClick={onClose}>×</button>
                </div>

                <form onSubmit={handleSubmit}>
                    <div className="form-group">
                        <label htmlFor="layout-name">Layout Name</label>
                        <input
                            ref={inputRef}
                            id="layout-name"
                            type="text"
                            value={name}
                            onChange={(e) => {
                                setName(e.target.value);
                                setError('');
                            }}
                            onKeyDown={handleKeyDown}
                            placeholder="e.g., Dev + Logs"
                            autoComplete="off"
                        />
                        {error && <div className="form-error">{error}</div>}
                    </div>

                    <div className="form-group">
                        <label htmlFor="layout-shortcut">
                            Keyboard Shortcut <span className="optional">(optional)</span>
                        </label>
                        <input
                            id="layout-shortcut"
                            type="text"
                            value={shortcut}
                            onChange={(e) => setShortcut(e.target.value)}
                            onKeyDown={handleKeyDown}
                            placeholder="e.g., cmd+shift+5"
                            autoComplete="off"
                        />
                        <div className="form-hint">
                            Use format: cmd+shift+5, ctrl+alt+l, etc.
                        </div>
                    </div>

                    <div className="modal-actions">
                        <button type="button" className="btn-secondary" onClick={onClose}>
                            Cancel
                        </button>
                        <button type="submit" className="btn-primary">
                            Save Layout
                        </button>
                    </div>
                </form>
            </div>
        </div>
    );
}
