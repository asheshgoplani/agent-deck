import { useCallback } from 'react';
import './SessionTab.css';
import ToolIcon from './ToolIcon';
import { useTooltip } from './Tooltip';
import { createLogger } from './logger';

const logger = createLogger('SessionTab');

// Status color mapping
function getStatusColor(status) {
    switch (status) {
        case 'running': return '#4ecdc4';
        case 'waiting': return '#ffe66d';
        case 'idle': return '#6c757d';
        case 'error': return '#ff6b6b';
        default: return '#6c757d';
    }
}

// Get relative project path (simplified)
function getRelativePath(fullPath) {
    if (!fullPath) return '';
    const parts = fullPath.split('/').filter(Boolean);
    if (parts.length >= 2) {
        return `.../${parts.slice(-2).join('/')}`;
    }
    return fullPath;
}

export default function SessionTab({ tab, index, isActive, onSwitch, onClose, onContextMenu }) {
    const { show: showTooltip, hide: hideTooltip, Tooltip } = useTooltip();
    const session = tab.session;

    // Build rich tooltip content
    const getTooltipContent = useCallback(() => {
        const shortcutNum = index < 9 ? index + 1 : null;
        const relativePath = getRelativePath(session.projectPath);

        return (
            <div className="session-tooltip">
                {/* Title and custom label */}
                <div className="tooltip-row tooltip-title">
                    <span className="tooltip-icon">
                        <ToolIcon tool={session.tool} size={12} />
                    </span>
                    <span>
                        {session.title}
                        {session.customLabel && (
                            <span className="tab-tooltip-label"> ({session.customLabel})</span>
                        )}
                    </span>
                </div>

                {/* Project path */}
                {relativePath && (
                    <div className="tooltip-row tooltip-path">
                        <span className="tooltip-icon">📁</span>
                        <span>{relativePath}</span>
                    </div>
                )}

                {/* Status */}
                <div className="tooltip-row tooltip-status">
                    <span
                        className="tooltip-icon tab-status-dot"
                        style={{ color: getStatusColor(session.status) }}
                    >●</span>
                    <span>{session.status || 'unknown'}</span>
                </div>

                {/* Git branch */}
                {session.gitBranch && (
                    <div className="tooltip-row tooltip-git">
                        <span className="tooltip-icon">{session.isWorktree ? '🌿' : '⎇'}</span>
                        <span>
                            {session.gitBranch}
                            {session.isWorktree && ' (worktree)'}
                        </span>
                    </div>
                )}

                {/* Keyboard shortcut hint */}
                {shortcutNum && (
                    <div className="tooltip-row tooltip-shortcut-hint">
                        <span className="tooltip-icon">⌘</span>
                        <span>Press ⌘{shortcutNum} to switch</span>
                    </div>
                )}
            </div>
        );
    }, [session, index]);

    const handleClose = (e) => {
        e.stopPropagation();
        logger.info('Closing tab', { tabId: tab.id, sessionTitle: session.title });
        onClose?.();
    };

    return (
        <>
            <button
                className={`session-tab${isActive ? ' active' : ''}`}
                onClick={onSwitch}
                onContextMenu={onContextMenu}
                onMouseEnter={(e) => showTooltip(e, getTooltipContent())}
                onMouseLeave={hideTooltip}
            >
                <span
                    className="tab-status"
                    style={{ backgroundColor: getStatusColor(session.status) }}
                >
                    <ToolIcon tool={session.tool} size={10} />
                </span>
                <span className="tab-title">
                    {session.customLabel || session.title}
                </span>
                <button
                    className="tab-close"
                    onClick={handleClose}
                    title="Close tab"
                >
                    ×
                </button>
            </button>
            <Tooltip />
        </>
    );
}
