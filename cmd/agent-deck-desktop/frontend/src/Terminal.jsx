import { useEffect, useRef, useState } from 'react';
import { Terminal as XTerm } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { SearchAddon } from '@xterm/addon-search';
import { WebLinksAddon } from '@xterm/addon-web-links';
import { Unicode11Addon } from '@xterm/addon-unicode11';
import '@xterm/xterm/css/xterm.css';
import './Terminal.css';
import { StartTerminal, WriteTerminal, ResizeTerminal, CloseTerminal, StartTmuxSession, RefreshScrollback } from '../wailsjs/go/main/App';
import { createLogger } from './logger';
import { EventsOn, EventsOff } from '../wailsjs/runtime/runtime';

const TERMINAL_OPTIONS = {
    fontFamily: '"MesloLGS NF", Menlo, Monaco, "Courier New", monospace',
    fontSize: 14,
    lineHeight: 1.2,
    cursorBlink: true,
    cursorStyle: 'block',
    scrollback: 10000,
    allowProposedApi: true,
    // Use DOM renderer to fix Canvas batching bug with repeated box-drawing chars
    // Canvas renderer corrupts long ───── sequences during scroll/repaint
    // DOM renderer is slightly slower but rock-solid for rendering accuracy
    rendererType: 'dom',
    // Rendering options
    smoothScrollDuration: 0,
    fastScrollModifier: 'alt',
    windowsMode: false,
    theme: {
        background: '#1a1a2e',
        foreground: '#eee',
        cursor: '#4cc9f0',
        cursorAccent: '#1a1a2e',
        selectionBackground: 'rgba(76, 201, 240, 0.3)',
        black: '#1a1a2e',
        red: '#ff6b6b',
        green: '#4ecdc4',
        yellow: '#ffe66d',
        blue: '#4cc9f0',
        magenta: '#f72585',
        cyan: '#7b8cde',
        white: '#eee',
        brightBlack: '#6c757d',
        brightRed: '#ff8787',
        brightGreen: '#69d9d0',
        brightYellow: '#fff3a3',
        brightBlue: '#72d4f7',
        brightMagenta: '#f85ca2',
        brightCyan: '#9ba8e8',
        brightWhite: '#fff',
    },
};

// requestAnimationFrame throttle - fires at most once per frame
function rafThrottle(fn) {
    let rafId = null;
    return (...args) => {
        if (rafId) cancelAnimationFrame(rafId);
        rafId = requestAnimationFrame(() => {
            rafId = null;
            fn(...args);
        });
    };
}

const logger = createLogger('Terminal');

export default function Terminal({ searchRef, session }) {
    const terminalRef = useRef(null);
    const xtermRef = useRef(null);
    const fitAddonRef = useRef(null);
    const searchAddonRef = useRef(null);
    const initRef = useRef(false);
    const isAtBottomRef = useRef(true);
    const refreshingRef = useRef(false); // Flag to pause PTY data during refresh
    const [showScrollIndicator, setShowScrollIndicator] = useState(false);

    // Initialize terminal
    useEffect(() => {
        // Prevent double initialization (React StrictMode)
        if (!terminalRef.current || initRef.current) return;
        initRef.current = true;

        logger.info('Initializing terminal', session ? `session: ${session.title}` : 'new terminal');

        const term = new XTerm(TERMINAL_OPTIONS);
        const fitAddon = new FitAddon();
        const searchAddon = new SearchAddon();
        const webLinksAddon = new WebLinksAddon();
        const unicode11Addon = new Unicode11Addon();

        term.loadAddon(fitAddon);
        term.loadAddon(searchAddon);
        term.loadAddon(webLinksAddon);
        term.loadAddon(unicode11Addon);

        term.open(terminalRef.current);

        // Activate Unicode 11 for proper character width handling
        // This fixes rendering issues with box-drawing chars, Braille, emoji during resize
        term.unicode.activeVersion = '11';

        // Initial fit
        fitAddon.fit();

        // Store refs
        xtermRef.current = term;
        fitAddonRef.current = fitAddon;
        searchAddonRef.current = searchAddon;

        // Expose search addon via ref
        if (searchRef) {
            searchRef.current = searchAddon;
        }

        // Handle data from terminal (user input) - send to PTY
        const dataDisposable = term.onData((data) => {
            WriteTerminal(data).catch(console.error);
        });

        // Track scroll position for scroll lock behavior
        // Also detect when user scrolls into scrollback and refresh to fix reflow issues
        let scrollRefreshTimer = null;
        let wasAtBottom = true;
        const scrollDisposable = term.onScroll(() => {
            const viewport = term.buffer.active;
            const isAtBottom = viewport.baseY + term.rows >= viewport.length;
            isAtBottomRef.current = isAtBottom;
            setShowScrollIndicator(!isAtBottom);

            // Refresh viewport after scroll settles to fix xterm.js rendering glitches
            if (scrollRefreshTimer) clearTimeout(scrollRefreshTimer);
            scrollRefreshTimer = setTimeout(() => {
                if (xtermRef.current) {
                    // Refresh just the visible viewport (not full scrollback reload)
                    xtermRef.current.refresh(0, xtermRef.current.rows - 1);
                }
            }, 100);
            wasAtBottom = isAtBottom;
        });

        // Listen for debug messages from backend
        const handleDebug = (msg) => {
            logger.debug('[Backend]', msg);
        };
        EventsOn('terminal:debug', handleDebug);

        // Handle pre-loaded history (Phase 1 of hybrid approach)
        // NOTE: We ignore this event now - using RefreshScrollback after PTY settles instead
        // This avoids race conditions between history write and PTY data
        const handleTerminalHistory = (history) => {
            logger.debug('Ignoring terminal:history event (', history.length, 'bytes) - using RefreshScrollback instead');
        };
        EventsOn('terminal:history', handleTerminalHistory);

        // Listen for data from PTY (Phase 2 - live streaming)
        const handleTerminalData = (data) => {
            // Skip writes during refresh to prevent corruption
            if (refreshingRef.current) {
                logger.debug('[DATA] Skipped during refresh:', data.length, 'bytes');
                return;
            }

            if (xtermRef.current) {
                xtermRef.current.write(data);
            }
        };
        EventsOn('terminal:data', handleTerminalData);

        // Listen for terminal exit
        const handleTerminalExit = (reason) => {
            if (xtermRef.current) {
                xtermRef.current.write(`\r\n\x1b[31m[Terminal exited: ${reason}]\x1b[0m\r\n`);
            }
        };
        EventsOn('terminal:exit', handleTerminalExit);

        // Track last sent dimensions to avoid duplicate calls
        let lastCols = term.cols;
        let lastRows = term.rows;

        // Debounced scrollback refresh - fixes xterm.js reflow issues with box-drawing chars
        // Only triggers after resize settles (no resize events for 400ms)
        let scrollbackRefreshTimer = null;
        const refreshScrollbackAfterResize = async () => {
            if (!session?.tmuxSession || !xtermRef.current) {
                logger.debug('[RESIZE-REFRESH] Skipped - no session or xterm');
                return;
            }

            try {
                refreshingRef.current = true; // Pause PTY data writes
                logger.info('[RESIZE-REFRESH] Fetching fresh scrollback from tmux...');
                const scrollback = await RefreshScrollback();

                if (scrollback && xtermRef.current) {
                    // Clear buffer and rewrite with fresh content from tmux
                    xtermRef.current.clear();
                    xtermRef.current.write(scrollback);
                    logger.info('[RESIZE-REFRESH] Done:', scrollback.length, 'bytes');
                } else {
                    logger.warn('[RESIZE-REFRESH] No scrollback returned or xterm gone');
                }
            } catch (err) {
                logger.error('[RESIZE-REFRESH] Failed:', err);
            } finally {
                refreshingRef.current = false; // Resume PTY data writes
            }
        };

        // RAF-throttled resize handler - fires at most once per frame
        const handleResize = rafThrottle(() => {
            if (fitAddonRef.current && xtermRef.current) {
                try {
                    // Fit terminal to container
                    fitAddonRef.current.fit();

                    const { cols, rows } = xtermRef.current;

                    // Only send resize if dimensions actually changed
                    if (cols !== lastCols || rows !== lastRows) {
                        lastCols = cols;
                        lastRows = rows;

                        // Send resize to PTY (handles tmux resize internally)
                        ResizeTerminal(cols, rows)
                            .then(() => {
                                // After resize, refresh terminal display
                                // This helps clear artifacts from stale content
                                if (xtermRef.current) {
                                    xtermRef.current.refresh(0, rows - 1);
                                }
                            })
                            .catch(console.error);

                        // Schedule debounced scrollback refresh (fixes box-drawing char reflow)
                        if (scrollbackRefreshTimer) {
                            clearTimeout(scrollbackRefreshTimer);
                        }
                        scrollbackRefreshTimer = setTimeout(refreshScrollbackAfterResize, 400);
                    }
                } catch (e) {
                    console.error('Resize error:', e);
                }
            }
        });

        // Handle window resize with ResizeObserver
        const resizeObserver = new ResizeObserver(handleResize);
        resizeObserver.observe(terminalRef.current);

        // Start the terminal
        const { cols, rows } = term;

        const startTerminal = async () => {
            try {
                if (session && session.tmuxSession) {
                    logger.info('Connecting to tmux session (hybrid mode):', session.tmuxSession);
                    // Backend handles history fetch + PTY attach in one call
                    await StartTmuxSession(session.tmuxSession, cols, rows);
                    logger.info('Hybrid session started, waiting for PTY to settle...');

                    // Wait for PTY to settle, then refresh scrollback
                    // This ensures clean content without race conditions
                    setTimeout(async () => {
                        logger.info('Refreshing scrollback after PTY settle...');
                        try {
                            refreshingRef.current = true; // Pause PTY data writes
                            const scrollback = await RefreshScrollback();
                            if (scrollback && xtermRef.current) {
                                xtermRef.current.clear();
                                xtermRef.current.write(scrollback);
                                xtermRef.current.scrollToBottom();
                                logger.info('Initial scrollback loaded:', scrollback.length, 'bytes');
                            }
                        } catch (err) {
                            logger.error('Failed to refresh initial scrollback:', err);
                        } finally {
                            refreshingRef.current = false; // Resume PTY data writes
                        }
                    }, 300);
                } else {
                    logger.info('Starting new terminal');
                    await StartTerminal(cols, rows);
                    logger.info('Terminal started');
                }
            } catch (err) {
                logger.error('Failed to start terminal:', err);
                term.write(`\x1b[31mFailed to start terminal: ${err}\x1b[0m\r\n`);
            }
        };

        startTerminal();

        // Focus terminal
        term.focus();

        return () => {
            logger.info('Cleaning up terminal');

            // Close the PTY backend
            CloseTerminal().catch((err) => {
                logger.error('Failed to close terminal:', err);
            });

            // Clean up frontend
            EventsOff('terminal:debug');
            EventsOff('terminal:history');
            EventsOff('terminal:data');
            EventsOff('terminal:exit');
            if (scrollbackRefreshTimer) {
                clearTimeout(scrollbackRefreshTimer);
            }
            resizeObserver.disconnect();
            scrollDisposable.dispose();
            dataDisposable.dispose();
            term.dispose();
            xtermRef.current = null;
            fitAddonRef.current = null;
            searchAddonRef.current = null;
            initRef.current = false;
        };
    }, [searchRef, session]);

    // Scroll to bottom when indicator is clicked
    const handleScrollToBottom = () => {
        if (xtermRef.current) {
            xtermRef.current.scrollToBottom();
            isAtBottomRef.current = true;
            setShowScrollIndicator(false);
        }
    };

    return (
        <div style={{ position: 'relative', width: '100%', height: '100%' }}>
            <div
                ref={terminalRef}
                data-testid="terminal"
                style={{
                    width: '100%',
                    height: '100%',
                    backgroundColor: '#1a1a2e',
                }}
            />
            {showScrollIndicator && (
                <button
                    className="scroll-indicator"
                    onClick={handleScrollToBottom}
                    title="Scroll to bottom"
                >
                    New output ↓
                </button>
            )}
        </div>
    );
}
