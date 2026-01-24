import { useCallback, useEffect, useRef, useState } from 'react';
import Terminal from './Terminal';
import PaneOverlay from './PaneOverlay';
import { createLogger } from './logger';
import { GetGitBranch, IsGitWorktree } from '../wailsjs/go/main/App';

const logger = createLogger('Pane');

/**
 * Pane - Individual pane wrapper for the multi-pane layout
 *
 * Handles:
 * - Terminal rendering for sessions
 * - Empty state UI when no session assigned
 * - Focus management and visual indicators
 * - Pane header with session info
 * - Move mode overlay with pane numbers
 */
export default function Pane({
    paneId,
    session,
    isActive,
    onFocus,
    onSessionSelect,
    terminalRefs,
    searchRefs,
    fontSize,
    moveMode = false,
    paneNumber = 0,
}) {
    const paneRef = useRef(null);
    const [gitBranch, setGitBranch] = useState('');
    const [isWorktree, setIsWorktree] = useState(false);

    // Load git info when session changes
    useEffect(() => {
        if (session?.projectPath) {
            Promise.all([
                GetGitBranch(session.projectPath),
                IsGitWorktree(session.projectPath)
            ]).then(([branch, worktree]) => {
                setGitBranch(branch || '');
                setIsWorktree(worktree);
            }).catch(() => {
                setGitBranch('');
                setIsWorktree(false);
            });
        } else {
            setGitBranch('');
            setIsWorktree(false);
        }
    }, [session?.projectPath]);

    // Handle click on pane to focus
    const handlePaneClick = useCallback(() => {
        if (!isActive && onFocus) {
            logger.debug('Pane clicked, focusing', { paneId });
            onFocus(paneId);
        }
    }, [paneId, isActive, onFocus]);

    // Handle terminal focus event
    const handleTerminalFocus = useCallback(() => {
        if (onFocus) {
            onFocus(paneId);
        }
    }, [paneId, onFocus]);

    // Handle empty pane click - open session selector
    const handleEmptyClick = useCallback(() => {
        if (onFocus) {
            onFocus(paneId);
        }
        if (onSessionSelect) {
            onSessionSelect(paneId);
        }
    }, [paneId, onFocus, onSessionSelect]);

    // Store terminal ref for this pane
    const searchAddonRef = useRef(null);

    useEffect(() => {
        if (terminalRefs && paneId) {
            terminalRefs.current = terminalRefs.current || {};
        }
        if (searchRefs && paneId && searchAddonRef.current) {
            searchRefs.current = searchRefs.current || {};
            searchRefs.current[paneId] = searchAddonRef.current;
        }
    }, [paneId, terminalRefs, searchRefs]);

    // Render empty pane
    if (!session) {
        return (
            <div
                ref={paneRef}
                className={`pane-wrapper ${isActive ? 'pane-active' : ''}`}
                onClick={handleEmptyClick}
            >
                <div className="pane-header">
                    <div className="pane-header-title">
                        <span style={{ color: 'var(--text-muted)' }}>Empty</span>
                    </div>
                </div>
                <div className="pane-content">
                    <div className="pane-empty">
                        <div className="pane-empty-icon">+</div>
                        <div className="pane-empty-text">No session</div>
                        <div className="pane-empty-hint">
                            Press <kbd>Cmd</kbd>+<kbd>K</kbd> to open a session
                        </div>
                    </div>
                </div>
                {moveMode && <PaneOverlay number={paneNumber} isActive={isActive} />}
            </div>
        );
    }

    // Render pane with terminal
    return (
        <div
            ref={paneRef}
            className={`pane-wrapper ${isActive ? 'pane-active' : ''}`}
            onClick={handlePaneClick}
        >
            <div className="pane-header">
                <div className="pane-header-title">
                    {session.dangerousMode && (
                        <span className="header-danger-icon" title="Dangerous mode enabled">!</span>
                    )}
                    <span className="session-title">
                        {session.customLabel || session.title}
                    </span>
                    {gitBranch && (
                        <span className={`git-branch${isWorktree ? ' is-worktree' : ''}`}>
                            <span className="git-branch-icon">{isWorktree ? 'W' : 'B'}</span>
                            {gitBranch}
                        </span>
                    )}
                    {session.launchConfigName && (
                        <span className="header-config-badge">{session.launchConfigName}</span>
                    )}
                </div>
            </div>
            <div className="pane-content">
                <Terminal
                    searchRef={searchAddonRef}
                    session={session}
                    paneId={paneId}
                    onFocus={handleTerminalFocus}
                    fontSize={fontSize}
                />
            </div>
            {moveMode && <PaneOverlay number={paneNumber} isActive={isActive} />}
        </div>
    );
}
