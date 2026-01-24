import { useEffect, useRef, useState } from 'react';
import { Terminal as XTerm } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { SearchAddon } from '@xterm/addon-search';
import { WebLinksAddon } from '@xterm/addon-web-links';
import { Unicode11Addon } from '@xterm/addon-unicode11';
// import { WebglAddon } from '@xterm/addon-webgl'; // Disabled - breaks scroll detection
import '@xterm/xterm/css/xterm.css';
import './Terminal.css';
import { StartTerminal, WriteTerminal, ResizeTerminal, CloseTerminal, StartTmuxSession, RefreshScrollback, LogFrontendDiagnostic } from '../wailsjs/go/main/App';
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
    // xterm.js v6 uses DOM renderer by default, with VS Code-based scrollbar
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
    const refreshingRef = useRef(false); // Flag to pause PTY data during refresh
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
        const unicode11Addon = new Unicode11Addon();

        term.loadAddon(fitAddon);
        term.loadAddon(searchAddon);
        term.loadAddon(webLinksAddon);
        term.loadAddon(unicode11Addon);

        term.open(terminalRef.current);

        // Using DOM renderer (xterm.js v6 default)
        // Note: WebGL addon breaks scroll detection in WKWebView
        logger.info('xterm.js v6 initialized with DOM renderer');
        console.log('%c[RENDERER] Using DOM renderer', 'color: lime; font-weight: bold');
        LogFrontendDiagnostic('[RENDERER] Using DOM renderer');

        // Unicode 11 disabled for testing - may be related to rendering corruption
        // term.unicode.activeVersion = '11';

        // Initial fit
        fitAddon.fit();

        // Store refs
        xtermRef.current = term;
        fitAddonRef.current = fitAddon;
        searchAddonRef.current = searchAddon;

        // Expose for debugging (remove in production)
        window._xterm = term;

        // Expose search addon via ref
        if (searchRef) {
            searchRef.current = searchAddon;
        }

        // Handle data from terminal (user input) - send to PTY
        const dataDisposable = term.onData((data) => {
            WriteTerminal(data).catch(console.error);
        });

        // ============================================================
        // SCROLL DETECTION & REPAIR SYSTEM
        // ============================================================
        // Problem: User scroll events don't fire in WKWebView
        // Solution: RAF polling to detect viewportY changes
        // ============================================================

        let sessionLoadComplete = false;
        let rafId = null;
        let scrollSettleTimer = null;
        let lastViewportY = -1; // Will be set after load completes
        let lastBaseY = -1;

        // Mark session load as complete - called after initial scrollback is written
        const markSessionLoadComplete = () => {
            sessionLoadComplete = true;
            const viewport = term.buffer.active;
            lastViewportY = viewport.viewportY;
            lastBaseY = viewport.baseY;

            // Diagnostic: inspect viewport element
            const viewportEl = terminalRef.current?.querySelector('.xterm-viewport');
            if (viewportEl) {
                const style = window.getComputedStyle(viewportEl);
                console.log(`%c[DIAG] .xterm-viewport: scrollTop=${viewportEl.scrollTop} scrollHeight=${viewportEl.scrollHeight} clientHeight=${viewportEl.clientHeight}`, 'color: magenta');
                console.log(`%c[DIAG] overflow: ${style.overflow} overflowY: ${style.overflowY}`, 'color: magenta');
                LogFrontendDiagnostic(`[DIAG] viewport: scrollTop=${viewportEl.scrollTop} scrollHeight=${viewportEl.scrollHeight} clientHeight=${viewportEl.clientHeight} overflow=${style.overflowY}`);

                // Try adding a pointerdown listener to see if pointer events work
                viewportEl.addEventListener('pointerdown', () => {
                    console.log('%c[EVENT] pointerdown on viewport!', 'color: red; font-weight: bold');
                    LogFrontendDiagnostic('[EVENT] pointerdown on viewport');
                });
            }

            console.log('%c========== SESSION LOAD COMPLETE ==========', 'color: lime; font-weight: bold; font-size: 14px');
            console.log(`%c[LOAD-DONE] viewportY=${lastViewportY} baseY=${lastBaseY} length=${viewport.length}`, 'color: lime');
            LogFrontendDiagnostic(`========== SESSION LOAD COMPLETE: viewportY=${lastViewportY} baseY=${lastBaseY} ==========`);
        };

        // Expose for calling after scrollback load
        term._markSessionLoadComplete = markSessionLoadComplete;

        const handleScrollFix = (source) => {
            const viewport = term.buffer.active;
            const isAtBottom = viewport.baseY + term.rows >= viewport.length;
            isAtBottomRef.current = isAtBottom;
            setShowScrollIndicator(!isAtBottom);
        };

        // Try multiple scroll detection methods for xterm.js v6
        // (scrollSettleTimer declared above)

        // Method 1: xterm.js onScroll API - triggers scroll indicator update
        const scrollDisposable = term.onScroll(() => {
            if (scrollSettleTimer) clearTimeout(scrollSettleTimer);
            scrollSettleTimer = setTimeout(() => {
                attemptRepaint();
            }, 200);
        });

        // Method 2: Direct DOM scroll listener on viewport
        const viewportEl = terminalRef.current?.querySelector('.xterm-viewport');
        const handleDOMScroll = () => {
            console.log(`%c[DOM-SCROLL] scrollTop=${viewportEl?.scrollTop}`, 'color: cyan; font-weight: bold');
            LogFrontendDiagnostic(`[DOM-SCROLL] scrollTop=${viewportEl?.scrollTop}`);
        };
        if (viewportEl) {
            viewportEl.addEventListener('scroll', handleDOMScroll, { passive: true });
            console.log('%c[INIT] DOM scroll listener attached', 'color: green');
        }

        const attemptRepaint = () => {
            if (!xtermRef.current) return;

            const term = xtermRef.current;

            // Update scroll indicator state
            handleScrollFix('scroll-settle');

            // Standard xterm.js refresh - keep display in sync
            term.refresh(0, term.rows - 1);
        };

        // INTERCEPT wheel events and use programmatic scroll instead
        // Key insight: Scrollbar drag renders correctly, wheel scroll corrupts
        // By intercepting wheel and using scrollLines(), we use the "good" rendering path
        const handleWheel = (e) => {
            // PREVENT default wheel behavior - we'll scroll programmatically
            e.preventDefault();
            e.stopPropagation();

            if (!xtermRef.current) return;

            // Calculate lines to scroll based on deltaY
            // Typical wheel deltaY is ~100 for one "notch", we want ~3 lines per notch
            const linesToScroll = Math.sign(e.deltaY) * Math.max(1, Math.ceil(Math.abs(e.deltaY) / 30));

            // Use xterm's programmatic scroll - this should use same path as scrollbar
            xtermRef.current.scrollLines(linesToScroll);
        };

        // Use capture phase and NOT passive (so we can preventDefault)
        terminalRef.current?.addEventListener('wheel', handleWheel, { passive: false, capture: true });
        console.log('%c[INIT] Wheel interception enabled - using programmatic scrollLines()', 'color: lime; font-weight: bold');
        LogFrontendDiagnostic('[INIT] Wheel interception enabled');

        console.log('%c[INIT] All scroll detection methods initialized', 'color: green; font-weight: bold');
        LogFrontendDiagnostic('[INIT] Scroll detection initialized');

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
                        console.log('%c[LOAD] Starting scrollback refresh...', 'color: cyan; font-weight: bold');
                        LogFrontendDiagnostic('[LOAD] Starting scrollback refresh');
                        try {
                            refreshingRef.current = true; // Pause PTY data writes
                            const scrollback = await RefreshScrollback();
                            if (scrollback && xtermRef.current) {
                                xtermRef.current.clear();
                                xtermRef.current.write(scrollback);
                                xtermRef.current.scrollToBottom();
                                logger.info('Initial scrollback loaded:', scrollback.length, 'bytes');
                                console.log(`%c[LOAD] Scrollback written: ${scrollback.length} bytes`, 'color: cyan; font-weight: bold');
                                LogFrontendDiagnostic(`[LOAD] Scrollback written: ${scrollback.length} bytes`);

                                // Mark session load complete so RAF polling can start tracking user scrolls
                                // Small delay to let xterm.js finish rendering
                                setTimeout(() => {
                                    if (xtermRef.current?._markSessionLoadComplete) {
                                        xtermRef.current._markSessionLoadComplete();
                                    }
                                }, 100);
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
            // Clean up scroll event listeners
            if (viewportEl) viewportEl.removeEventListener('scroll', handleDOMScroll);
            if (terminalRef.current) terminalRef.current.removeEventListener('wheel', handleWheel);
            if (scrollSettleTimer) clearTimeout(scrollSettleTimer);
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
