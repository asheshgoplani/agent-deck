import { useEffect, useRef, useState, useCallback } from 'react';
import { Terminal as XTerm } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import { StartTmuxSession, StartRemoteTmuxSession, CloseTerminal, ResizeTerminal, WriteTerminal, GetGitBranch, IsGitWorktree } from '../wailsjs/go/main/App';
import { createLogger } from './logger';
import { DEFAULT_FONT_SIZE } from './constants/terminal';
import { EventsOn } from '../wailsjs/runtime/runtime';
import { useTheme } from './context/ThemeContext';
import { getTerminalTheme } from './themes/terminal';
import ToolIcon from './ToolIcon';

const logger = createLogger('SessionPreview');

// Base terminal options for preview (read-only feel)
const BASE_TERMINAL_OPTIONS = {
    fontFamily: '"MesloLGS NF", Menlo, Monaco, "Courier New", monospace',
    fontSize: 12, // Slightly smaller for preview
    lineHeight: 1.2,
    cursorBlink: false, // No blink for preview
    cursorStyle: 'underline',
    scrollback: 1000,
    allowProposedApi: true,
    smoothScrollDuration: 0,
    disableStdin: false, // Allow scrolling
};

export default function SessionPreview({ session, onAttach, fontSize = DEFAULT_FONT_SIZE }) {
    const terminalRef = useRef(null);
    const xtermRef = useRef(null);
    const fitAddonRef = useRef(null);
    const initRef = useRef(false);
    const sessionIdRef = useRef(null);
    const [gitBranch, setGitBranch] = useState('');
    const [isWorktree, setIsWorktree] = useState(false);
    const { theme } = useTheme();

    // Format relative time
    const formatRelativeTime = useCallback((dateString) => {
        if (!dateString) return null;
        const date = new Date(dateString);
        if (isNaN(date.getTime())) return null;

        const now = new Date();
        const diffMs = now - date;
        const diffMin = Math.floor(diffMs / 60000);
        const diffHour = Math.floor(diffMin / 60);
        const diffDay = Math.floor(diffHour / 24);

        if (diffMin < 1) return 'just now';
        if (diffMin < 60) return `${diffMin} min ago`;
        if (diffHour < 24) return `${diffHour} hour${diffHour > 1 ? 's' : ''} ago`;
        if (diffDay < 7) return `${diffDay} day${diffDay > 1 ? 's' : ''} ago`;
        return date.toLocaleDateString();
    }, []);

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

    // Update terminal theme when app theme changes
    useEffect(() => {
        if (xtermRef.current) {
            const terminalTheme = getTerminalTheme(theme);
            xtermRef.current.options.theme = terminalTheme;
        }
    }, [theme]);

    // Initialize/update terminal when session changes
    useEffect(() => {
        if (!terminalRef.current || !session) {
            // Clean up if session is null
            if (xtermRef.current) {
                if (sessionIdRef.current) {
                    CloseTerminal(`preview-${sessionIdRef.current}`).catch(err => {
                        logger.warn('Failed to close preview terminal:', err);
                    });
                }
                xtermRef.current.dispose();
                xtermRef.current = null;
                fitAddonRef.current = null;
                sessionIdRef.current = null;
                initRef.current = false;
            }
            return;
        }

        // If switching to a different session, clean up old one
        if (sessionIdRef.current && sessionIdRef.current !== session.id) {
            if (xtermRef.current) {
                CloseTerminal(`preview-${sessionIdRef.current}`).catch(err => {
                    logger.warn('Failed to close preview terminal:', err);
                });
                xtermRef.current.dispose();
                xtermRef.current = null;
                fitAddonRef.current = null;
            }
            initRef.current = false;
        }

        sessionIdRef.current = session.id;
        const previewSessionId = `preview-${session.id}`;

        // Only initialize once per session
        if (initRef.current && xtermRef.current) {
            return;
        }

        initRef.current = true;
        logger.info('Initializing preview terminal for session:', session.title);

        const terminalTheme = getTerminalTheme(theme);
        const terminalOptions = {
            ...BASE_TERMINAL_OPTIONS,
            fontSize: Math.max(10, fontSize - 2), // Slightly smaller for preview
            theme: terminalTheme,
        };

        const term = new XTerm(terminalOptions);
        const fitAddon = new FitAddon();

        term.loadAddon(fitAddon);
        term.open(terminalRef.current);
        fitAddon.fit();

        xtermRef.current = term;
        fitAddonRef.current = fitAddon;

        // Handle terminal data events (filtered by session ID)
        const handleTerminalHistory = (payload) => {
            if (payload?.sessionId !== previewSessionId) return;
            if (xtermRef.current && payload.data) {
                xtermRef.current.write(payload.data);
                xtermRef.current.scrollToBottom();
            }
        };
        EventsOn('terminal:history', handleTerminalHistory);

        const handleTerminalData = (payload) => {
            if (payload?.sessionId !== previewSessionId) return;
            if (xtermRef.current && payload.data) {
                xtermRef.current.write(payload.data);
            }
        };
        EventsOn('terminal:data', handleTerminalData);

        // Make preview interactive (allow scrolling)
        const handleWheel = (e) => {
            e.preventDefault();
            e.stopPropagation();
            if (xtermRef.current) {
                const linesToScroll = Math.sign(e.deltaY) * Math.max(1, Math.ceil(Math.abs(e.deltaY) / 30));
                xtermRef.current.scrollLines(linesToScroll);
            }
        };
        terminalRef.current?.addEventListener('wheel', handleWheel, { passive: false, capture: true });

        // Handle resize
        const resizeObserver = new ResizeObserver(() => {
            if (fitAddonRef.current && xtermRef.current) {
                fitAddonRef.current.fit();
                const { cols, rows } = xtermRef.current;
                ResizeTerminal(previewSessionId, cols, rows).catch(() => {});
            }
        });
        resizeObserver.observe(terminalRef.current);

        // Start terminal
        const { cols, rows } = term;

        const startPreviewTerminal = async () => {
            try {
                if (session.tmuxSession) {
                    if (session.isRemote && session.remoteHost) {
                        logger.info('Starting remote preview for:', session.remoteHost, session.tmuxSession);
                        await StartRemoteTmuxSession(previewSessionId, session.remoteHost, session.tmuxSession, cols, rows);
                    } else {
                        logger.info('Starting local preview for:', session.tmuxSession);
                        await StartTmuxSession(previewSessionId, session.tmuxSession, cols, rows);
                    }
                    logger.info('Preview terminal started');
                }
            } catch (err) {
                logger.error('Failed to start preview terminal:', err);
                if (xtermRef.current) {
                    xtermRef.current.write(`\x1b[31m[Preview unavailable]\x1b[0m\r\n`);
                }
            }
        };

        startPreviewTerminal();

        return () => {
            resizeObserver.disconnect();
            if (terminalRef.current) {
                terminalRef.current.removeEventListener('wheel', handleWheel);
            }
            // Note: We don't dispose here because the effect might re-run
            // Disposal happens on session change or unmount
        };
    }, [session?.id, theme, fontSize]);

    // Cleanup on unmount
    useEffect(() => {
        return () => {
            if (sessionIdRef.current) {
                CloseTerminal(`preview-${sessionIdRef.current}`).catch(err => {
                    logger.warn('Failed to close preview terminal on unmount:', err);
                });
            }
            if (xtermRef.current) {
                xtermRef.current.dispose();
                xtermRef.current = null;
                fitAddonRef.current = null;
            }
        };
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

    const getStatusLabel = (status) => {
        switch (status) {
            case 'running': return 'Running';
            case 'waiting': return 'Waiting';
            case 'idle': return 'Idle';
            case 'error': return 'Error';
            default: return status;
        }
    };

    if (!session) {
        return (
            <div className="session-preview-pane">
                <div className="session-preview-empty">
                    <div className="preview-empty-icon">👁</div>
                    <div className="preview-empty-text">Select a session to preview</div>
                    <div className="preview-empty-hint">Use ↑↓ to navigate, Enter to attach</div>
                </div>
            </div>
        );
    }

    const relativeTime = formatRelativeTime(session.lastAccessedAt);

    return (
        <div className="session-preview-pane">
            <div className="session-preview-header">
                <div className="preview-header-main">
                    <span
                        className="preview-tool-badge"
                        style={{ backgroundColor: getStatusColor(session.status) }}
                    >
                        <ToolIcon tool={session.tool} size={16} />
                    </span>
                    <div className="preview-title-section">
                        <h3 className="preview-title">
                            {session.dangerousMode && (
                                <span className="preview-danger-icon" title="Dangerous mode">!</span>
                            )}
                            {session.customLabel || session.title}
                        </h3>
                        <div className="preview-meta">
                            <span
                                className="preview-status"
                                style={{ color: getStatusColor(session.status) }}
                            >
                                {getStatusLabel(session.status)}
                            </span>
                            {session.isRemote && (
                                <span className="preview-remote-badge" title={`Remote: ${session.remoteHost}`}>
                                    SSH: {session.remoteHost}
                                </span>
                            )}
                            {(gitBranch || session.gitBranch) && (
                                <span className={`preview-branch${isWorktree || session.isWorktree ? ' worktree' : ''}`}>
                                    {isWorktree || session.isWorktree ? '🌿' : '⎇'} {gitBranch || session.gitBranch}
                                </span>
                            )}
                            {relativeTime && (
                                <span className="preview-time">🕐 {relativeTime}</span>
                            )}
                        </div>
                    </div>
                </div>
                <button
                    className="preview-attach-btn"
                    onClick={() => onAttach && onAttach(session)}
                    title="Attach to session (Enter)"
                >
                    Attach →
                </button>
            </div>

            <div className="session-preview-info">
                {session.projectPath && (
                    <div className="preview-info-row">
                        <span className="preview-info-label">Path:</span>
                        <span className="preview-info-value">{session.projectPath}</span>
                    </div>
                )}
                {session.launchConfigName && (
                    <div className="preview-info-row">
                        <span className="preview-info-label">Config:</span>
                        <span className="preview-info-value">{session.launchConfigName}</span>
                    </div>
                )}
                {session.gitDirty && (
                    <div className="preview-info-row">
                        <span className="preview-git-dirty">● Uncommitted changes</span>
                    </div>
                )}
                {(session.gitAhead > 0 || session.gitBehind > 0) && (
                    <div className="preview-info-row">
                        <span className="preview-git-sync">
                            {session.gitAhead > 0 && <span className="git-ahead">↑{session.gitAhead} ahead</span>}
                            {session.gitAhead > 0 && session.gitBehind > 0 && ' • '}
                            {session.gitBehind > 0 && <span className="git-behind">↓{session.gitBehind} behind</span>}
                        </span>
                    </div>
                )}
            </div>

            <div className="session-preview-terminal">
                <div
                    ref={terminalRef}
                    className="preview-terminal-container"
                />
            </div>

            <div className="session-preview-footer">
                <span className="preview-hint">Live preview • Scroll to explore • Press Enter to attach</span>
            </div>
        </div>
    );
}
