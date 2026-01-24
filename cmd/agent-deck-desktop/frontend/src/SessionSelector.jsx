import { useState, useEffect, useCallback, useMemo } from 'react';
import { ListSessions, GetProjectRoots } from '../wailsjs/go/main/App';
import './SessionSelector.css';
import { createLogger } from './logger';
import ToolIcon from './ToolIcon';
import { useTooltip } from './Tooltip';
import ShortcutBar from './ShortcutBar';

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
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState(null);
    const [projectRoots, setProjectRoots] = useState([]);
    const [selectedIndex, setSelectedIndex] = useState(0);
    const { show: showTooltip, hide: hideTooltip, Tooltip } = useTooltip();

    useEffect(() => {
        loadSessions();
        // Load project roots for relative path display
        GetProjectRoots().then(roots => {
            setProjectRoots(roots || []);
        }).catch(err => {
            logger.warn('Failed to load project roots:', err);
        });
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
                {session.isRemote && (
                    <div className="tooltip-row tooltip-remote">
                        <span className="tooltip-icon">⚠️</span>
                        <span>Remote (not yet supported)</span>
                    </div>
                )}
            </div>
        );
    }, [projectRoots]);

    const loadSessions = async () => {
        try {
            logger.info('Loading sessions...');
            setLoading(true);
            const result = await ListSessions();
            logger.info('Loaded sessions:', result?.length || 0);
            setSessions(result || []);
            setError(null);
        } catch (err) {
            logger.error('Failed to load sessions:', err);
            setError(err.message || 'Failed to load sessions');
            setSessions([]);
        } finally {
            setLoading(false);
        }
    };

    const getStatusColor = (status) => {
        switch (status) {
            case 'running': return '#4ecdc4';
            case 'waiting': return '#ffe66d';
            case 'idle': return '#6c757d';
            case 'error': return '#ff6b6b';
            default: return '#6c757d';
        }
    };

    // Filter sessions based on current status filter
    const filteredSessions = useMemo(() => {
        if (statusFilter === 'all') return sessions;
        if (statusFilter === 'active') {
            return sessions.filter(s => s.status === 'running' || s.status === 'waiting');
        }
        if (statusFilter === 'idle') {
            return sessions.filter(s => s.status === 'idle');
        }
        return sessions;
    }, [sessions, statusFilter]);

    // Reset selected index when filtered sessions change
    useEffect(() => {
        setSelectedIndex(0);
    }, [filteredSessions.length, statusFilter]);

    // Keyboard navigation for session list
    useEffect(() => {
        const handleKeyDown = (e) => {
            if (filteredSessions.length === 0) return;

            switch (e.key) {
                case 'ArrowDown':
                case 'j':
                    e.preventDefault();
                    setSelectedIndex(prev =>
                        prev < filteredSessions.length - 1 ? prev + 1 : prev
                    );
                    break;
                case 'ArrowUp':
                case 'k':
                    e.preventDefault();
                    setSelectedIndex(prev => prev > 0 ? prev - 1 : prev);
                    break;
                case 'Enter':
                    e.preventDefault();
                    const session = filteredSessions[selectedIndex];
                    if (session && !session.isRemote) {
                        onSelect(session);
                    }
                    break;
                default:
                    break;
            }
        };

        document.addEventListener('keydown', handleKeyDown);
        return () => document.removeEventListener('keydown', handleKeyDown);
    }, [filteredSessions, selectedIndex, onSelect]);

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

            <div className="session-list">
                {filteredSessions.length === 0 ? (
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
                    filteredSessions.map((session, index) => (
                        <button
                            key={session.id}
                            className={`session-item${index === selectedIndex ? ' selected' : ''}`}
                            onClick={() => onSelect(session)}
                            disabled={session.isRemote}
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
                                    {session.title}
                                </div>
                                <div className="session-meta">
                                    <span className="session-group">{session.groupPath || 'ungrouped'}</span>
                                    {session.launchConfigName && (
                                        <>
                                            <span className="meta-separator">•</span>
                                            <span className="session-config">{session.launchConfigName}</span>
                                        </>
                                    )}
                                    {session.gitBranch && (
                                        <>
                                            <span className="meta-separator">•</span>
                                            <span className={`session-branch${session.isWorktree ? ' is-worktree' : ''}`}>
                                                <span className="branch-icon">{session.isWorktree ? '🌿' : '⎇'}</span>
                                                {session.gitBranch}
                                            </span>
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
                    ))
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
        </div>
    );
}
