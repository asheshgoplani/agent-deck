import { useEffect, useRef, useState } from 'react';
import { Terminal as XTerm } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { SearchAddon } from '@xterm/addon-search';
import { WebLinksAddon } from '@xterm/addon-web-links';
import '@xterm/xterm/css/xterm.css';
import './Terminal.css';
import { StartTerminal, WriteTerminal, ResizeTerminal, CloseTerminal, StartTmuxSession } from '../wailsjs/go/main/App';
import { createLogger } from './logger';
import { EventsOn, EventsOff } from '../wailsjs/runtime/runtime';
import { useTheme } from './context/ThemeContext';
import { getTerminalTheme } from './themes/terminal';

// Base terminal options (theme applied dynamically)
const BASE_TERMINAL_OPTIONS = {
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
    const [showScrollIndicator, setShowScrollIndicator] = useState(false);
    const { theme } = useTheme();

    // Update terminal theme when app theme changes
    useEffect(() => {
        if (xtermRef.current) {
            const terminalTheme = getTerminalTheme(theme);
            xtermRef.current.options.theme = terminalTheme;
            logger.debug('Updated terminal theme to:', theme);
        }
    }, [theme]);

    // Initialize terminal
    useEffect(() => {
        // Prevent double initialization (React StrictMode)
        if (!terminalRef.current || initRef.current) return;
        initRef.current = true;

        logger.info('Initializing terminal', session ? `session: ${session.title}` : 'new terminal');

        // Get theme-specific terminal options
        const terminalTheme = getTerminalTheme(theme);
        const terminalOptions = {
            ...BASE_TERMINAL_OPTIONS,
            theme: terminalTheme,
        };

        const term = new XTerm(terminalOptions);
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

        // Handle data from terminal (user input) - send to PTY
        const dataDisposable = term.onData((data) => {
            WriteTerminal(data).catch(console.error);
        });

        // Track scroll position for scroll lock behavior
        const scrollDisposable = term.onScroll(() => {
            const viewport = term.buffer.active;
            const isAtBottom = viewport.baseY + term.rows >= viewport.length;
            isAtBottomRef.current = isAtBottom;
            setShowScrollIndicator(!isAtBottom);
        });

        // Listen for debug messages from backend
        const handleDebug = (msg) => {
            logger.debug('[Backend]', msg);
        };
        EventsOn('terminal:debug', handleDebug);

        // Handle pre-loaded history (Phase 1 of hybrid approach)
        const handleTerminalHistory = (history) => {
            logger.info('Received history:', history.length, 'bytes');
            if (xtermRef.current) {
                xtermRef.current.write(history);
                // Add visual separator so user can see where history ends
                xtermRef.current.write('\r\n\x1b[2m─── Live session below ───\x1b[0m\r\n');
            }
        };
        EventsOn('terminal:history', handleTerminalHistory);

        // Listen for data from PTY (Phase 2 - live streaming)
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
                    logger.info('Hybrid session started successfully');
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
            resizeObserver.disconnect();
            scrollDisposable.dispose();
            dataDisposable.dispose();
            term.dispose();
            xtermRef.current = null;
            fitAddonRef.current = null;
            searchAddonRef.current = null;
            initRef.current = false;
        };
    }, [searchRef, session]); // Note: theme changes handled by separate useEffect

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
