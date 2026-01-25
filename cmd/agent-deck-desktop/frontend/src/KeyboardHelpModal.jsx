import { useEffect, useCallback } from 'react';
import './KeyboardHelpModal.css';
import { modKey } from './utils/platform';

export default function KeyboardHelpModal({ onClose }) {
    // Close on Escape or any other key
    const handleKeyDown = useCallback((e) => {
        e.preventDefault();
        e.stopPropagation();
        onClose();
    }, [onClose]);

    useEffect(() => {
        // Use capture phase to intercept before xterm can swallow the event
        document.addEventListener('keydown', handleKeyDown, true);
        return () => document.removeEventListener('keydown', handleKeyDown, true);
    }, [handleKeyDown]);

    return (
        <div className="help-overlay" onClick={onClose}>
            <div className="help-container" onClick={(e) => e.stopPropagation()}>
                <div className="help-header">
                    <h2>Keyboard Shortcuts</h2>
                    <button className="help-close" onClick={onClose}>
                        &times;
                    </button>
                </div>

                <div className="help-content">
                    <div className="help-column">
                        <div className="help-section">
                            <h3>Sessions</h3>
                            <div className="help-row">
                                <kbd>{modKey}N</kbd>
                                <span>New terminal</span>
                            </div>
                            <div className="help-row">
                                <kbd>&uarr;&darr;</kbd>
                                <span>Navigate list</span>
                            </div>
                            <div className="help-row">
                                <kbd>Enter</kbd>
                                <span>Open session</span>
                            </div>
                        </div>

                        <div className="help-section">
                            <h3>Navigation</h3>
                            <div className="help-row">
                                <kbd>{modKey}Esc</kbd>
                                <span>Back to sessions</span>
                            </div>
                            <div className="help-row">
                                <kbd>{modKey},</kbd>
                                <span>Settings</span>
                            </div>
                            <div className="help-row">
                                <kbd>{modKey}F</kbd>
                                <span>Find in terminal</span>
                            </div>
                            <div className="help-row">
                                <kbd>{modKey}+</kbd>
                                <span>Increase font size</span>
                            </div>
                            <div className="help-row">
                                <kbd>{modKey}-</kbd>
                                <span>Decrease font size</span>
                            </div>
                            <div className="help-row">
                                <kbd>{modKey}0</kbd>
                                <span>Reset font size</span>
                            </div>
                        </div>

                        <div className="help-section">
                            <h3>Tabs</h3>
                            <div className="help-row">
                                <kbd>{modKey}T</kbd>
                                <span>New tab</span>
                            </div>
                            <div className="help-row">
                                <kbd>{modKey}1-9</kbd>
                                <span>Switch to tab N</span>
                            </div>
                            <div className="help-row">
                                <kbd>{modKey}[</kbd>
                                <span>Previous tab</span>
                            </div>
                            <div className="help-row">
                                <kbd>{modKey}]</kbd>
                                <span>Next tab</span>
                            </div>
                            <div className="help-row">
                                <kbd>{modKey}W</kbd>
                                <span>Close tab</span>
                            </div>
                        </div>

                        <div className="help-section">
                            <h3>Panes</h3>
                            <div className="help-row">
                                <kbd>{modKey}D</kbd>
                                <span>Split right</span>
                            </div>
                            <div className="help-row">
                                <kbd>{modKey}⇧D</kbd>
                                <span>Split down</span>
                            </div>
                            <div className="help-row">
                                <kbd>{modKey}⌥&larr;&rarr;&uarr;&darr;</kbd>
                                <span>Navigate panes</span>
                            </div>
                            <div className="help-row">
                                <kbd>{modKey}⇧W</kbd>
                                <span>Close pane</span>
                            </div>
                            <div className="help-row">
                                <kbd>{modKey}⇧Z</kbd>
                                <span>Zoom pane</span>
                            </div>
                            <div className="help-row">
                                <kbd>{modKey}⌥=</kbd>
                                <span>Balance sizes</span>
                            </div>
                            <div className="help-row">
                                <kbd>{modKey}K</kbd>
                                <span>Move session (via palette)</span>
                            </div>
                        </div>

                        <div className="help-section">
                            <h3>Quick Launch</h3>
                            <div className="help-row">
                                <span className="help-action">Click</span>
                                <span>Launch project</span>
                            </div>
                            <div className="help-row">
                                <span className="help-action">{modKey}+Click</span>
                                <span>Tool picker</span>
                            </div>
                            <div className="help-row">
                                <span className="help-action">Custom</span>
                                <span>User shortcuts</span>
                            </div>
                        </div>
                    </div>

                    <div className="help-column">
                        <div className="help-section">
                            <h3>Command Palette</h3>
                            <div className="help-row">
                                <kbd>{modKey}K</kbd>
                                <span>Open palette</span>
                            </div>
                            <div className="help-row help-subtext">
                                <span className="help-note">While palette is open:</span>
                            </div>
                            <div className="help-row">
                                <kbd>&uarr;&darr;</kbd>
                                <span>Navigate items</span>
                            </div>
                            <div className="help-row">
                                <kbd>Enter</kbd>
                                <span>Select item</span>
                            </div>
                            <div className="help-row">
                                <kbd>{modKey}Enter</kbd>
                                <span>Open with tool picker</span>
                            </div>
                            <div className="help-row">
                                <kbd>{modKey}P</kbd>
                                <span>Pin to Quick Launch</span>
                            </div>
                            <div className="help-row">
                                <kbd>Esc</kbd>
                                <span>Close palette</span>
                            </div>
                        </div>

                        <div className="help-section">
                            <h3>Layout Presets</h3>
                            <div className="help-row">
                                <kbd>{modKey}⌥1</kbd>
                                <span>Single pane</span>
                            </div>
                            <div className="help-row">
                                <kbd>{modKey}⌥2</kbd>
                                <span>2 columns</span>
                            </div>
                            <div className="help-row">
                                <kbd>{modKey}⌥3</kbd>
                                <span>2 rows</span>
                            </div>
                            <div className="help-row">
                                <kbd>{modKey}⌥4</kbd>
                                <span>2x2 grid</span>
                            </div>
                        </div>

                        <div className="help-section">
                            <h3>General</h3>
                            <div className="help-row">
                                <kbd>⇧5</kbd>
                                <span>Cycle filter</span>
                            </div>
                            <div className="help-row">
                                <span className="help-keys">
                                    <kbd>?</kbd>
                                    <kbd>{modKey}/</kbd>
                                </span>
                                <span>This help</span>
                            </div>
                            <div className="help-row">
                                <kbd>Esc</kbd>
                                <span>Close dialog</span>
                            </div>
                        </div>
                    </div>
                </div>

                <div className="help-footer">
                    Press Esc or any key to close
                </div>
            </div>
        </div>
    );
}
