import { useEffect, useRef } from 'react';
import { Terminal as XTerm } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { SearchAddon } from '@xterm/addon-search';
import { WebLinksAddon } from '@xterm/addon-web-links';
import '@xterm/xterm/css/xterm.css';
import './Terminal.css';
import { StartTerminal, WriteTerminal, ResizeTerminal, GetScrollback, CloseTerminal, StartTmuxPolling, SendTmuxInput, ResizeTmuxPane } from '../wailsjs/go/main/App';
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
    // Rendering options to reduce artifacts
    smoothScrollDuration: 0,
    fastScrollModifier: 'alt',
    // Window mode affects wrapping behavior
    windowsMode: false, // Unix-style wrapping (default)
    // Note: Terminal content doesn't reflow on resize (standard behavior)
    // Already-printed output stays wrapped at original width
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
    const isTmuxPollingRef = useRef(false);

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

        term.loadAddon(fitAddon);
        term.loadAddon(searchAddon);
        term.loadAddon(webLinksAddon);

        term.open(terminalRef.current);

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

        // Handle data from terminal (user input) - send to PTY or tmux
        const dataDisposable = term.onData((data) => {
            if (isTmuxPollingRef.current) {
                SendTmuxInput(data).catch(console.error);
            } else {
                WriteTerminal(data).catch(console.error);
            }
        });

        // Listen for debug messages from backend
        const handleDebug = (msg) => {
            logger.debug('[Backend]', msg);
        };
        EventsOn('terminal:debug', handleDebug);

        // Listen for data from PTY
        const handleTerminalData = (data) => {
            // Log data characteristics for debugging
            const hasClr = data.includes('\x1b[2J');
            const hasHome = data.includes('\x1b[H');
            const lines = data.split('\n').length;
            logger.debug(`[DATA] received: ${data.length} bytes, ${lines} lines, clearScreen=${hasClr}, home=${hasHome}`);

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

        // Debounced scrollback refresh - fires after resize activity stops
        // This reloads scrollback from tmux at the new width so it reflows properly
        let scrollbackRefreshTimer = null;
        const SCROLLBACK_REFRESH_DELAY = 400; // ms after resize stops

        const refreshScrollbackAfterResize = async () => {
            if (!isTmuxPollingRef.current || !session?.tmuxSession || !xtermRef.current) {
                return;
            }

            logger.info('Refreshing scrollback after resize...');
            try {
                // Clear xterm buffer completely (visible + scrollback)
                // This is necessary because old scrollback was rendered at old width
                xtermRef.current.clear();

                // Re-fetch scrollback from tmux (now reflowed to new width)
                const scrollback = await GetScrollback(session.tmuxSession);
                if (scrollback && xtermRef.current) {
                    const scrollbackLines = scrollback.split('\n').length;
                    logger.debug(`[RESIZE-REFRESH] Got ${scrollback.length} bytes, ${scrollbackLines} lines`);

                    // Write reflowed scrollback to buffer
                    xtermRef.current.write(scrollback);

                    // Add separator
                    xtermRef.current.write('\r\n\x1b[2m─── History above, live session below ───\x1b[0m\r\n');

                    logger.info('Scrollback refreshed at new width');
                }
            } catch (err) {
                logger.warn('Failed to refresh scrollback:', err);
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

                        // Send resize to PTY or tmux pane
                        const resizeFn = isTmuxPollingRef.current ? ResizeTmuxPane : ResizeTerminal;
                        resizeFn(cols, rows)
                            .then(() => {
                                // After resize, refresh terminal display
                                // This helps clear artifacts from stale content
                                if (xtermRef.current) {
                                    xtermRef.current.refresh(0, rows - 1);
                                }
                            })
                            .catch(console.error);

                        // For tmux polling mode, debounce scrollback refresh
                        // This reloads scrollback at new width after resize settles
                        if (isTmuxPollingRef.current) {
                            if (scrollbackRefreshTimer) {
                                clearTimeout(scrollbackRefreshTimer);
                            }
                            scrollbackRefreshTimer = setTimeout(refreshScrollbackAfterResize, SCROLLBACK_REFRESH_DELAY);
                        }
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
                    logger.info('Connecting to tmux session:', session.tmuxSession);

                    // Load scrollback into xterm buffer so Cmd+F search works on history
                    try {
                        logger.info('Loading scrollback for search...');
                        const scrollback = await GetScrollback(session.tmuxSession);
                        if (scrollback) {
                            const scrollbackLines = scrollback.split('\n').length;
                            logger.debug(`[SCROLLBACK] Got ${scrollback.length} bytes, ${scrollbackLines} lines`);

                            // Write scrollback directly to buffer
                            // Note: GetScrollback now returns CRLF line endings for proper xterm rendering
                            term.write(scrollback);
                            logger.info('Scrollback loaded:', scrollback.length, 'chars,', scrollbackLines, 'lines');

                            // Add visual separator so user can see where history ends
                            term.write('\r\n\x1b[2m─── History above, live session below ───\x1b[0m\r\n');

                            // Log terminal state after scrollback
                            logger.debug(`[SCROLLBACK] Terminal buffer after load: cols=${term.cols}, rows=${term.rows}`);
                        }
                    } catch (scrollErr) {
                        logger.warn('Failed to load scrollback:', scrollErr);
                    }

                    // Wait for xterm to finish rendering scrollback
                    logger.debug('[TIMING] Waiting 100ms for xterm to render scrollback...');
                    await new Promise(resolve => setTimeout(resolve, 100));

                    // Log terminal state before polling
                    logger.debug(`[TIMING] Starting polling. xterm: cols=${term.cols}, rows=${term.rows}`);

                    // Start polling instead of attaching
                    // Polling avoids cursor position conflicts - it overwrites the visible
                    // screen each update using line-by-line rendering (no full screen clear)
                    logger.info('Starting tmux polling...');
                    isTmuxPollingRef.current = true;
                    await StartTmuxPolling(session.tmuxSession, cols, rows);
                    logger.info('Polling started successfully');
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

            // Cancel any pending scrollback refresh
            if (scrollbackRefreshTimer) {
                clearTimeout(scrollbackRefreshTimer);
            }

            // Close the PTY/polling backend
            CloseTerminal().catch((err) => {
                logger.error('Failed to close terminal:', err);
            });

            // Clean up frontend
            EventsOff('terminal:debug');
            EventsOff('terminal:data');
            EventsOff('terminal:exit');
            resizeObserver.disconnect();
            dataDisposable.dispose();
            term.dispose();
            xtermRef.current = null;
            fitAddonRef.current = null;
            searchAddonRef.current = null;
            initRef.current = false;
            isTmuxPollingRef.current = false;
        };
    }, [searchRef, session]);

    return (
        <div
            ref={terminalRef}
            data-testid="terminal"
            style={{
                width: '100%',
                height: '100%',
                backgroundColor: '#1a1a2e',
            }}
        />
    );
}
