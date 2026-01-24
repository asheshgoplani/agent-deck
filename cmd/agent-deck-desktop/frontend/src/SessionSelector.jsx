import { useState, useEffect } from 'react';
import { ListSessions } from '../wailsjs/go/main/App';
import './SessionSelector.css';
import { createLogger } from './logger';

const logger = createLogger('SessionSelector');

export default function SessionSelector({ onSelect, onNewTerminal }) {
    const [sessions, setSessions] = useState([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState(null);

    useEffect(() => {
        loadSessions();
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

    const getToolIcon = (tool) => {
        switch (tool) {
            case 'claude': return 'C';
            case 'gemini': return 'G';
            case 'opencode': return 'O';
            default: return '$';
        }
    };

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
                <button className="refresh-btn" onClick={loadSessions} title="Refresh">
                    ↻
                </button>
            </div>

            {error && (
                <div className="session-error">{error}</div>
            )}

            <div className="session-list">
                {sessions.length === 0 ? (
                    <div className="no-sessions">
                        No active sessions found.
                        <br />
                        <small>Start sessions using the Agent Deck TUI.</small>
                    </div>
                ) : (
                    sessions.map((session) => (
                        <button
                            key={session.id}
                            className="session-item"
                            onClick={() => onSelect(session)}
                            disabled={session.isRemote}
                            title={session.isRemote ? 'Remote sessions not supported yet' : session.projectPath}
                        >
                            <span
                                className="session-tool"
                                style={{ backgroundColor: getStatusColor(session.status) }}
                            >
                                {getToolIcon(session.tool)}
                            </span>
                            <div className="session-info">
                                <div className="session-title">{session.title}</div>
                                <div className="session-group">{session.groupPath || 'ungrouped'}</div>
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
        </div>
    );
}
