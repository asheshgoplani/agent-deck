import { useState, useEffect, useCallback, useMemo } from 'react';
import { ListSessionsWithGroups, GetProjectRoots, UpdateSessionCustomLabel, GetExpandedGroups, ToggleGroupExpanded } from '../wailsjs/go/main/App';
import './SessionSelector.css';
import { createLogger } from './logger';
import ToolIcon from './ToolIcon';
import { useTooltip } from './Tooltip';
import ShortcutBar from './ShortcutBar';
import RenameDialog from './RenameDialog';
import GroupHeader from './GroupHeader';

const logger = createLogger('SessionSelector');

// Format a relative time string (e.g., "5 min ago", "2 hours ago")
function formatRelativeTime(dateString) {
    if (!dateString) return null;

    const date = new Date(dateString);
    if (isNaN(date.getTime())) return null;

    const now = new Date();
    const diffMs = now - date;
    const diffSec = Math.floor(diffMs / 1000);
    const diffMin = Math.floor(diffSec / 60);
    const diffHour = Math.floor(diffMin / 60);
    const diffDay = Math.floor(diffHour / 24);

    if (diffSec < 60) return 'just now';
    if (diffMin < 60) return `${diffMin} min ago`;
    if (diffHour < 24) return `${diffHour} hour${diffHour > 1 ? 's' : ''} ago`;
    if (diffDay < 7) return `${diffDay} day${diffDay > 1 ? 's' : ''} ago`;
    if (diffDay < 30) return `${Math.floor(diffDay / 7)} week${Math.floor(diffDay / 7) > 1 ? 's' : ''} ago`;
    return date.toLocaleDateString();
}

// Compute relative project path based on configured roots
function getRelativeProjectPath(fullPath, projectRoots) {
    if (!fullPath || !projectRoots || projectRoots.length === 0) {
        // Fallback: show last 2 path components
        const parts = fullPath?.split('/').filter(Boolean) || [];
        if (parts.length >= 2) {
            return `.../${parts.slice(-2).join('/')}`;
        }
        return fullPath || '';
    }

    // Find which root contains this path
    for (const root of projectRoots) {
        if (fullPath.startsWith(root)) {
            const rootName = root.split('/').filter(Boolean).pop();
            const relativePath = fullPath.slice(root.length).replace(/^\//, '');
            if (relativePath) {
                return `${rootName}/${relativePath}`;
            }
            return rootName;
        }
    }

    // No matching root, show abbreviated path
    const parts = fullPath.split('/').filter(Boolean);
    if (parts.length >= 2) {
        return `.../${parts.slice(-2).join('/')}`;
    }
    return fullPath;
}

export default function SessionSelector({ onSelect, onNewTerminal, statusFilter = 'all', onCycleFilter, onOpenPalette, onOpenHelp }) {
    const [sessions, setSessions] = useState([]);
    const [groups, setGroups] = useState([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState(null);
    const [projectRoots, setProjectRoots] = useState([]);
    const [selectedIndex, setSelectedIndex] = useState(0);
    const [contextMenu, setContextMenu] = useState(null); // { x, y, session }
    const [labelingSession, setLabelingSession] = useState(null); // session being labeled
    const [expandedGroups, setExpandedGroups] = useState({}); // path -> bool (desktop overrides)
    const { show: showTooltip, hide: hideTooltip, Tooltip } = useTooltip();

    useEffect(() => {
        loadSessions();
        loadExpandedGroups();
        // Load project roots for relative path display
        GetProjectRoots().then(roots => {
            setProjectRoots(roots || []);
        }).catch(err => {
            logger.warn('Failed to load project roots:', err);
        });
    }, []);

    const loadExpandedGroups = useCallback(async () => {
        try {
            const overrides = await GetExpandedGroups();
            setExpandedGroups(overrides || {});
        } catch (err) {
            logger.warn('Failed to load expanded groups:', err);
        }
    }, []);

    // Tooltip content builder for sessions - returns JSX for rich formatting
    const getTooltipContent = useCallback((session) => {
        const relativeTime = formatRelativeTime(session.lastAccessedAt);
        const relativePath = getRelativeProjectPath(session.projectPath, projectRoots);

        return (
            <div className="session-tooltip">
                {relativeTime && (
                    <div className="tooltip-row tooltip-time">
                        <span className="tooltip-icon">🕐</span>
                        <span>Active {relativeTime}</span>
                    </div>
                )}
                {relativePath && (
                    <div className="tooltip-row tooltip-path">
                        <span className="tooltip-icon">📁</span>
                        <span>{relativePath}</span>
                    </div>
                )}
                {session.isWorktree && (
                    <div className="tooltip-row tooltip-worktree">
                        <span className="tooltip-icon">🌿</span>
                        <span>Git worktree</span>
                    </div>
                )}
                {session.gitDirty && (
                    <div className="tooltip-row tooltip-git-status">
                        <span className="tooltip-icon git-dirty-indicator">●</span>
                        <span>uncommitted changes</span>
                    </div>
                )}
                {(session.gitAhead > 0 || session.gitBehind > 0) && (
                    <div className="tooltip-row tooltip-git-status">
                        <span className="tooltip-icon tooltip-sync-icon">
                            {session.gitAhead > 0 && <span className="git-ahead">↑{session.gitAhead}</span>}
                            {session.gitBehind > 0 && <span className="git-behind">↓{session.gitBehind}</span>}
                        </span>
                        <span>
                            {[
                                session.gitAhead > 0 && `${session.gitAhead} ahead`,
                                session.gitBehind > 0 && `${session.gitBehind} behind`
                            ].filter(Boolean).join(', ')}
                        </span>
                    </div>
                )}
                {session.isRemote && (
                    <div className="tooltip-row tooltip-remote">
                        <span className="tooltip-icon">🌐</span>
                        <span>Remote session via SSH</span>
                    </div>
                )}
            </div>
        );
    }, [projectRoots]);

    const loadSessions = useCallback(async () => {
        try {
            logger.info('Loading sessions with groups...');
            setLoading(true);
            const result = await ListSessionsWithGroups();
            logger.info('Loaded sessions:', result?.sessions?.length || 0, 'groups:', result?.groups?.length || 0);
            setSessions(result?.sessions || []);
            setGroups(result?.groups || []);
            setError(null);
        } catch (err) {
            logger.error('Failed to load sessions:', err);
            setError(err.message || 'Failed to load sessions');
            setSessions([]);
            setGroups([]);
        } finally {
            setLoading(false);
        }
    }, []);

    // Context menu handlers
    const handleContextMenu = useCallback((e, session) => {
        e.preventDefault();
        logger.debug('Context menu on session', { title: session.title });
        // Adjust position to keep menu in viewport (menu is ~180px wide, ~80px tall)
        const menuWidth = 200;
        const menuHeight = 100;
        const x = Math.min(e.clientX, window.innerWidth - menuWidth);
        const y = Math.min(e.clientY, window.innerHeight - menuHeight);
        setContextMenu({
            x,
            y,
            session,
        });
    }, []);

    const closeContextMenu = useCallback(() => {
        setContextMenu(null);
    }, []);

    // Close context menu on click outside
    useEffect(() => {
        if (contextMenu) {
            document.addEventListener('click', closeContextMenu);
            return () => document.removeEventListener('click', closeContextMenu);
        }
    }, [contextMenu, closeContextMenu]);

    const handleAddCustomLabel = useCallback(() => {
        if (!contextMenu?.session) return;
        logger.info('Opening label dialog', { session: contextMenu.session.title });
        setLabelingSession(contextMenu.session);
        setContextMenu(null);
    }, [contextMenu]);

    const handleRemoveCustomLabel = useCallback(async () => {
        if (!contextMenu?.session) return;
        try {
            logger.info('Removing custom label', { sessionId: contextMenu.session.id });
            await UpdateSessionCustomLabel(contextMenu.session.id, '');
            loadSessions(); // Refresh to show updated label
        } catch (err) {
            logger.error('Failed to remove custom label:', err);
        }
        setContextMenu(null);
    }, [contextMenu, loadSessions]);

    const handleSaveCustomLabel = useCallback(async (newLabel) => {
        if (!labelingSession || !newLabel.trim()) return;
        try {
            logger.info('Saving custom label', { sessionId: labelingSession.id, label: newLabel });
            await UpdateSessionCustomLabel(labelingSession.id, newLabel.trim());
            loadSessions(); // Refresh to show updated label
        } catch (err) {
            logger.error('Failed to save custom label:', err);
        }
        setLabelingSession(null);
    }, [labelingSession, loadSessions]);

    const getStatusColor = (status) => {
        switch (status) {
            case 'running': return '#4ecdc4';
            case 'waiting': return '#ffe66d';
            case 'idle': return '#6c757d';
            case 'error': return '#ff6b6b';
            default: return '#6c757d';
        }
    };

    // Check if a group is expanded (desktop override takes precedence over TUI default)
    const isGroupExpanded = useCallback((groupPath) => {
        if (expandedGroups.hasOwnProperty(groupPath)) {
            return expandedGroups[groupPath];
        }
        // Fall back to TUI default from the group data
        const group = groups.find(g => g.path === groupPath);
        return group?.expanded ?? true;
    }, [expandedGroups, groups]);

    // Toggle group expanded state
    const handleGroupToggle = useCallback(async (groupPath, expanded) => {
        try {
            await ToggleGroupExpanded(groupPath, expanded);
            setExpandedGroups(prev => ({
                ...prev,
                [groupPath]: expanded
            }));
        } catch (err) {
            logger.error('Failed to toggle group:', err);
        }
    }, []);

    // Check if a group path is visible (all ancestors are expanded)
    const isGroupVisible = useCallback((groupPath) => {
        if (!groupPath || groupPath === '') return true;

        // Check if any parent is collapsed
        const parts = groupPath.split('/');
        for (let i = 1; i < parts.length; i++) {
            const parentPath = parts.slice(0, i).join('/');
            if (!isGroupExpanded(parentPath)) {
                return false;
            }
        }
        return true;
    }, [isGroupExpanded]);

    // Filter sessions based on current status filter
    const filteredSessions = useMemo(() => {
        let filtered = sessions;
        if (statusFilter === 'active') {
            filtered = sessions.filter(s => s.status === 'running' || s.status === 'waiting');
        } else if (statusFilter === 'idle') {
            filtered = sessions.filter(s => s.status === 'idle');
        }
        return filtered;
    }, [sessions, statusFilter]);

    // Build hierarchical render list with groups and sessions
    const renderList = useMemo(() => {
        const items = [];

        // Create a map of sessions by group path
        // Sessions with empty/undefined groupPath are stored under '' and handled specially later
        const sessionsByGroup = {};
        for (const session of filteredSessions) {
            const groupPath = session.groupPath || '';
            if (!sessionsByGroup[groupPath]) {
                sessionsByGroup[groupPath] = [];
            }
            sessionsByGroup[groupPath].push(session);
        }

        // Sort groups: put my-sessions first, then sort by path
        const sortedGroups = [...groups].sort((a, b) => {
            if (a.path === 'my-sessions') return -1;
            if (b.path === 'my-sessions') return 1;
            return a.path.localeCompare(b.path);
        });

        // Process each group
        for (const group of sortedGroups) {
            // Skip if this group's parent is collapsed
            if (!isGroupVisible(group.path)) {
                continue;
            }

            // Count filtered sessions in this group and its subgroups
            let visibleSessionCount = 0;
            for (const [path, sessionList] of Object.entries(sessionsByGroup)) {
                if (path === group.path || path.startsWith(group.path + '/')) {
                    visibleSessionCount += sessionList.length;
                }
            }

            // Only show groups that have sessions (after filtering) or have subgroups with sessions
            const hasDirectSessions = sessionsByGroup[group.path]?.length > 0;
            const hasSubgroups = sortedGroups.some(g => g.path.startsWith(group.path + '/'));

            if (visibleSessionCount === 0 && !hasSubgroups) {
                continue;
            }

            // Add group header
            items.push({
                type: 'group',
                group: {
                    ...group,
                    // Update counts based on filtered sessions
                    sessionCount: sessionsByGroup[group.path]?.length || 0,
                    totalCount: visibleSessionCount
                }
            });

            // If group is expanded, add its direct sessions
            if (isGroupExpanded(group.path) && hasDirectSessions) {
                for (const session of sessionsByGroup[group.path]) {
                    items.push({
                        type: 'session',
                        session,
                        level: group.level + 1
                    });
                }
            }
        }

        // Handle ungrouped sessions (sessions with empty groupPath and no my-sessions group)
        const ungroupedSessions = sessionsByGroup[''] || [];
        if (ungroupedSessions.length > 0 && !groups.some(g => g.path === 'my-sessions')) {
            // Build the ungrouped items first, then prepend to maintain order
            const ungroupedItems = [];
            ungroupedItems.push({
                type: 'group',
                group: {
                    name: 'My Sessions',
                    path: 'my-sessions',
                    sessionCount: ungroupedSessions.length,
                    totalCount: ungroupedSessions.length,
                    level: 0,
                    hasChildren: false,
                    expanded: true
                }
            });
            if (isGroupExpanded('my-sessions')) {
                for (const session of ungroupedSessions) {
                    ungroupedItems.push({
                        type: 'session',
                        session,
                        level: 1
                    });
                }
            }
            // Prepend ungrouped items to maintain correct order
            items.unshift(...ungroupedItems);
        }

        return items;
    }, [filteredSessions, groups, isGroupExpanded, isGroupVisible]);

    // Reset selected index when render list changes
    useEffect(() => {
        setSelectedIndex(0);
    }, [renderList.length, statusFilter]);

    // Keyboard navigation for session list (works with grouped view)
    useEffect(() => {
        const handleKeyDown = (e) => {
            // Don't handle keyboard nav when dialog is open
            if (labelingSession || contextMenu) return;
            if (renderList.length === 0) return;

            switch (e.key) {
                case 'ArrowDown':
                case 'j':
                    e.preventDefault();
                    setSelectedIndex(prev =>
                        prev < renderList.length - 1 ? prev + 1 : prev
                    );
                    break;
                case 'ArrowUp':
                case 'k':
                    e.preventDefault();
                    setSelectedIndex(prev => prev > 0 ? prev - 1 : prev);
                    break;
                case 'Enter':
                    e.preventDefault();
                    const item = renderList[selectedIndex];
                    if (item) {
                        if (item.type === 'group') {
                            // Toggle group expansion
                            handleGroupToggle(item.group.path, !isGroupExpanded(item.group.path));
                        } else if (item.type === 'session') {
                            onSelect(item.session);
                        }
                    }
                    break;
                case 'ArrowLeft':
                case 'h':
                    e.preventDefault();
                    // Collapse current group or navigate to parent
                    const currentItem = renderList[selectedIndex];
                    if (currentItem?.type === 'group' && isGroupExpanded(currentItem.group.path)) {
                        handleGroupToggle(currentItem.group.path, false);
                    }
                    break;
                case 'ArrowRight':
                case 'l':
                    e.preventDefault();
                    // Expand current group
                    const rightItem = renderList[selectedIndex];
                    if (rightItem?.type === 'group' && !isGroupExpanded(rightItem.group.path)) {
                        handleGroupToggle(rightItem.group.path, true);
                    }
                    break;
                default:
                    break;
            }
        };

        document.addEventListener('keydown', handleKeyDown);
        return () => document.removeEventListener('keydown', handleKeyDown);
    }, [renderList, selectedIndex, onSelect, labelingSession, contextMenu, handleGroupToggle, isGroupExpanded]);

    // Get display label for current filter mode
    const getFilterLabel = () => {
        switch (statusFilter) {
            case 'active': return 'Active';
            case 'idle': return 'Idle';
            default: return 'All';
        }
    };

    // Get color for filter badge
    const getFilterColor = () => {
        switch (statusFilter) {
            case 'active': return '#4ecdc4'; // cyan for active
            case 'idle': return '#6c757d';   // gray for idle
            default: return '#888';          // neutral for all
        }
    };

    // Get tooltip content for filter button explaining current and next states
    const getFilterTooltipContent = useCallback(() => {
        const descriptions = {
            all: 'All sessions (agents + terminals)',
            active: 'Active agents only (running or waiting)',
            idle: 'Terminals only (no active agent)',
        };
        const nextFilter = {
            all: 'active',
            active: 'idle',
            idle: 'all',
        };
        const nextLabels = {
            all: 'All',
            active: 'Active',
            idle: 'Idle',
        };

        const current = descriptions[statusFilter];
        const next = nextFilter[statusFilter];
        const nextLabel = nextLabels[next];
        const nextDesc = descriptions[next];

        return (
            <>
                Showing: {current}
                {'\n\n'}
                Click or <strong>Shift+5</strong> to switch to:
                {'\n'}
                {nextLabel}: {nextDesc}
            </>
        );
    }, [statusFilter]);

    if (loading) {
        return (
            <div className="session-selector">
                <div className="session-loading">Loading sessions...</div>
            </div>
        );
    }

    return (
        <div className="session-selector">
            <div className="session-header">
                <h2>Agent Deck Sessions</h2>
                <div className="header-controls">
                    <button
                        className="filter-btn"
                        onClick={onCycleFilter}
                        onMouseEnter={(e) => showTooltip(e, getFilterTooltipContent())}
                        onMouseLeave={hideTooltip}
                        style={{ borderColor: getFilterColor() }}
                    >
                        <span className="filter-indicator" style={{ backgroundColor: getFilterColor() }} />
                        {getFilterLabel()}
                    </button>
                    <button className="refresh-btn" onClick={loadSessions} title="Refresh">
                        ↻
                    </button>
                </div>
            </div>

            {error && (
                <div className="session-error">{error}</div>
            )}

            <div className="session-list session-list-grouped">
                {renderList.length === 0 ? (
                    <div className="no-sessions">
                        {sessions.length === 0 ? (
                            <>
                                No active sessions found.
                                <br />
                                <small>Start sessions using the Agent Deck TUI.</small>
                            </>
                        ) : (
                            <>
                                No {statusFilter === 'active' ? 'active' : 'idle'} sessions.
                                <br />
                                <small>Press Shift+5 to show all sessions.</small>
                            </>
                        )}
                    </div>
                ) : (
                    renderList.map((item, index) => {
                        if (item.type === 'group') {
                            return (
                                <GroupHeader
                                    key={`group-${item.group.path}`}
                                    group={item.group}
                                    isExpanded={isGroupExpanded(item.group.path)}
                                    isSelected={index === selectedIndex}
                                    onToggle={handleGroupToggle}
                                    onClick={() => setSelectedIndex(index)}
                                />
                            );
                        }

                        const session = item.session;
                        const levelClass = `level-${Math.min(item.level, 3)}`;

                        return (
                            <button
                                key={session.id}
                                className={`session-item in-group ${levelClass}${index === selectedIndex ? ' selected' : ''}${session.isRemote ? ' remote' : ''}`}
                                onClick={() => onSelect(session)}
                                onContextMenu={(e) => handleContextMenu(e, session)}
                                onMouseEnter={(e) => {
                                    setSelectedIndex(index);
                                    showTooltip(e, getTooltipContent(session));
                                }}
                                onMouseLeave={hideTooltip}
                            >
                                <span
                                    className="session-tool"
                                    style={{ backgroundColor: getStatusColor(session.status) }}
                                >
                                    <ToolIcon tool={session.tool} size={16} />
                                </span>
                                <div className="session-info">
                                    <div className="session-title">
                                        {session.dangerousMode && (
                                            <span className="session-danger-icon" title="Dangerous mode enabled">⚠</span>
                                        )}
                                        {session.isRemote && (
                                            <span className="session-remote-badge" title={`Remote session on ${session.remoteHost}`}>
                                                {session.remoteHost}
                                            </span>
                                        )}
                                        {session.title}
                                        {session.customLabel && (
                                            <span className="session-custom-label">{session.customLabel}</span>
                                        )}
                                    </div>
                                    <div className="session-meta">
                                        {session.launchConfigName && (
                                            <>
                                                <span className="session-config">{session.launchConfigName}</span>
                                                <span className="meta-separator">•</span>
                                            </>
                                        )}
                                        {session.gitBranch && (
                                            <>
                                                <span className={`session-branch${session.isWorktree ? ' is-worktree' : ''}`}>
                                                    {session.gitDirty && <span className="git-dirty-indicator" title="Uncommitted changes">●</span>}
                                                    <span className="branch-icon">{session.isWorktree ? '🌿' : '⎇'}</span>
                                                    {session.gitBranch}
                                                </span>
                                                {(session.gitAhead > 0 || session.gitBehind > 0) && (
                                                    <span className="git-sync-status">
                                                        {session.gitAhead > 0 && <span className="git-ahead" title={`${session.gitAhead} commit${session.gitAhead > 1 ? 's' : ''} ahead`}>↑{session.gitAhead}</span>}
                                                        {session.gitBehind > 0 && <span className="git-behind" title={`${session.gitBehind} commit${session.gitBehind > 1 ? 's' : ''} behind`}>↓{session.gitBehind}</span>}
                                                    </span>
                                                )}
                                            </>
                                        )}
                                    </div>
                                </div>
                                <span
                                    className="session-status"
                                    style={{ color: getStatusColor(session.status) }}
                                >
                                    {session.status}
                                </span>
                            </button>
                        );
                    })
                )}
            </div>

            <ShortcutBar
                view="selector"
                onNewTerminal={onNewTerminal}
                onOpenPalette={onOpenPalette}
                onCycleFilter={onCycleFilter}
                onOpenHelp={onOpenHelp}
            />

            <Tooltip />

            {contextMenu && (
                <div
                    className="session-context-menu"
                    style={{ left: contextMenu.x, top: contextMenu.y }}
                    onClick={(e) => e.stopPropagation()}
                >
                    <button onClick={handleAddCustomLabel}>
                        {contextMenu.session?.customLabel ? 'Edit Custom Label' : 'Add Custom Label'}
                    </button>
                    {contextMenu.session?.customLabel && (
                        <button onClick={handleRemoveCustomLabel}>
                            Remove Custom Label
                        </button>
                    )}
                </div>
            )}

            {labelingSession && (
                <RenameDialog
                    currentName={labelingSession.customLabel || ''}
                    title={labelingSession.customLabel ? 'Edit Custom Label' : 'Add Custom Label'}
                    placeholder="Enter label..."
                    onSave={handleSaveCustomLabel}
                    onCancel={() => setLabelingSession(null)}
                />
            )}
        </div>
    );
}
