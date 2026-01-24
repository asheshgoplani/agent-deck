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

const TERMINAL_OPTIONS = {
    fontFamily: '"MesloLGS NF", Menlo, Monaco, "Courier New", monospace',
    fontSize: 14,
    lineHeight: 1.2,
    cursorBlink: true,
    cursorStyle: 'block',
    scrollback: 10000,
    allowProposedApi: true,
    // xterm.js v6 uses DOM renderer by default, with VS Code-based scrollbar
    smoothScrollDuration: 0,
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

        // Track scroll positions - we need to know the MINIMUM position during a scroll session
        // because PTY data may reset scroll to bottom, but we want to know where user scrolled to
        let lastKnownScrollPosition = -1;
        let minScrollPositionDuringScroll = -1;
        let isUserScrolling = false;

        // Method 1: xterm.js onScroll API - THIS gives us the real scroll position!
        const scrollDisposable = term.onScroll((newPosition) => {
            lastKnownScrollPosition = newPosition;

            // Track the minimum (most scrolled up) position during this scroll session
            if (minScrollPositionDuringScroll < 0 || newPosition < minScrollPositionDuringScroll) {
                minScrollPositionDuringScroll = newPosition;
            }

            if (scrollSettleTimer) clearTimeout(scrollSettleTimer);
            scrollSettleTimer = setTimeout(() => {
                const scrolledUpTo = minScrollPositionDuringScroll;
                console.log(`%c[SCROLL-SETTLE] settled at ${lastKnownScrollPosition}, user scrolled up to ${scrolledUpTo}`, 'color: lime; font-weight: bold');
                LogFrontendDiagnostic(`[SCROLL-SETTLE] current=${lastKnownScrollPosition} minDuringScroll=${scrolledUpTo}`);

                // Reset for next scroll session
                minScrollPositionDuringScroll = -1;

                // Trigger repaint using the minimum scroll position (where user actually scrolled to)
                attemptRepaint(scrolledUpTo);
            }, 200);
        });

        // Expose scroll position getter for attemptRepaint
        term._lastKnownScrollPosition = () => lastKnownScrollPosition;
        term._getMinScrollPosition = () => minScrollPositionDuringScroll;

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

        // Method 3: Wheel event on terminal container with CAPTURE phase
        // This is the ONLY scroll detection method that works reliably in WKWebView
        let wheelSettleTimer = null;
        let isScrolling = false;

        const attemptRepaint = async () => {
            if (!xtermRef.current) return;

            const term = xtermRef.current;
            const buffer = term.buffer.active;

            // Get scroll position from our captured onScroll event value
            // This is more reliable than buffer.viewportY which may be stale
            const lastScrollPos = term._lastKnownScrollPosition?.() ?? -1;

            // Also get internal values for comparison
            const coreBuffer = term._core?.buffer;
            const ydisp = coreBuffer?.ydisp ?? 0;
            const ybase = coreBuffer?.ybase ?? 0;
            const viewportY = buffer.viewportY;
            const baseY = buffer.baseY;

            // Use the captured scroll position if available, otherwise fall back to ydisp
            const actualVisibleRow = lastScrollPos >= 0 ? lastScrollPos : ydisp;

            // We're scrolled up if the visible row is less than the base (bottom)
            const isScrolledUp = actualVisibleRow < baseY;

            console.log('%c[REPAINT] Attempting repaint after scroll settle...', 'color: yellow; font-weight: bold');
            LogFrontendDiagnostic(`[REPAINT] lastScrollPos=${lastScrollPos} actualVisibleRow=${actualVisibleRow}`);
            LogFrontendDiagnostic(`[REPAINT] ydisp=${ydisp} ybase=${ybase} viewportY=${viewportY} baseY=${baseY}`);
            LogFrontendDiagnostic(`[REPAINT] isScrolledUp=${isScrolledUp}`);
            console.log(`%c[REPAINT] lastScrollPos=${lastScrollPos} isScrolledUp=${isScrolledUp}`, 'color: cyan; font-weight: bold');

            // Strategy 1: Check buffer content of ACTUALLY visible rows
            const viewportStart = actualVisibleRow;
            for (let i = 0; i < Math.min(3, term.rows); i++) {
                const line = buffer.getLine(viewportStart + i);
                if (line) {
                    const text = line.translateToString();
                    // Check for box-drawing chars
                    if (text.includes('─') || text.includes('│') || text.includes('┌')) {
                        console.log(`%c[BUFFER-CHECK] Row ${viewportStart + i}: "${text.substring(0, 60)}..."`, 'color: cyan');
                        LogFrontendDiagnostic(`[BUFFER-CHECK] Row ${viewportStart + i} has box chars, content OK`);
                    }
                }
            }

            // Strategy 2: Standard xterm.js refresh
            term.refresh(0, term.rows - 1);
            console.log('%c[REPAINT] Strategy 2: term.refresh() done', 'color: yellow');

            // Strategy 3: Force CSS layer invalidation via opacity toggle
            const screenEl = terminalRef.current?.querySelector('.xterm-screen');
            if (screenEl) {
                screenEl.style.opacity = '0.999';
                await new Promise(r => requestAnimationFrame(r));
                screenEl.style.opacity = '1';
                console.log('%c[REPAINT] Strategy 3: opacity toggle done', 'color: yellow');
            }

            // Strategy 4: Try to force xterm.js to re-render by briefly hiding rows
            // This might force new glyph rendering rather than cached
            if (screenEl) {
                screenEl.style.visibility = 'hidden';
                await new Promise(r => requestAnimationFrame(r));
                screenEl.style.visibility = 'visible';
                console.log('%c[REPAINT] Strategy 4: visibility toggle done', 'color: yellow');
            }

            // Strategy 5: Try to access xterm's internal render service for forced refresh
            try {
                const renderService = term._core?._renderService;
                if (renderService) {
                    // Try different internal methods that might force a clean render
                    if (renderService.refreshRows) {
                        renderService.refreshRows(0, term.rows - 1);
                        console.log('%c[REPAINT] Strategy 5a: renderService.refreshRows() done', 'color: yellow');
                    }
                    if (renderService._renderer?.handleResize) {
                        // Fake a resize event to force recalculation
                        renderService._renderer.handleResize(term.cols, term.rows);
                        console.log('%c[REPAINT] Strategy 5b: renderer.handleResize() done', 'color: yellow');
                    }
                    // Clear any cached dimensions
                    if (renderService.clear) {
                        renderService.clear();
                        console.log('%c[REPAINT] Strategy 5c: renderService.clear() done', 'color: yellow');
                    }
                    LogFrontendDiagnostic('[REPAINT] Strategy 5: internal render service methods called');
                }
            } catch (e) {
                console.log('%c[REPAINT] Strategy 5: internal API access failed:', e.message, 'color: red');
            }

            // Strategy 6: DISABLED - DOM rebuild breaks xterm.js internal references
            // const rowContainer = terminalRef.current?.querySelector('.xterm-rows');
            // if (rowContainer && isScrolledUp) {
            //     const clone = rowContainer.cloneNode(true);
            //     rowContainer.parentNode.replaceChild(clone, rowContainer);
            // }

            // Strategy 7: Verify buffer content (diagnostic)
            if (isScrolledUp) {
                console.log('%c[REPAINT] Strategy 7: In scrollback - checking buffer content...', 'color: orange; font-weight: bold');
                LogFrontendDiagnostic('[REPAINT] Strategy 7: Checking scrollback content');

                // Collect visible row content from ACTUAL visible position
                const visibleContent = [];
                for (let i = 0; i < term.rows; i++) {
                    const line = buffer.getLine(actualVisibleRow + i);
                    if (line) {
                        visibleContent.push(line.translateToString(true)); // true = trim trailing whitespace
                    }
                }

                console.log(`%c[REPAINT] Strategy 7: Collected ${visibleContent.length} rows from buffer (starting row ${actualVisibleRow})`, 'color: orange');

                // Verify the content is correct
                const hasBoxChars = visibleContent.some(line =>
                    line.includes('─') || line.includes('│') || line.includes('┌') || line.includes('┐')
                );
                if (hasBoxChars) {
                    LogFrontendDiagnostic(`[REPAINT] Buffer content has box chars - data is CORRECT, issue is visual only`);
                    console.log('%c[REPAINT] Buffer data is CORRECT - corruption is visual rendering only!', 'color: lime; font-weight: bold');

                    // Log the actual content of rows with box chars for verification
                    visibleContent.forEach((line, i) => {
                        if (line.includes('─') || line.includes('│')) {
                            console.log(`%c[BUFFER] Row ${actualVisibleRow + i}: ${line}`, 'color: cyan; font-size: 10px');
                        }
                    });
                }
            }

            LogFrontendDiagnostic('[REPAINT] All strategies applied');
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
            if (wheelSettleTimer) clearTimeout(wheelSettleTimer);
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
