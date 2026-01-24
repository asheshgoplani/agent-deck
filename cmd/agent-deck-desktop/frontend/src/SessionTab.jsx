import { useCallback, useMemo } from 'react';
import './SessionTab.css';
import ToolIcon from './ToolIcon';
import { useTooltip } from './Tooltip';
import { createLogger } from './logger';
import { getPaneList, findPane, countPanes } from './layoutUtils';

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

    // Extract session info from the new layout-based tab structure
    // Tab structure: { id, name, layout, activePaneId, openedAt, zoomedPaneId }
    const { primarySession, paneCount, sessions } = useMemo(() => {
        if (!tab.layout) {
            // Fallback for old tab structure (shouldn't happen but be safe)
            return { primarySession: tab.session, paneCount: 1, sessions: [tab.session].filter(Boolean) };
        }

        const panes = getPaneList(tab.layout);
        const panesWithSessions = panes.filter(p => p.session);
        const count = panes.length;

        // Get the active pane's session, or the first session if no active
        const activePane = findPane(tab.layout, tab.activePaneId);
        const primary = activePane?.session || panesWithSessions[0]?.session || null;

        return {
            primarySession: primary,
            paneCount: count,
            sessions: panesWithSessions.map(p => p.session),
        };
    }, [tab]);

    const session = primarySession;

    // Build rich tooltip content
    const getTooltipContent = useCallback(() => {
        const shortcutNum = index < 9 ? index + 1 : null;

        // Handle tabs with no sessions (empty panes only)
        if (!session) {
            return (
                <div className="session-tooltip">
                    <div className="tooltip-row tooltip-title">
                        <span className="tooltip-icon">+</span>
                        <span>Empty tab ({paneCount} pane{paneCount > 1 ? 's' : ''})</span>
                    </div>
                    {shortcutNum && (
                        <div className="tooltip-row tooltip-shortcut-hint">
                            <span className="tooltip-icon">⌘</span>
                            <span>Press ⌘{shortcutNum} to switch</span>
                        </div>
                    )}
                </div>
            );
        }

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

                {/* Multi-pane indicator */}
                {paneCount > 1 && (
                    <div className="tooltip-row tooltip-panes">
                        <span className="tooltip-icon">#</span>
                        <span>{paneCount} panes ({sessions.length} with sessions)</span>
                    </div>
                )}

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
    }, [session, sessions, paneCount, index]);

    const handleClose = (e) => {
        e.stopPropagation();
        logger.info('Closing tab', { tabId: tab.id, sessionTitle: session?.title || 'empty' });
        onClose?.();
    };

    // Calculate tab title: show "Name + N more" if multiple sessions
    const tabTitle = useMemo(() => {
        if (!session) {
            return paneCount > 1 ? `${paneCount} panes` : 'Empty';
        }
        const displayName = session.customLabel || session.title;
        if (sessions.length > 1) {
            return `${displayName} +${sessions.length - 1}`;
        }
        return displayName;
    }, [session, sessions.length, paneCount]);

    return (
        <>
            <button
                className={`session-tab${isActive ? ' active' : ''}${paneCount > 1 ? ' multi-pane' : ''}`}
                onClick={onSwitch}
                onContextMenu={onContextMenu}
                onMouseEnter={(e) => showTooltip(e, getTooltipContent())}
                onMouseLeave={hideTooltip}
            >
                <span
                    className="tab-status"
                    style={{ backgroundColor: session ? getStatusColor(session.status) : '#6c757d' }}
                >
                    {session ? (
                        <ToolIcon tool={session.tool} size={10} />
                    ) : (
                        <span style={{ fontSize: '10px' }}>+</span>
                    )}
                </span>
                <span className="tab-title">
                    {tabTitle}
                </span>
                {paneCount > 1 && (
                    <span className="tab-pane-badge">{paneCount}</span>
                )}
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
