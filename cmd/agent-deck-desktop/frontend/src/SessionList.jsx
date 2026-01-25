import { useState, useEffect, useCallback, useMemo, useRef, forwardRef, useImperativeHandle } from 'react';
import { ListSessionsWithGroups, GetProjectRoots, GetExpandedGroups, ToggleGroupExpanded, SetAllGroupsExpanded, GetSSHHostStatus } from '../wailsjs/go/main/App';
import { createLogger } from './logger';
import ToolIcon from './ToolIcon';
import GroupHeader from './GroupHeader';
import GroupControls from './GroupControls';

const logger = createLogger('SessionList');

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
    if (diffMin < 60) return `${diffMin}m`;
    if (diffHour < 24) return `${diffHour}h`;
    if (diffDay < 7) return `${diffDay}d`;
    if (diffDay < 30) return `${Math.floor(diffDay / 7)}w`;
    return date.toLocaleDateString();
}

// Compute relative project path based on configured roots
function getRelativeProjectPath(fullPath, projectRoots) {
    if (!fullPath || !projectRoots || projectRoots.length === 0) {
        const parts = fullPath?.split('/').filter(Boolean) || [];
        if (parts.length >= 2) {
            return `.../${parts.slice(-2).join('/')}`;
        }
        return fullPath || '';
    }

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

    const parts = fullPath.split('/').filter(Boolean);
    if (parts.length >= 2) {
        return `.../${parts.slice(-2).join('/')}`;
    }
    return fullPath;
}

const SessionList = forwardRef(function SessionList({
    onSelect,
    onPreview,
    selectedSessionId,
    statusFilter = 'all',
    onCycleFilter,
}, ref) {
    const [sessions, setSessions] = useState([]);
    const [groups, setGroups] = useState([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState(null);
    const [projectRoots, setProjectRoots] = useState([]);
    const [selectedIndex, setSelectedIndex] = useState(0);
    const [expandedGroups, setExpandedGroups] = useState({});
    const [sshHostStatus, setSshHostStatus] = useState({});
    const listRef = useRef(null);

    useEffect(() => {
        loadSessions();
        loadExpandedGroups();
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

    // Poll SSH host status
    useEffect(() => {
        const fetchHostStatus = async () => {
            try {
                const statuses = await GetSSHHostStatus();
                if (Array.isArray(statuses)) {
                    const statusMap = {};
                    for (const s of statuses) {
                        statusMap[s.hostId] = {
                            connected: s.connected,
                            lastError: s.lastError,
                        };
                    }
                    setSshHostStatus(statusMap);
                }
            } catch (err) {
                logger.warn('Failed to fetch SSH host status:', err);
            }
        };

        fetchHostStatus();
        const interval = setInterval(fetchHostStatus, 30000);
        return () => clearInterval(interval);
    }, []);

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

    const getStatusColor = (status) => {
        switch (status) {
            case 'running': return '#4ecdc4';
            case 'waiting': return '#ffe66d';
            case 'idle': return '#6c757d';
            case 'error': return '#ff6b6b';
            default: return '#6c757d';
        }
    };

    // Check if a group is expanded
    const isGroupExpanded = useCallback((groupPath) => {
        if (expandedGroups.hasOwnProperty(groupPath)) {
            return expandedGroups[groupPath];
        }
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

    // Collapse all groups
    const handleCollapseAll = useCallback(async () => {
        try {
            const groupPaths = groups.map(g => g.path);
            await SetAllGroupsExpanded(groupPaths, false);
            const newExpandedGroups = {};
            for (const path of groupPaths) {
                newExpandedGroups[path] = false;
            }
            setExpandedGroups(newExpandedGroups);
            logger.info('Collapsed all groups', { count: groupPaths.length });
        } catch (err) {
            logger.error('Failed to collapse all groups:', err);
        }
    }, [groups]);

    // Expand all groups
    const handleExpandAll = useCallback(async () => {
        try {
            const groupPaths = groups.map(g => g.path);
            await SetAllGroupsExpanded(groupPaths, true);
            const newExpandedGroups = {};
            for (const path of groupPaths) {
                newExpandedGroups[path] = true;
            }
            setExpandedGroups(newExpandedGroups);
            logger.info('Expanded all groups', { count: groupPaths.length });
        } catch (err) {
            logger.error('Failed to expand all groups:', err);
        }
    }, [groups]);

    // Toggle all groups: collapse if any expanded, expand if all collapsed
    const handleToggleAllGroups = useCallback(async () => {
        let anyExpanded = false;
        for (const group of groups) {
            const isExpanded = expandedGroups.hasOwnProperty(group.path)
                ? expandedGroups[group.path]
                : (group.expanded ?? true);
            if (isExpanded) {
                anyExpanded = true;
                break;
            }
        }

        if (anyExpanded) {
            await handleCollapseAll();
        } else {
            await handleExpandAll();
        }
    }, [groups, expandedGroups, handleCollapseAll, handleExpandAll]);

    // Expose toggle function to parent via ref
    useImperativeHandle(ref, () => ({
        toggleAllGroups: handleToggleAllGroups,
    }), [handleToggleAllGroups]);

    // Check if a group path is visible
    const isGroupVisible = useCallback((groupPath) => {
        if (!groupPath || groupPath === '') return true;

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

        const sessionsByGroup = {};
        for (const session of filteredSessions) {
            const groupPath = session.groupPath || '';
            if (!sessionsByGroup[groupPath]) {
                sessionsByGroup[groupPath] = [];
            }
            sessionsByGroup[groupPath].push(session);
        }

        const sortedGroups = [...groups].sort((a, b) => {
            if (a.path === 'my-sessions') return -1;
            if (b.path === 'my-sessions') return 1;
            return a.path.localeCompare(b.path);
        });

        for (const group of sortedGroups) {
            if (!isGroupVisible(group.path)) {
                continue;
            }

            let visibleSessionCount = 0;
            for (const [path, sessionList] of Object.entries(sessionsByGroup)) {
                if (path === group.path || path.startsWith(group.path + '/')) {
                    visibleSessionCount += sessionList.length;
                }
            }

            const hasDirectSessions = sessionsByGroup[group.path]?.length > 0;
            const hasSubgroups = sortedGroups.some(g => g.path.startsWith(group.path + '/'));

            if (visibleSessionCount === 0 && !hasSubgroups) {
                continue;
            }

            items.push({
                type: 'group',
                group: {
                    ...group,
                    sessionCount: sessionsByGroup[group.path]?.length || 0,
                    totalCount: visibleSessionCount
                }
            });

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

        const ungroupedSessions = sessionsByGroup[''] || [];
        if (ungroupedSessions.length > 0 && !groups.some(g => g.path === 'my-sessions')) {
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
            items.unshift(...ungroupedItems);
        }

        return items;
    }, [filteredSessions, groups, isGroupExpanded, isGroupVisible]);

    // Reset selected index when render list changes
    useEffect(() => {
        setSelectedIndex(0);
    }, [renderList.length, statusFilter]);

    // Find current session item index and notify preview
    useEffect(() => {
        const sessionItems = renderList.filter(item => item.type === 'session');
        if (sessionItems.length > 0 && onPreview) {
            const sessionIndices = renderList
                .map((item, idx) => item.type === 'session' ? { item, idx } : null)
                .filter(Boolean);

            if (selectedIndex < renderList.length) {
                const currentItem = renderList[selectedIndex];
                if (currentItem?.type === 'session') {
                    onPreview(currentItem.session);
                }
            }
        }
    }, [selectedIndex, renderList, onPreview]);

    // Keyboard navigation for session list
    useEffect(() => {
        const handleKeyDown = (e) => {
            if (renderList.length === 0) return;

            switch (e.key) {
                case 'ArrowDown':
                case 'j':
                    e.preventDefault();
                    setSelectedIndex(prev => {
                        const newIndex = prev < renderList.length - 1 ? prev + 1 : prev;
                        // Update preview when moving to a session
                        const item = renderList[newIndex];
                        if (item?.type === 'session' && onPreview) {
                            onPreview(item.session);
                        }
                        return newIndex;
                    });
                    break;
                case 'ArrowUp':
                case 'k':
                    e.preventDefault();
                    setSelectedIndex(prev => {
                        const newIndex = prev > 0 ? prev - 1 : prev;
                        const item = renderList[newIndex];
                        if (item?.type === 'session' && onPreview) {
                            onPreview(item.session);
                        }
                        return newIndex;
                    });
                    break;
                case 'Enter':
                    e.preventDefault();
                    const item = renderList[selectedIndex];
                    if (item) {
                        if (item.type === 'group') {
                            handleGroupToggle(item.group.path, !isGroupExpanded(item.group.path));
                        } else if (item.type === 'session') {
                            onSelect(item.session);
                        }
                    }
                    break;
                case 'ArrowLeft':
                case 'h':
                    e.preventDefault();
                    const currentItem = renderList[selectedIndex];
                    if (currentItem?.type === 'group' && isGroupExpanded(currentItem.group.path)) {
                        handleGroupToggle(currentItem.group.path, false);
                    }
                    break;
                case 'ArrowRight':
                case 'l':
                    e.preventDefault();
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
    }, [renderList, selectedIndex, onSelect, onPreview, handleGroupToggle, isGroupExpanded]);

    // Scroll selected item into view
    useEffect(() => {
        if (listRef.current) {
            const selectedEl = listRef.current.querySelector('.selected');
            if (selectedEl) {
                selectedEl.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
            }
        }
    }, [selectedIndex]);

    // Get display label for current filter mode
    const getFilterLabel = () => {
        switch (statusFilter) {
            case 'active': return 'Active';
            case 'idle': return 'Idle';
            default: return 'All';
        }
    };

    const getFilterColor = () => {
        switch (statusFilter) {
            case 'active': return '#4ecdc4';
            case 'idle': return '#6c757d';
            default: return '#888';
        }
    };

    if (loading) {
        return (
            <div className="session-list-pane">
                <div className="session-list-loading">Loading sessions...</div>
            </div>
        );
    }

    return (
        <div className="session-list-pane">
            <div className="session-list-header">
                <h3>Sessions</h3>
                <div className="session-list-controls">
                    {groups.length > 0 && (
                        <GroupControls
                            groups={groups}
                            expandedGroups={expandedGroups}
                            onCollapseAll={handleCollapseAll}
                            onExpandAll={handleExpandAll}
                        />
                    )}
                    <button
                        className="filter-btn-compact"
                        onClick={onCycleFilter}
                        style={{ borderColor: getFilterColor() }}
                        title="Cycle filter: All / Active / Idle (Shift+5)"
                    >
                        <span className="filter-indicator" style={{ backgroundColor: getFilterColor() }} />
                        {getFilterLabel()}
                    </button>
                    <button className="refresh-btn-compact" onClick={loadSessions} title="Refresh">
                        ↻
                    </button>
                </div>
            </div>

            {error && (
                <div className="session-list-error">{error}</div>
            )}

            <div className="session-list-items" ref={listRef}>
                {renderList.length === 0 ? (
                    <div className="no-sessions-compact">
                        {sessions.length === 0 ? (
                            <>No sessions</>
                        ) : (
                            <>No {statusFilter} sessions</>
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
                        const isSelected = index === selectedIndex;
                        const isCurrentSession = session.id === selectedSessionId;
                        const relativeTime = formatRelativeTime(session.lastAccessedAt);

                        return (
                            <button
                                key={session.id}
                                className={`session-list-item ${levelClass}${isSelected ? ' selected' : ''}${isCurrentSession ? ' current' : ''}${session.isRemote ? ' remote' : ''}`}
                                onClick={() => {
                                    setSelectedIndex(index);
                                    if (onPreview) onPreview(session);
                                }}
                                onDoubleClick={() => onSelect(session)}
                            >
                                <span
                                    className="session-list-tool"
                                    style={{ backgroundColor: getStatusColor(session.status) }}
                                >
                                    <ToolIcon tool={session.tool} size={14} />
                                </span>
                                <div className="session-list-info">
                                    <div className="session-list-title">
                                        {session.dangerousMode && (
                                            <span className="session-danger-icon" title="Dangerous mode">!</span>
                                        )}
                                        {session.isRemote && (
                                            <span className="session-remote-indicator" title={`Remote: ${session.remoteHost}`}>
                                                {(() => {
                                                    const hostStatus = sshHostStatus[session.remoteHost];
                                                    const isDisconnected = hostStatus?.connected === false;
                                                    return (
                                                        <span className={`ssh-dot${isDisconnected ? ' disconnected' : ''}`} />
                                                    );
                                                })()}
                                            </span>
                                        )}
                                        {session.customLabel || session.title}
                                    </div>
                                    <div className="session-list-meta">
                                        {session.gitBranch && (
                                            <span className={`branch-badge${session.isWorktree ? ' worktree' : ''}`}>
                                                {session.gitBranch}
                                            </span>
                                        )}
                                        {relativeTime && (
                                            <span className="time-badge">{relativeTime}</span>
                                        )}
                                    </div>
                                </div>
                                <span
                                    className="session-list-status"
                                    style={{ color: getStatusColor(session.status) }}
                                >
                                    {session.status === 'running' ? '●' : session.status === 'waiting' ? '◐' : '○'}
                                </span>
                            </button>
                        );
                    })
                )}
            </div>

            <div className="session-list-footer">
                <span className="session-count">{filteredSessions.length} session{filteredSessions.length !== 1 ? 's' : ''}</span>
                <span className="keyboard-hint">↑↓ navigate • Enter attach</span>
            </div>
        </div>
    );
});

export default SessionList;
