import { useState, useEffect, useCallback, useMemo } from 'react';
import { ListSessions } from '../wailsjs/go/main/App';
import './SessionSelector.css';
import { createLogger } from './logger';
import ToolIcon from './ToolIcon';
import { useTooltip } from './Tooltip';

const logger = createLogger('SessionSelector');

export default function SessionSelector({ onSelect, onNewTerminal, statusFilter = 'all', onCycleFilter }) {
    const [sessions, setSessions] = useState([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState(null);
    const { show: showTooltip, hide: hideTooltip, Tooltip } = useTooltip();

    useEffect(() => {
        loadSessions();
    }, []);

    // Tooltip content builder for sessions
    const getTooltipContent = useCallback((session) => {
        const lines = [session.title];
        if (session.projectPath) {
            lines.push(session.projectPath);
        }
        if (session.isWorktree) {
            lines.push('Git worktree (separate working directory)');
        }
        if (session.isRemote) {
            lines.push('Remote sessions not supported yet');
        }
        return lines.join('\n');
    }, []);

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

        return `Showing: ${current}\n\nClick or Shift+5 to switch to:\n${nextLabel}: ${nextDesc}`;
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
                    filteredSessions.map((session) => (
                        <button
                            key={session.id}
                            className="session-item"
                            onClick={() => onSelect(session)}
                            disabled={session.isRemote}
                            onMouseEnter={(e) => showTooltip(e, getTooltipContent(session))}
                            onMouseLeave={hideTooltip}
                        >
                            <span
                                className="session-tool"
                                style={{ backgroundColor: getStatusColor(session.status) }}
                            >
                                <ToolIcon tool={session.tool} size={16} />
                            </span>
                            <div className="session-info">
                                <div className="session-title">{session.title}</div>
                                <div className="session-meta">
                                    <span className="session-group">{session.groupPath || 'ungrouped'}</span>
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

            <div className="session-footer">
                <button className="new-terminal-btn" onClick={onNewTerminal}>
                    New Terminal
                </button>
            </div>

            <Tooltip />
        </div>
    );
}
