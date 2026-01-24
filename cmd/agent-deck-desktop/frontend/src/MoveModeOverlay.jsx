/**
 * MoveModeOverlay - Floating overlay shown during move mode
 *
 * Shows instructions for swapping sessions between panes.
 */

import './MoveModeOverlay.css';

export default function MoveModeOverlay({ onExit }) {
    return (
        <div className="move-mode-overlay">
            <div className="move-mode-content">
                <span className="move-mode-icon">↔</span>
                <span className="move-mode-text">
                    Press <strong>1-9</strong> to swap sessions with that pane
                </span>
                <button className="move-mode-cancel" onClick={onExit}>
                    Cancel (Esc)
                </button>
            </div>
        </div>
    );
}
