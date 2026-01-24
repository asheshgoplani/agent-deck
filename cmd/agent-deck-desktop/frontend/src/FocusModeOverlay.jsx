/**
 * FocusModeOverlay - Floating exit button shown when a pane is zoomed
 */
export default function FocusModeOverlay({ onExit }) {
    return (
        <div className="focus-mode-overlay">
            <button className="focus-mode-exit-btn" onClick={onExit}>
                Exit Focus
                <kbd>ESC</kbd>
            </button>
        </div>
    );
}
