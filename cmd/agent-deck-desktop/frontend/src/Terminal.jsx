import { useEffect, useRef, useState } from 'react';
import { Terminal as XTerm } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { SearchAddon } from '@xterm/addon-search';
import { WebLinksAddon } from '@xterm/addon-web-links';
import { Unicode11Addon } from '@xterm/addon-unicode11';
// import { WebglAddon } from '@xterm/addon-webgl'; // Disabled - breaks scroll detection
import '@xterm/xterm/css/xterm.css';
import './Terminal.css';
import { StartTerminal, WriteTerminal, ResizeTerminal, CloseTerminal, StartTmuxSession, StartRemoteTmuxSession, LogFrontendDiagnostic, GetTerminalSettings } from '../wailsjs/go/main/App';
import { createLogger } from './logger';
import { DEFAULT_FONT_SIZE } from './constants/terminal';
import { EventsOn, EventsOff } from '../wailsjs/runtime/runtime';
import { useTheme } from './context/ThemeContext';
import { getTerminalTheme } from './themes/terminal';

// Base terminal options (theme applied dynamically)
const BASE_TERMINAL_OPTIONS = {
    fontFamily: '"MesloLGS NF", Menlo, Monaco, "Courier New", monospace',
    fontSize: DEFAULT_FONT_SIZE,
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

export default function Terminal({ searchRef, session, paneId, onFocus, fontSize = DEFAULT_FONT_SIZE }) {
    const terminalRef = useRef(null);
    const xtermRef = useRef(null);
    const fitAddonRef = useRef(null);
    const searchAddonRef = useRef(null);
    const initRef = useRef(false);
    const isAtBottomRef = useRef(true);
    const [showScrollIndicator, setShowScrollIndicator] = useState(false);
    const [isAltScreen, setIsAltScreen] = useState(false);
    const { theme } = useTheme();

    // Update terminal theme when app theme changes
    useEffect(() => {
        if (xtermRef.current) {
            const terminalTheme = getTerminalTheme(theme);
            xtermRef.current.options.theme = terminalTheme;
            logger.debug('Updated terminal theme to:', theme);
        }
    }, [theme]);

    // Update terminal font size when it changes
    useEffect(() => {
        if (xtermRef.current && fitAddonRef.current) {
            xtermRef.current.options.fontSize = fontSize;
            fitAddonRef.current.fit();
            logger.debug('Updated terminal font size to:', fontSize);
        }
    }, [fontSize]);

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
            fontSize,
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

        // Get the session ID for this terminal (used for multi-pane support)
        const sessionId = session?.id || 'default';

        // ============================================================
        // ALT-SCREEN TRACKING (from backend via tmux)
        // ============================================================
        // The backend polls tmux for #{alternate_on} and emits terminal:altscreen
        // when apps like nano/vim/less switch to/from alternate screen buffer.
        // We use this to decide whether to send Page Up/Down for scrolling.
        // ============================================================
        let isInAltScreen = false;

        const handleAltScreenChange = (payload) => {
            if (payload?.sessionId !== sessionId) return;
            isInAltScreen = payload.inAltScreen;
            setIsAltScreen(payload.inAltScreen); // Update state for CSS class
            console.log(`%c[ALT-SCREEN] Changed to: ${isInAltScreen}`, 'color: magenta; font-weight: bold');
            LogFrontendDiagnostic(`[ALT-SCREEN] Changed to: ${isInAltScreen}`);
        };
        EventsOn('terminal:altscreen', handleAltScreenChange);

        // ============================================================
        // MOUSE MODE TRACKING (parser hooks - may not work in polling mode)
        // ============================================================
        // Track which mouse modes the backend application has enabled.
        // NOTE: In polling mode, these sequences may be stripped, so
        // alt-screen tracking above is more reliable.
        // ============================================================
        const mouseModes = new Set();

        // Monitor mouse mode enable sequences (CSI ? ... h)
        const enableHandler = term.parser.registerCsiHandler({ prefix: '?', final: 'h' }, (params) => {
            for (const p of params) {
                if ([1000, 1002, 1003, 1006].includes(p)) {
                    mouseModes.add(p);
                    console.log(`%c[MOUSE] Enabled mode ${p}. Active modes:`, 'color: cyan', [...mouseModes]);
                    LogFrontendDiagnostic(`[MOUSE] Enabled mode ${p}. Active: ${[...mouseModes].join(',')}`);
                }
            }
            return false; // Allow xterm to process it too
        });

        // Monitor mouse mode disable sequences (CSI ? ... l)
        const disableHandler = term.parser.registerCsiHandler({ prefix: '?', final: 'l' }, (params) => {
            for (const p of params) {
                if (mouseModes.delete(p)) {
                    console.log(`%c[MOUSE] Disabled mode ${p}. Active modes:`, 'color: orange', [...mouseModes]);
                    LogFrontendDiagnostic(`[MOUSE] Disabled mode ${p}. Active: ${[...mouseModes].join(',')}`);
                }
            }
            return false;
        });

        // Handle data from terminal (user input) - send to PTY
        const dataDisposable = term.onData((data) => {
            WriteTerminal(sessionId, data).catch(console.error);
            // Notify parent that this pane received input (for focus tracking)
            if (onFocus) {
                onFocus();
            }
        });

        // ============================================================
        // SOFT NEWLINE SUPPORT (multiline input without execute)
        // ============================================================
        // Claude Code and other AI tools support Shift+Enter for inserting
        // a newline without executing the command. We send ESC + CR (\x1b\r)
        // which is interpreted as "insert newline" rather than "execute".
        //
        // The user can configure this via Settings:
        // - "shift_enter": Only Shift+Enter triggers soft newline
        // - "alt_enter": Only Alt/Option+Enter triggers soft newline
        // - "both": Both Shift+Enter and Alt+Enter work (default)
        // - "disabled": Disable soft newline (use backslash continuation)
        //
        // Note: Backslash continuation (typing \ at end of line then Enter)
        // is always supported natively by Claude Code - no special handling needed.
        // ============================================================

        // Use ref to allow settings to be updated without recreating handler
        const softNewlineModeRef = { current: 'both' }; // Default to both

        // Load settings asynchronously and update the ref
        GetTerminalSettings()
            .then(settings => {
                softNewlineModeRef.current = settings?.softNewline || 'both';
                logger.info('Loaded soft newline mode:', softNewlineModeRef.current);
            })
            .catch(err => {
                logger.warn('Failed to load terminal settings, using defaults:', err);
            });

        const customKeyHandler = term.attachCustomKeyEventHandler((e) => {
            // Only handle keydown events (not keyup)
            if (e.type !== 'keydown') {
                return true; // Let xterm handle it
            }

            // Check if this key combo should trigger soft newline
            if (e.key === 'Enter') {
                const mode = softNewlineModeRef.current;

                // Determine if we should intercept based on current mode
                let shouldIntercept = false;
                if (mode === 'disabled') {
                    shouldIntercept = false;
                } else if (mode === 'shift_enter' && e.shiftKey && !e.altKey) {
                    shouldIntercept = true;
                } else if (mode === 'alt_enter' && e.altKey && !e.shiftKey) {
                    shouldIntercept = true;
                } else if (mode === 'both' && (e.shiftKey || e.altKey)) {
                    shouldIntercept = true;
                }

                if (shouldIntercept) {
                    e.preventDefault();
                    // Send ESC + CR sequence which Claude Code interprets as soft newline
                    WriteTerminal(sessionId, '\x1b\r').catch(console.error);
                    return false; // Prevent xterm from handling Enter
                }
            }

            // Let xterm.js handle all other keys normally
            return true;
        });
        logger.info('Soft newline handler attached (default mode: both)');

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
        // In alt-screen mode, intercept scrollbar and send Page Up/Down to app
        const viewportEl = terminalRef.current?.querySelector('.xterm-viewport');
        let lastScrollTop = 0;
        let scrollbarThrottleTime = 0;
        const SCROLLBAR_THROTTLE_MS = 400;

        const handleDOMScroll = () => {
            if (!viewportEl || !xtermRef.current) return;

            const currentScrollTop = viewportEl.scrollTop;
            const scrollingUp = currentScrollTop < lastScrollTop;
            lastScrollTop = currentScrollTop;

            // In alt-screen mode, intercept scrollbar and send commands to app
            if (isInAltScreen) {
                const now = Date.now();
                if (now - scrollbarThrottleTime < SCROLLBAR_THROTTLE_MS) {
                    // Reset scroll position to prevent visual movement
                    xtermRef.current.scrollToBottom();
                    return;
                }
                scrollbarThrottleTime = now;

                // Send Page Up/Down based on scroll direction
                const pageSeq = scrollingUp ? '\x1b[5~' : '\x1b[6~';
                console.log(`%c[SCROLLBAR] Alt: Page ${scrollingUp ? 'Up' : 'Down'}`, 'color: cyan');
                WriteTerminal(sessionId, pageSeq).catch(console.error);

                // Reset scroll position
                xtermRef.current.scrollToBottom();
                return;
            }
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

        // ============================================================
        // WHEEL EVENT HANDLER FOR ALTERNATE BUFFER APPS
        // ============================================================
        // Since we use polling mode (tmux capture-pane), mouse mode escape
        // sequences from apps go to tmux, not xterm.js. We can't track them.
        //
        // Strategy:
        // - Alt buffer (nano, micro, vim, less): Send throttled Page Up/Down
        // - Normal buffer (shell): Use programmatic xterm scroll
        //
        // We throttle alt-buffer scrolling aggressively to prevent rapid page jumps.
        // ============================================================
        let lastAltScrollTime = 0;
        let pendingDirection = null; // 'up' or 'down'
        const ALT_SCROLL_THROTTLE_MS = 400; // One page scroll per 400ms max

        const handleWheel = (e) => {
            // Always prevent default to stop browser from scrolling the viewport
            e.preventDefault();
            e.stopPropagation();

            if (!xtermRef.current) return;

            const term = xtermRef.current;
            const now = Date.now();

            // Alt buffer (nano, micro, vim, less, htop, etc.)
            if (isInAltScreen) {
                const scrollUp = e.deltaY < 0;
                pendingDirection = scrollUp ? 'up' : 'down';

                // Strict throttle: only send if enough time has passed
                if (now - lastAltScrollTime < ALT_SCROLL_THROTTLE_MS) {
                    return; // Swallow event, direction is saved
                }

                lastAltScrollTime = now;

                // Page Up = \x1b[5~, Page Down = \x1b[6~
                const pageSeq = pendingDirection === 'up' ? '\x1b[5~' : '\x1b[6~';
                pendingDirection = null;

                console.log(`%c[WHEEL] Alt: Page ${scrollUp ? 'Up' : 'Down'} (throttled)`, 'color: lime');
                WriteTerminal(sessionId, pageSeq).catch(console.error);
                return;
            }

            // Normal buffer (shell) - use programmatic xterm scroll
            const linesToScroll = Math.sign(e.deltaY) * Math.max(1, Math.ceil(Math.abs(e.deltaY) / 30));
            term.scrollLines(linesToScroll);
        };


        // Use capture phase and NOT passive (so we can preventDefault)
        terminalRef.current?.addEventListener('wheel', handleWheel, { passive: false, capture: true });
        console.log('%c[INIT] Wheel interception enabled - using programmatic scrollLines()', 'color: lime; font-weight: bold');
        LogFrontendDiagnostic('[INIT] Wheel interception enabled');

        console.log('%c[INIT] All scroll detection methods initialized', 'color: green; font-weight: bold');
        LogFrontendDiagnostic('[INIT] Scroll detection initialized');

        // Listen for debug messages from backend
        // Filter by sessionId for multi-pane support
        const handleDebug = (payload) => {
            // Filter: only process events for this terminal's session
            if (payload?.sessionId !== sessionId) return;
            logger.debug('[Backend]', payload.data);
        };
        EventsOn('terminal:debug', handleDebug);

        // Handle pre-loaded history (initial scrollback from tmux)
        // In polling mode, this contains the full scrollback captured before polling starts
        // Filter by sessionId for multi-pane support
        const handleTerminalHistory = (payload) => {
            // Filter: only process events for this terminal's session
            if (payload?.sessionId !== sessionId) return;

            const history = payload.data;
            logger.info('Received initial history:', history?.length || 0, 'bytes');
            if (xtermRef.current && history) {
                // Write initial scrollback to xterm
                xtermRef.current.write(history);
                xtermRef.current.scrollToBottom();

                // Mark session load complete for scroll tracking
                setTimeout(() => {
                    if (xtermRef.current?._markSessionLoadComplete) {
                        xtermRef.current._markSessionLoadComplete();
                    }
                }, 100);
            }
        };
        EventsOn('terminal:history', handleTerminalHistory);

        // Listen for data from backend (polling mode - history gaps and viewport diffs)
        // In polling mode, this receives:
        // 1. History gap lines (content that scrolled off viewport)
        // 2. Viewport diff updates (efficient in-place updates)
        // Filter by sessionId for multi-pane support
        const handleTerminalData = (payload) => {
            // Filter: only process events for this terminal's session
            if (payload?.sessionId !== sessionId) return;

            if (xtermRef.current && payload.data) {
                xtermRef.current.write(payload.data);
            }
        };
        EventsOn('terminal:data', handleTerminalData);

        // Listen for terminal exit
        // Filter by sessionId for multi-pane support
        const handleTerminalExit = (payload) => {
            // Filter: only process events for this terminal's session
            if (payload?.sessionId !== sessionId) return;

            if (xtermRef.current) {
                xtermRef.current.write(`\r\n\x1b[31m[Terminal exited: ${payload.data}]\x1b[0m\r\n`);
            }
        };
        EventsOn('terminal:exit', handleTerminalExit);

        // Track last sent dimensions to avoid duplicate calls
        let lastCols = term.cols;
        let lastRows = term.rows;

        // Debounced scrollback refresh - fixes xterm.js reflow issues with box-drawing chars
        // Only triggers after resize settles (no resize events for 400ms)
        // In polling mode, this clears xterm so polling can rebuild display cleanly
        let scrollbackRefreshTimer = null;
        const refreshScrollbackAfterResize = async () => {
            if (!session?.tmuxSession || !xtermRef.current) {
                logger.debug('[RESIZE-REFRESH] Skipped - no session or xterm');
                return;
            }

            try {
                logger.info('[RESIZE-REFRESH] Clearing xterm for clean polling rebuild...');
                // In polling mode, just clear xterm - polling will rebuild the display
                // The backend Resize() already reset the history tracker
                xtermRef.current.clear();
                logger.info('[RESIZE-REFRESH] Done - polling will rebuild display');
            } catch (err) {
                logger.error('[RESIZE-REFRESH] Failed:', err);
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
                        ResizeTerminal(sessionId, cols, rows)
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
                    if (session.isRemote && session.remoteHost) {
                        // Remote session - use SSH polling
                        logger.info('Connecting to remote tmux session (SSH polling mode):',
                            session.remoteHost, session.tmuxSession, 'sessionId:', sessionId);
                        // Backend handles:
                        // 1. SSH connection to remote host
                        // 2. History fetch + emit via terminal:history event
                        // 3. Polling loop for display updates via SSH
                        await StartRemoteTmuxSession(sessionId, session.remoteHost, session.tmuxSession, cols, rows);
                        logger.info('Remote polling session started');
                        console.log('%c[LOAD] Remote SSH polling session started', 'color: cyan; font-weight: bold');
                        LogFrontendDiagnostic('[LOAD] Remote SSH polling session started');
                    } else {
                        // Local session - use local polling
                        logger.info('Connecting to tmux session (polling mode):', session.tmuxSession, 'sessionId:', sessionId);
                        // Backend handles:
                        // 1. History fetch + emit via terminal:history event
                        // 2. PTY attach for user input
                        // 3. Polling loop for display updates
                        await StartTmuxSession(sessionId, session.tmuxSession, cols, rows);
                        logger.info('Polling session started');
                        console.log('%c[LOAD] Polling session started', 'color: cyan; font-weight: bold');
                        LogFrontendDiagnostic('[LOAD] Polling session started');
                    }
                } else {
                    logger.info('Starting new terminal, sessionId:', sessionId);
                    await StartTerminal(sessionId, cols, rows);
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
            logger.info('Cleaning up terminal, sessionId:', sessionId);

            // Close the PTY backend for this session
            CloseTerminal(sessionId).catch((err) => {
                logger.error('Failed to close terminal:', err);
            });

            // NOTE: We intentionally do NOT call EventsOff() here.
            // EventsOff removes ALL listeners globally, which breaks multi-pane mode
            // when one pane unmounts. Our event handlers already filter by sessionId
            // and check xtermRef.current, so stale listeners safely no-op.

            if (scrollbackRefreshTimer) {
                clearTimeout(scrollbackRefreshTimer);
            }
            resizeObserver.disconnect();
            scrollDisposable.dispose();
            dataDisposable.dispose();
            if (customKeyHandler) customKeyHandler.dispose();
            // Clean up mouse mode parser handlers
            if (enableHandler) enableHandler.dispose();
            if (disableHandler) disableHandler.dispose();
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
    }, [searchRef, session, paneId, onFocus, fontSize]); // Note: theme/fontSize changes handled by separate useEffects

    // Scroll to bottom when indicator is clicked
    const handleScrollToBottom = () => {
        if (xtermRef.current) {
            xtermRef.current.scrollToBottom();
            isAtBottomRef.current = true;
            setShowScrollIndicator(false);
        }
    };

    return (
        <div style={{ position: 'relative', width: '100%', height: '100%' }} className={isAltScreen ? 'terminal-alt-screen' : ''}>
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
