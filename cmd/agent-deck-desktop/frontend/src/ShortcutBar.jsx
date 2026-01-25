import './ShortcutBar.css';
import { modKey } from './utils/platform';

export default function ShortcutBar({
    view,
    onNewTerminal,
    onOpenPalette,
    onCycleFilter,
    onOpenHelp,
    onBackToSessions,
    onOpenSearch,
    onSplitPane,
    hasPanes,
}) {
    if (view === 'selector') {
        return (
            <div className="shortcut-bar">
                <button className="shortcut-item" onClick={onNewTerminal}>
                    <kbd>{modKey}N</kbd>
                    <span>New</span>
                </button>
                <button className="shortcut-item" onClick={onOpenPalette}>
                    <kbd>{modKey}K</kbd>
                    <span>Palette</span>
                </button>
                <button className="shortcut-item" onClick={onCycleFilter}>
                    <kbd>⇧5</kbd>
                    <span>Filter</span>
                </button>
                <button className="shortcut-item" onClick={onOpenHelp}>
                    <kbd>?</kbd>
                    <span>Help</span>
                </button>
            </div>
        );
    }

    // Terminal view - use Cmd+/ for help since ? is used by Claude
    return (
        <div className="shortcut-bar">
            <button className="shortcut-item" onClick={onBackToSessions}>
                <kbd>{modKey}Esc</kbd>
                <span>Sessions</span>
            </button>
            <button className="shortcut-item" onClick={onSplitPane}>
                <kbd>{modKey}D</kbd>
                <span>{hasPanes ? 'Split' : 'Split'}</span>
            </button>
            <button className="shortcut-item" onClick={onOpenSearch}>
                <kbd>{modKey}F</kbd>
                <span>Find</span>
            </button>
            <button className="shortcut-item" onClick={onOpenPalette}>
                <kbd>{modKey}K</kbd>
                <span>Palette</span>
            </button>
            <button className="shortcut-item" onClick={onOpenHelp}>
                <kbd>{modKey}/</kbd>
                <span>Help</span>
            </button>
        </div>
    );
}
