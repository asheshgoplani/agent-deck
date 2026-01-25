import { useMemo } from 'react';
import './StatusBar.css';
import ToolIcon from './ToolIcon';
import { useSessionMetadata } from './hooks/useSessionMetadata';

/**
 * StatusBar - Displays contextual session information at the bottom of a pane.
 *
 * Format: [hostname] [path] [branch] [session-name] [tool]
 *
 * @param {Object} session - The session object
 */
export default function StatusBar({ session }) {
    const {
        hostname,
        cwd,
        gitBranch,
        isWorktree,
        isLoading,
    } = useSessionMetadata(session);

    // Determine display hostname - for remote sessions, use remoteHost
    const displayHostname = session?.isRemote ? session.remoteHost : hostname;
    const isRemote = session?.isRemote || false;

    // Shorten the path for display (show last 2-3 segments)
    const shortPath = useMemo(() => {
        if (!cwd) return '';
        const parts = cwd.split('/').filter(Boolean);
        if (parts.length <= 3) return cwd;
        return '~/' + parts.slice(-2).join('/');
    }, [cwd]);

    // Session display name
    const sessionName = session?.customLabel || session?.title || 'Unknown';

    // Tool name for display
    const toolName = session?.tool || 'shell';

    if (!session) {
        return null;
    }

    return (
        <div className="status-bar">
            {/* Hostname */}
            {displayHostname && (
                <div
                    className={`status-bar-item status-bar-hostname${isRemote ? ' is-remote' : ''}`}
                    title={`Host: ${displayHostname}${isRemote ? ' (remote)' : ''}`}
                >
                    <span className="status-bar-icon">{isRemote ? '🌐' : '@'}</span>
                    <span className="status-bar-text">{displayHostname}</span>
                </div>
            )}

            {/* Current Working Directory */}
            {shortPath && (
                <div className="status-bar-item status-bar-path" title={cwd}>
                    <span className="status-bar-icon">📁</span>
                    <span className="status-bar-text">{shortPath}</span>
                </div>
            )}

            {/* Git Branch */}
            {gitBranch && (
                <div
                    className={`status-bar-item status-bar-branch${isWorktree ? ' is-worktree' : ''}`}
                    title={`Branch: ${gitBranch}${isWorktree ? ' (worktree)' : ''}`}
                >
                    <span className="status-bar-icon">{isWorktree ? '🌿' : '⎇'}</span>
                    <span className="status-bar-text">{gitBranch}</span>
                </div>
            )}

            {/* Spacer to push right items */}
            <div className="status-bar-spacer" />

            {/* Session Name */}
            <div className="status-bar-item status-bar-session" title={`Session: ${sessionName}`}>
                <span className="status-bar-text">{sessionName}</span>
            </div>

            {/* Tool */}
            <div className="status-bar-item status-bar-tool" title={`Tool: ${toolName}`}>
                <ToolIcon tool={toolName} size={12} />
                <span className="status-bar-text">{toolName}</span>
            </div>

            {/* Loading indicator */}
            {isLoading && (
                <div className="status-bar-item status-bar-loading">
                    <span className="status-bar-spinner">⟳</span>
                </div>
            )}
        </div>
    );
}
