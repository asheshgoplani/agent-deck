/**
 * PaneOverlay - Large number overlay for move mode
 *
 * Shows a large number (1, 2, 3, 4...) on each pane when move mode is active.
 * User presses a number key to swap sessions between the current pane and the target.
 */

import './PaneOverlay.css';

export default function PaneOverlay({ number, isActive }) {
    return (
        <div className={`pane-overlay ${isActive ? 'active' : ''}`}>
            <div className="pane-overlay-number">{number}</div>
        </div>
    );
}
