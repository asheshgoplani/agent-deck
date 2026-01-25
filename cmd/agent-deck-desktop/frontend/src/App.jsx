import { useRef, useState, useEffect, useCallback, useMemo } from 'react';
import { useTooltip } from './Tooltip';
import { useTheme } from './context/ThemeContext';
import './App.css';
import Search from './Search';
import SessionSelector from './SessionSelector';
import CommandMenu from './CommandMenu';
import ToolPicker from './ToolPicker';
import ConfigPicker from './ConfigPicker';
import SettingsModal from './SettingsModal';
import UnifiedTopBar from './UnifiedTopBar';
import ShortcutBar from './ShortcutBar';
import KeyboardHelpModal from './KeyboardHelpModal';
import RenameDialog from './RenameDialog';
import PaneLayout from './PaneLayout';
import FocusModeOverlay from './FocusModeOverlay';
import MoveModeOverlay from './MoveModeOverlay';
import SaveLayoutModal from './SaveLayoutModal';
import HostPicker from './HostPicker';
import { BranchIcon } from './ToolIcon';
import { ListSessions, DiscoverProjects, CreateSession, CreateRemoteSession, RecordProjectUsage, GetQuickLaunchFavorites, AddQuickLaunchFavorite, GetQuickLaunchBarVisibility, SetQuickLaunchBarVisibility, GetGitBranch, IsGitWorktree, GetSessionMetadata, MarkSessionAccessed, GetDefaultLaunchConfig, UpdateSessionCustomLabel, GetFontSize, SetFontSize, GetScrollSpeed, GetSavedLayouts, SaveLayout, DeleteSavedLayout, StartRemoteTmuxSession } from '../wailsjs/go/main/App';
import { createLogger } from './logger';
import { DEFAULT_FONT_SIZE, MIN_FONT_SIZE, MAX_FONT_SIZE } from './constants/terminal';
import { shouldInterceptShortcut, hasAppModifier } from './utils/platform';
import {
    createSinglePaneLayout,
    splitPane,
    closePane as closePaneInLayout,
    updateSplitRatio,
    getAdjacentPane,
    getCyclicPane,
    getPaneList,
    findPane,
    updatePaneSession,
    countPanes,
    balanceLayout,
    createPresetLayout,
    applyPreset,
    getFirstPaneId,
    swapPaneSessions,
    layoutToSaveFormat,
    applySavedLayout,
} from './layoutUtils';

const logger = createLogger('App');

function App() {
    const searchAddonRef = useRef(null);
    const [showSearch, setShowSearch] = useState(false);
    const [searchFocusTrigger, setSearchFocusTrigger] = useState(0); // Increments to trigger focus
    const [view, setView] = useState('selector'); // 'selector' or 'terminal'
    const [selectedSession, setSelectedSession] = useState(null);
    const [showCloseConfirm, setShowCloseConfirm] = useState(false);
    const [showCommandMenu, setShowCommandMenu] = useState(false);
    const [sessions, setSessions] = useState([]);
    const [projects, setProjects] = useState([]);
    const [showToolPicker, setShowToolPicker] = useState(false);
    const [toolPickerProject, setToolPickerProject] = useState(null);
    const [showQuickLaunch, setShowQuickLaunch] = useState(true); // Show by default if favorites exist
    const [palettePinMode, setPalettePinMode] = useState(false); // When true, selecting pins instead of launching
    const [paletteNewTabMode, setPaletteNewTabMode] = useState(false); // When true, Cmd+T was used to open palette
    const [quickLaunchKey, setQuickLaunchKey] = useState(0); // For forcing refresh
    const [shortcuts, setShortcuts] = useState({}); // shortcut -> {path, name, tool}
    const [favorites, setFavorites] = useState([]); // All quick launch favorites
    const [gitBranch, setGitBranch] = useState(''); // Current git branch for selected session
    const [isWorktree, setIsWorktree] = useState(false); // Whether session is in a git worktree
    const [statusFilter, setStatusFilter] = useState('all'); // 'all', 'active', 'idle'
    const [showHelpModal, setShowHelpModal] = useState(false);
    const [showConfigPicker, setShowConfigPicker] = useState(false);
    const [configPickerTool, setConfigPickerTool] = useState(null);
    const [showSettings, setShowSettings] = useState(false);
    const [showLabelDialog, setShowLabelDialog] = useState(false);
    // Tab state - now includes layout tree and active pane
    // Each tab: { id, name, layout: LayoutNode, activePaneId: string, openedAt, zoomedPaneId: string|null }
    const [openTabs, setOpenTabs] = useState([]);
    const [activeTabId, setActiveTabId] = useState(null);
    const [fontSize, setFontSizeState] = useState(DEFAULT_FONT_SIZE);
    const [scrollSpeed, setScrollSpeedState] = useState(100); // Default 100%
    // Move mode - when true, shows pane numbers for session swap
    const [moveMode, setMoveMode] = useState(false);
    // Saved layouts
    const [savedLayouts, setSavedLayouts] = useState([]);
    const [showSaveLayoutModal, setShowSaveLayoutModal] = useState(false);

    // Build saved layout shortcut map for keyboard handling
    const savedLayoutShortcuts = useMemo(() => {
        const map = {};
        for (const layout of savedLayouts) {
            if (layout.shortcut) {
                map[layout.shortcut.toLowerCase()] = layout;
            }
        }
        return map;
    }, [savedLayouts]);

    const sessionSelectorRef = useRef(null);
    const terminalRefs = useRef({});
    const searchRefs = useRef({});

    // Tooltip for back button
    const { show: showBackTooltip, hide: hideBackTooltip, Tooltip: BackTooltip } = useTooltip();

    // Theme context for toggle
    const { theme, setTheme } = useTheme();

    // Remote session creation flow state
    const [showHostPicker, setShowHostPicker] = useState(false);
    const [selectedRemoteHost, setSelectedRemoteHost] = useState(null);
    const [showRemotePathInput, setShowRemotePathInput] = useState(false);

    // Cycle through status filter modes: all -> active -> idle -> all
    const handleCycleStatusFilter = useCallback(() => {
        setStatusFilter(current => {
            const modes = ['all', 'active', 'idle'];
            const currentIndex = modes.indexOf(current);
            const nextIndex = (currentIndex + 1) % modes.length;
            const nextMode = modes[nextIndex];
            logger.info('Cycling status filter', { from: current, to: nextMode });
            return nextMode;
        });
    }, []);

    // Build shortcut key from event
    const buildShortcutKey = useCallback((e) => {
        const parts = [];
        if (e.metaKey) parts.push('cmd');
        if (e.ctrlKey) parts.push('ctrl');
        if (e.shiftKey) parts.push('shift');
        if (e.altKey) parts.push('alt');
        if (e.key && e.key.length === 1) {
            parts.push(e.key.toLowerCase());
        }
        return parts.join('+');
    }, []);

    // Load shortcuts and favorites
    const loadShortcuts = useCallback(async () => {
        try {
            const favs = await GetQuickLaunchFavorites();
            setFavorites(favs || []);
            const shortcutMap = {};
            for (const fav of favs || []) {
                if (fav.shortcut) {
                    shortcutMap[fav.shortcut] = {
                        path: fav.path,
                        name: fav.name,
                        tool: fav.tool,
                    };
                }
            }
            setShortcuts(shortcutMap);
            logger.info('Loaded shortcuts', { count: Object.keys(shortcutMap).length, favorites: favs?.length || 0 });
        } catch (err) {
            logger.error('Failed to load shortcuts:', err);
        }
    }, []);

    // Load shortcuts, bar visibility, and font size on mount
    useEffect(() => {
        loadShortcuts();

        // Load bar visibility preference
        const loadBarVisibility = async () => {
            try {
                const visible = await GetQuickLaunchBarVisibility();
                setShowQuickLaunch(visible);
                logger.info('Loaded bar visibility', { visible });
            } catch (err) {
                logger.error('Failed to load bar visibility:', err);
            }
        };
        loadBarVisibility();

        // Load font size preference
        const loadFontSize = async () => {
            try {
                const size = await GetFontSize();
                setFontSizeState(size);
                logger.info('Loaded font size', { size });
            } catch (err) {
                logger.error('Failed to load font size:', err);
            }
        };
        loadFontSize();

        // Load scroll speed preference
        const loadScrollSpeed = async () => {
            try {
                const speed = await GetScrollSpeed();
                setScrollSpeedState(speed);
                logger.info('Loaded scroll speed', { speed });
            } catch (err) {
                logger.error('Failed to load scroll speed:', err);
            }
        };
        loadScrollSpeed();

        // Load saved layouts
        const loadSavedLayouts = async () => {
            try {
                const layouts = await GetSavedLayouts();
                setSavedLayouts(layouts || []);
                logger.info('Loaded saved layouts', { count: layouts?.length || 0 });
            } catch (err) {
                logger.error('Failed to load saved layouts:', err);
            }
        };
        loadSavedLayouts();
    }, [loadShortcuts]);

    const handleCloseSearch = useCallback(() => {
        setShowSearch(false);
    }, []);

    // Load sessions and projects for command menu
    const loadSessionsAndProjects = useCallback(async () => {
        try {
            logger.info('Loading sessions and projects for palette');
            const [sessionsResult, projectsResult] = await Promise.all([
                ListSessions(),
                DiscoverProjects(),
            ]);
            setSessions(sessionsResult || []);
            setProjects(projectsResult || []);
            logger.info('Loaded palette data', {
                sessions: sessionsResult?.length || 0,
                projects: projectsResult?.length || 0,
            });
        } catch (err) {
            logger.error('Failed to load palette data:', err);
        }
    }, []);

    // Load sessions/projects when palette opens
    useEffect(() => {
        if (showCommandMenu) {
            loadSessionsAndProjects();
        }
    }, [showCommandMenu, loadSessionsAndProjects]);

    // Handle command menu actions
    const handlePaletteAction = useCallback((actionId) => {
        switch (actionId) {
            case 'new-terminal':
                logger.info('Palette action: new terminal');
                setSelectedSession(null);
                setView('terminal');
                break;
            case 'refresh-sessions':
                logger.info('Palette action: refresh sessions');
                loadSessionsAndProjects();
                // If in selector view, tell it to refresh too
                if (view === 'selector') {
                    // Force re-render of selector
                    setView('selector');
                }
                break;
            case 'toggle-quick-launch':
                logger.info('Palette action: toggle quick launch bar');
                setShowQuickLaunch(prev => {
                    const newValue = !prev;
                    SetQuickLaunchBarVisibility(newValue).catch(err => {
                        logger.error('Failed to save bar visibility:', err);
                    });
                    return newValue;
                });
                break;
            case 'create-remote-session':
                logger.info('Palette action: create remote session');
                setShowHostPicker(true);
                break;
            case 'toggle-theme':
                logger.info('Palette action: toggle theme', { currentTheme: theme });
                // Toggle between light and dark (skip auto for quick toggle)
                setTheme(theme === 'dark' ? 'light' : 'dark');
                break;
            default:
                logger.warn('Unknown palette action:', actionId);
        }
    }, [view, loadSessionsAndProjects, theme, setTheme]);

    // Get the current active tab
    const activeTab = openTabs.find(t => t.id === activeTabId);

    // Tab management handlers - defined early so other handlers can use them
    const handleOpenTab = useCallback((session) => {
        // Check if session is already open in any tab's pane
        for (const tab of openTabs) {
            const panes = getPaneList(tab.layout);
            const paneWithSession = panes.find(p => p.session?.id === session.id);
            if (paneWithSession) {
                // Session exists in this tab, switch to it
                setActiveTabId(tab.id);
                // Update the tab's active pane to this one
                setOpenTabs(prev => prev.map(t =>
                    t.id === tab.id
                        ? { ...t, activePaneId: paneWithSession.id }
                        : t
                ));
                return;
            }
        }
        // Create new tab with single-pane layout containing the session
        const layout = createSinglePaneLayout(session);
        const newTab = {
            id: `tab-${session.id}-${Date.now()}`,
            name: session.customLabel || session.title,
            layout,
            activePaneId: layout.id,
            openedAt: Date.now(),
            zoomedPaneId: null,
        };
        logger.info('Opening new tab', { tabId: newTab.id, sessionTitle: session.title });
        setOpenTabs(prev => [...prev, newTab]);
        setActiveTabId(newTab.id);
    }, [openTabs]);

    const handleCloseTab = useCallback((tabId) => {
        setOpenTabs(prev => {
            const tabIndex = prev.findIndex(t => t.id === tabId);
            if (tabIndex === -1) return prev;

            const newTabs = prev.filter(t => t.id !== tabId);
            logger.info('Closing tab', { tabId, remainingTabs: newTabs.length });

            // If closing active tab, switch to adjacent
            if (tabId === activeTabId) {
                if (newTabs.length === 0) {
                    // No tabs left, return to selector
                    setActiveTabId(null);
                    setSelectedSession(null);
                    setView('selector');
                    setGitBranch('');
                    setIsWorktree(false);
                } else {
                    // Switch to previous tab, or next if at start
                    const newIndex = Math.min(tabIndex, newTabs.length - 1);
                    const newActiveTab = newTabs[newIndex];
                    setActiveTabId(newActiveTab.id);
                    // Get the active pane's session from the new active tab
                    const activePane = findPane(newActiveTab.layout, newActiveTab.activePaneId);
                    setSelectedSession(activePane?.session || null);
                }
            }

            return newTabs;
        });
    }, [activeTabId]);

    const handleSwitchTab = useCallback(async (tabId) => {
        const tab = openTabs.find(t => t.id === tabId);
        if (!tab) return;

        logger.info('Switching to tab', { tabId, tabName: tab.name });
        setActiveTabId(tabId);
        setView('terminal');

        // Get the active pane's session from the tab
        const activePane = findPane(tab.layout, tab.activePaneId);
        const session = activePane?.session;
        setSelectedSession(session || null);

        // Update git info for the session using real-time cwd from tmux
        if (session?.tmuxSession) {
            try {
                const metadata = await GetSessionMetadata(session.tmuxSession);
                setGitBranch(metadata.gitBranch || '');
                // Check worktree status for the actual cwd
                if (metadata.cwd) {
                    const worktree = await IsGitWorktree(metadata.cwd);
                    setIsWorktree(worktree);
                } else {
                    setIsWorktree(false);
                }
            } catch (err) {
                setGitBranch('');
                setIsWorktree(false);
            }
        } else {
            setGitBranch('');
            setIsWorktree(false);
        }
    }, [openTabs]);

    // ============================================================
    // PANE MANAGEMENT - Layout manipulation handlers
    // ============================================================

    // Focus a specific pane
    const handlePaneFocus = useCallback((paneId) => {
        if (!activeTabId) return;

        setOpenTabs(prev => prev.map(tab => {
            if (tab.id !== activeTabId) return tab;

            const pane = findPane(tab.layout, paneId);
            if (!pane) return tab;

            logger.debug('Pane focused', { paneId, hasSession: !!pane.session });

            // Update selectedSession to match the focused pane
            if (pane.session) {
                setSelectedSession(pane.session);
                // Update git info using real-time cwd from tmux
                if (pane.session.tmuxSession) {
                    GetSessionMetadata(pane.session.tmuxSession).then(async (metadata) => {
                        setGitBranch(metadata.gitBranch || '');
                        if (metadata.cwd) {
                            const worktree = await IsGitWorktree(metadata.cwd);
                            setIsWorktree(worktree);
                        } else {
                            setIsWorktree(false);
                        }
                    }).catch(() => {
                        setGitBranch('');
                        setIsWorktree(false);
                    });
                } else {
                    setGitBranch('');
                    setIsWorktree(false);
                }
            } else {
                setSelectedSession(null);
                setGitBranch('');
                setIsWorktree(false);
            }

            return { ...tab, activePaneId: paneId };
        }));
    }, [activeTabId]);

    // Split the active pane
    const handleSplitPane = useCallback((direction) => {
        if (!activeTab) return;

        const { layout, newPaneId } = splitPane(activeTab.layout, activeTab.activePaneId, direction);
        logger.info('Split pane', { direction, newPaneId });

        setOpenTabs(prev => prev.map(tab =>
            tab.id === activeTabId
                ? { ...tab, layout, activePaneId: newPaneId }
                : tab
        ));

        // Clear selected session since new pane is empty
        setSelectedSession(null);
        setGitBranch('');
        setIsWorktree(false);
    }, [activeTab, activeTabId]);

    // Close the active pane
    const handleClosePane = useCallback(() => {
        if (!activeTab) return;

        const paneCount = countPanes(activeTab.layout);
        if (paneCount <= 1) {
            // Last pane - close the tab instead
            handleCloseTab(activeTabId);
            return;
        }

        // Find a sibling pane to focus after closing
        const nextPaneId = getCyclicPane(activeTab.layout, activeTab.activePaneId, 'next');
        const newLayout = closePaneInLayout(activeTab.layout, activeTab.activePaneId);

        if (!newLayout) {
            // This shouldn't happen given the paneCount check above
            handleCloseTab(activeTabId);
            return;
        }

        logger.info('Closed pane', { closedPaneId: activeTab.activePaneId, newActivePaneId: nextPaneId });

        setOpenTabs(prev => prev.map(tab => {
            if (tab.id !== activeTabId) return tab;
            return { ...tab, layout: newLayout, activePaneId: nextPaneId };
        }));

        // Update selected session to the new active pane
        const newActivePane = findPane(newLayout, nextPaneId);
        if (newActivePane?.session) {
            setSelectedSession(newActivePane.session);
        } else {
            setSelectedSession(null);
            setGitBranch('');
            setIsWorktree(false);
        }
    }, [activeTab, activeTabId, handleCloseTab]);

    // Navigate to adjacent pane
    const handleNavigatePane = useCallback((direction) => {
        if (!activeTab) return;

        const adjacentPaneId = getAdjacentPane(activeTab.layout, activeTab.activePaneId, direction);
        if (adjacentPaneId) {
            handlePaneFocus(adjacentPaneId);
        }
    }, [activeTab, handlePaneFocus]);

    // Navigate to next/previous pane (cyclic)
    const handleCyclicNavigatePane = useCallback((direction) => {
        if (!activeTab) return;

        const nextPaneId = getCyclicPane(activeTab.layout, activeTab.activePaneId, direction);
        if (nextPaneId) {
            handlePaneFocus(nextPaneId);
        }
    }, [activeTab, handlePaneFocus]);

    // Update split ratio
    const handleRatioChange = useCallback((paneId, newRatio) => {
        if (!activeTabId) return;

        setOpenTabs(prev => prev.map(tab =>
            tab.id === activeTabId
                ? { ...tab, layout: updateSplitRatio(tab.layout, paneId, newRatio) }
                : tab
        ));
    }, [activeTabId]);

    // Balance all panes
    const handleBalancePanes = useCallback(() => {
        if (!activeTabId) return;

        logger.info('Balancing panes');
        setOpenTabs(prev => prev.map(tab =>
            tab.id === activeTabId
                ? { ...tab, layout: balanceLayout(tab.layout) }
                : tab
        ));
    }, [activeTabId]);

    // Toggle zoom on active pane
    const handleToggleZoom = useCallback(() => {
        if (!activeTab) return;

        setOpenTabs(prev => prev.map(tab => {
            if (tab.id !== activeTabId) return tab;
            const newZoomedPaneId = tab.zoomedPaneId ? null : tab.activePaneId;
            logger.info('Toggle zoom', { zoomedPaneId: newZoomedPaneId });
            return { ...tab, zoomedPaneId: newZoomedPaneId };
        }));
    }, [activeTab, activeTabId]);

    // Exit zoom mode
    const handleExitZoom = useCallback(() => {
        if (!activeTab || !activeTab.zoomedPaneId) return;

        setOpenTabs(prev => prev.map(tab =>
            tab.id === activeTabId
                ? { ...tab, zoomedPaneId: null }
                : tab
        ));
    }, [activeTab, activeTabId]);

    // Apply a layout preset
    const handleApplyPreset = useCallback((presetType) => {
        if (!activeTab) return;

        const presetLayout = createPresetLayout(presetType);
        const { layout, closedSessions } = applyPreset(activeTab.layout, presetLayout);
        const firstPaneId = getFirstPaneId(layout);

        logger.info('Applied preset', { presetType, closedSessions: closedSessions.length });

        setOpenTabs(prev => prev.map(tab => {
            if (tab.id !== activeTabId) return tab;
            return {
                ...tab,
                layout,
                activePaneId: firstPaneId,
                zoomedPaneId: null, // Exit zoom when applying preset
            };
        }));

        // Update selected session to the first pane's session
        const firstPane = findPane(layout, firstPaneId);
        setSelectedSession(firstPane?.session || null);
    }, [activeTab, activeTabId]);

    // Build pane number map for move mode (paneId -> 1, 2, 3, ...)
    const buildPaneNumberMap = useCallback((layout) => {
        if (!layout) return {};
        const panes = getPaneList(layout);
        const map = {};
        panes.forEach((pane, index) => {
            map[pane.id] = index + 1; // 1-indexed for user display
        });
        return map;
    }, []);

    // Enter move mode
    const handleEnterMoveMode = useCallback(() => {
        if (!activeTab || countPanes(activeTab.layout) < 2) {
            logger.info('Cannot enter move mode - need at least 2 panes');
            return;
        }
        logger.info('Entering move mode');
        setMoveMode(true);
    }, [activeTab]);

    // Exit move mode
    const handleExitMoveMode = useCallback(() => {
        logger.info('Exiting move mode');
        setMoveMode(false);
    }, []);

    // Swap sessions by pane number (1-indexed)
    const handleMoveToPane = useCallback((targetPaneNumber) => {
        if (!activeTab || !moveMode) return;

        const paneNumberMap = buildPaneNumberMap(activeTab.layout);

        // Find target pane ID by number
        const targetPaneId = Object.entries(paneNumberMap).find(
            ([_, num]) => num === targetPaneNumber
        )?.[0];

        if (!targetPaneId) {
            logger.warn('Invalid pane number', { targetPaneNumber });
            handleExitMoveMode();
            return;
        }

        // Don't swap with self
        if (targetPaneId === activeTab.activePaneId) {
            logger.info('Cannot swap pane with itself');
            handleExitMoveMode();
            return;
        }

        logger.info('Swapping sessions', {
            from: activeTab.activePaneId,
            to: targetPaneId,
            targetNumber: targetPaneNumber
        });

        // Swap sessions
        setOpenTabs(prev => prev.map(tab => {
            if (tab.id !== activeTabId) return tab;
            return {
                ...tab,
                layout: swapPaneSessions(tab.layout, tab.activePaneId, targetPaneId),
            };
        }));

        // Exit move mode
        handleExitMoveMode();
    }, [activeTab, activeTabId, moveMode, buildPaneNumberMap, handleExitMoveMode]);

    // Open save layout modal
    const handleOpenSaveLayoutModal = useCallback(() => {
        if (!activeTab || countPanes(activeTab.layout) < 2) {
            logger.info('Cannot save layout - need at least 2 panes');
            return;
        }
        setShowSaveLayoutModal(true);
    }, [activeTab]);

    // Save current layout
    const handleSaveLayout = useCallback(async (name, shortcut) => {
        if (!activeTab) return;

        try {
            // Convert current layout to save format (strips sessions)
            const layoutStructure = layoutToSaveFormat(activeTab.layout);

            const savedLayout = await SaveLayout({
                id: '', // Will be assigned by backend
                name,
                layout: layoutStructure,
                shortcut: shortcut || '',
                createdAt: 0, // Will be set by backend
            });

            logger.info('Layout saved', { id: savedLayout.id, name });

            // Refresh saved layouts
            const layouts = await GetSavedLayouts();
            setSavedLayouts(layouts || []);

            setShowSaveLayoutModal(false);
        } catch (err) {
            logger.error('Failed to save layout:', err);
        }
    }, [activeTab]);

    // Apply a saved layout
    const handleApplySavedLayout = useCallback((savedLayout) => {
        if (!activeTab || !savedLayout?.layout) return;

        const { layout, closedSessions } = applySavedLayout(activeTab.layout, savedLayout.layout);
        const firstPaneId = getFirstPaneId(layout);

        logger.info('Applied saved layout', { name: savedLayout.name, closedSessions: closedSessions.length });

        setOpenTabs(prev => prev.map(tab => {
            if (tab.id !== activeTabId) return tab;
            return {
                ...tab,
                layout,
                activePaneId: firstPaneId,
                zoomedPaneId: null, // Exit zoom when applying layout
            };
        }));

        // Update selected session to the first pane's session
        const firstPane = findPane(layout, firstPaneId);
        setSelectedSession(firstPane?.session || null);
    }, [activeTab, activeTabId]);

    // Handle layout actions from command menu
    // NOTE: Defined after all the handlers it depends on to avoid forward reference issues
    const handleLayoutAction = useCallback((actionId) => {
        logger.info('Layout action from palette', { actionId });
        switch (actionId) {
            case 'split-right':
                handleSplitPane('vertical');
                break;
            case 'split-down':
                handleSplitPane('horizontal');
                break;
            case 'close-pane':
                handleClosePane();
                break;
            case 'balance-panes':
                handleBalancePanes();
                break;
            case 'toggle-zoom':
                handleToggleZoom();
                break;
            case 'move-to-pane':
                handleEnterMoveMode();
                break;
            case 'layout-single':
                handleApplyPreset('single');
                break;
            case 'layout-2-col':
                handleApplyPreset('2-col');
                break;
            case 'layout-2-row':
                handleApplyPreset('2-row');
                break;
            case 'layout-2x2':
                handleApplyPreset('2x2');
                break;
            case 'save-layout':
                handleOpenSaveLayoutModal();
                break;
            default:
                // Check for saved layout IDs (format: 'saved-layout:id')
                if (actionId.startsWith('saved-layout:')) {
                    const layoutId = actionId.replace('saved-layout:', '');
                    const savedLayout = savedLayouts.find(l => l.id === layoutId);
                    if (savedLayout) {
                        handleApplySavedLayout(savedLayout);
                    } else {
                        logger.warn('Saved layout not found:', layoutId);
                    }
                } else {
                    logger.warn('Unknown layout action:', actionId);
                }
        }
    }, [handleSplitPane, handleClosePane, handleBalancePanes, handleToggleZoom, handleEnterMoveMode, handleApplyPreset, handleOpenSaveLayoutModal, savedLayouts, handleApplySavedLayout]);

    // Delete a saved layout
    const handleDeleteSavedLayout = useCallback(async (layoutId) => {
        try {
            await DeleteSavedLayout(layoutId);
            logger.info('Layout deleted', { layoutId });

            // Refresh saved layouts
            const layouts = await GetSavedLayouts();
            setSavedLayouts(layouts || []);
        } catch (err) {
            logger.error('Failed to delete layout:', err);
        }
    }, []);

    // Open a session in the active pane (from command menu)
    const handlePaneSessionSelect = useCallback((paneId) => {
        // This is called when user clicks on an empty pane
        // Opens command menu to select a session for this pane
        if (!activeTabId) return;

        // First, ensure this pane is focused
        handlePaneFocus(paneId);

        // Then open command menu
        setShowCommandMenu(true);
    }, [activeTabId, handlePaneFocus]);

    // Assign a session to the current active pane
    const handleAssignSessionToPane = useCallback((session) => {
        if (!activeTab) return;

        logger.info('Assigning session to pane', { paneId: activeTab.activePaneId, sessionTitle: session.title });

        setOpenTabs(prev => prev.map(tab => {
            if (tab.id !== activeTabId) return tab;
            return {
                ...tab,
                layout: updatePaneSession(tab.layout, tab.activePaneId, session),
                // Update tab name if it's a single-pane tab
                name: countPanes(tab.layout) === 1 ? (session.customLabel || session.title) : tab.name,
            };
        }));

        setSelectedSession(session);
    }, [activeTab, activeTabId]);

    // Launch a project with the specified tool and optional config
    // customLabel is optional - if provided, will be set as the session's custom label
    const handleLaunchProject = useCallback(async (projectPath, projectName, tool, configKey = '', customLabel = '') => {
        try {
            logger.info('Launching project', { projectPath, projectName, tool, configKey, customLabel });

            // Create session with config key
            const session = await CreateSession(projectPath, projectName, tool, configKey);
            logger.info('Session created', { sessionId: session.id, tmuxSession: session.tmuxSession });

            // Validate session object
            if (!session || !session.id || !session.tmuxSession) {
                throw new Error('Invalid session returned from CreateSession');
            }

            // Set custom label if provided
            if (customLabel) {
                try {
                    await UpdateSessionCustomLabel(session.id, customLabel);
                    session.customLabel = customLabel;
                    logger.info('Custom label set', { customLabel });
                } catch (err) {
                    logger.warn('Failed to set custom label:', err);
                }
            }

            // Record usage for frecency
            await RecordProjectUsage(projectPath);

            // Load git branch and worktree status using real-time cwd from tmux
            try {
                const metadata = await GetSessionMetadata(session.tmuxSession);
                setGitBranch(metadata.gitBranch || '');
                if (metadata.cwd) {
                    const worktree = await IsGitWorktree(metadata.cwd);
                    setIsWorktree(worktree);
                } else {
                    setIsWorktree(false);
                }
                logger.info('Git info:', { branch: metadata.gitBranch || '(not a git repo)', cwd: metadata.cwd });
            } catch (err) {
                setGitBranch('');
                setIsWorktree(false);
            }

            // Open as tab and switch to terminal view
            handleOpenTab(session);
            setSelectedSession(session);
            setView('terminal');
        } catch (err) {
            logger.error('Failed to launch project:', err);
            // Show alert so user knows something went wrong
            alert(`Failed to create session: ${err.message || err}`);
        }
    }, [handleOpenTab]);

    // Launch a remote session (modified version of handleLaunchProject)
    const handleLaunchRemoteProject = useCallback(async (hostId, projectPath, projectName, tool, configKey = '') => {
        try {
            logger.info('Launching remote project', { hostId, projectPath, projectName, tool, configKey });

            // Create session on remote host
            const session = await CreateRemoteSession(hostId, projectPath, projectName, tool, configKey);
            logger.info('Remote session created', { sessionId: session.id, tmuxSession: session.tmuxSession, remoteHost: session.remoteHost });

            // Clear remote session state
            setSelectedRemoteHost(null);

            // Open as tab and switch to terminal view
            handleOpenTab(session);
            setSelectedSession(session);
            setView('terminal');
            // Remote sessions don't have local git info
            setGitBranch('');
            setIsWorktree(false);
        } catch (err) {
            logger.error('Failed to launch remote project:', err);
            // Show alert so user knows something went wrong
            alert(`Failed to create remote session: ${err.message || err}`);
        }
    }, [handleOpenTab]);

    // Show tool picker for a project
    const handleShowToolPicker = useCallback((projectPath, projectName) => {
        logger.info('Showing tool picker', { projectPath, projectName });
        setToolPickerProject({ path: projectPath, name: projectName });
        setShowToolPicker(true);
    }, []);

    // Pin project to Quick Launch
    const handlePinToQuickLaunch = useCallback(async (projectPath, projectName) => {
        try {
            logger.info('Pinning to Quick Launch', { projectPath, projectName });
            await AddQuickLaunchFavorite(projectName, projectPath, 'claude');
            // Refresh favorites and Quick Launch Bar
            await loadShortcuts();
            setQuickLaunchKey(prev => prev + 1);
        } catch (err) {
            logger.error('Failed to pin to Quick Launch:', err);
        }
    }, [loadShortcuts]);

    // Open palette in pin mode (for adding favorites)
    const handleOpenPaletteForPinning = useCallback(() => {
        logger.info('Opening palette in pin mode');
        setPalettePinMode(true);
        setShowCommandMenu(true);
    }, []);

    // Close palette and reset modes
    const handleClosePalette = useCallback(() => {
        setShowCommandMenu(false);
        setPalettePinMode(false);
        setPaletteNewTabMode(false);
    }, []);

    // Handle tool selection from picker (use default config if available)
    const handleToolSelected = useCallback(async (tool) => {
        if (toolPickerProject) {
            // Try to get default config for this tool
            let configKey = '';
            try {
                const defaultConfig = await GetDefaultLaunchConfig(tool);
                if (defaultConfig?.key) {
                    configKey = defaultConfig.key;
                    logger.info('Using default config', { tool, configKey });
                }
            } catch (err) {
                logger.warn('Failed to get default config:', err);
            }

            // Check if this is a remote session
            if (toolPickerProject.isRemote && toolPickerProject.remoteHost) {
                handleLaunchRemoteProject(toolPickerProject.remoteHost, toolPickerProject.path, toolPickerProject.name, tool, configKey);
            } else {
                handleLaunchProject(toolPickerProject.path, toolPickerProject.name, tool, configKey);
            }
        }
        setShowToolPicker(false);
        setToolPickerProject(null);
        setSelectedRemoteHost(null);
    }, [toolPickerProject, handleLaunchProject, handleLaunchRemoteProject]);

    // Handle tool selection with config picker (Cmd+Enter)
    const handleToolSelectedWithConfig = useCallback((tool) => {
        logger.info('Opening config picker', { tool });
        setShowToolPicker(false);
        setConfigPickerTool(tool);
        setShowConfigPicker(true);
    }, []);

    // Handle config selection from picker
    const handleConfigSelected = useCallback((configKey) => {
        if (toolPickerProject && configPickerTool) {
            // Check if this is a remote session
            if (toolPickerProject.isRemote && toolPickerProject.remoteHost) {
                handleLaunchRemoteProject(toolPickerProject.remoteHost, toolPickerProject.path, toolPickerProject.name, configPickerTool, configKey);
            } else {
                handleLaunchProject(toolPickerProject.path, toolPickerProject.name, configPickerTool, configKey);
            }
        }
        setShowConfigPicker(false);
        setConfigPickerTool(null);
        setToolPickerProject(null);
        setSelectedRemoteHost(null);
    }, [toolPickerProject, configPickerTool, handleLaunchProject, handleLaunchRemoteProject]);

    // Cancel config picker
    const handleCancelConfigPicker = useCallback(() => {
        setShowConfigPicker(false);
        setConfigPickerTool(null);
        // Go back to tool picker
        setShowToolPicker(true);
    }, []);

    // Cancel tool picker
    const handleCancelToolPicker = useCallback(() => {
        setShowToolPicker(false);
        setToolPickerProject(null);
        // Clear remote session state if in remote flow
        setSelectedRemoteHost(null);
    }, []);

    // ==================== Remote Session Creation ====================

    // Handle host selection from HostPicker
    const handleHostSelected = useCallback((hostId) => {
        logger.info('Remote host selected', { hostId });
        setSelectedRemoteHost(hostId);
        setShowHostPicker(false);
        setShowRemotePathInput(true);
    }, []);

    // Cancel host picker
    const handleCancelHostPicker = useCallback(() => {
        setShowHostPicker(false);
        setSelectedRemoteHost(null);
    }, []);

    // Handle remote path input
    const handleRemotePathSubmit = useCallback((path) => {
        if (!path.trim()) return;
        logger.info('Remote path entered', { path, host: selectedRemoteHost });
        setShowRemotePathInput(false);
        // Extract project name from path (last component)
        const projectName = path.split('/').pop() || path;
        setToolPickerProject({ path: path.trim(), name: projectName, isRemote: true, remoteHost: selectedRemoteHost });
        setShowToolPicker(true);
    }, [selectedRemoteHost]);

    // Cancel remote path input
    const handleCancelRemotePathInput = useCallback(() => {
        setShowRemotePathInput(false);
        setSelectedRemoteHost(null);
    }, []);

    // Open settings modal
    const handleOpenSettings = useCallback(() => {
        logger.info('Opening settings modal');
        setShowSettings(true);
    }, []);

    // Handle saving custom label for current session
    const handleSaveSessionCustomLabel = useCallback(async (newLabel) => {
        if (!selectedSession) return;
        try {
            logger.info('Saving session custom label', { sessionId: selectedSession.id, newLabel });
            await UpdateSessionCustomLabel(selectedSession.id, newLabel);
            // Update local state
            setSelectedSession(prev => ({ ...prev, customLabel: newLabel }));
        } catch (err) {
            logger.error('Failed to save session custom label:', err);
        }
        setShowLabelDialog(false);
    }, [selectedSession]);

    // Handle tab label updated from context menu
    const handleTabLabelUpdated = useCallback((sessionId, newLabel) => {
        setOpenTabs(prev => prev.map(tab => {
            if (tab.session.id === sessionId) {
                return { ...tab, session: { ...tab.session, customLabel: newLabel || undefined } };
            }
            return tab;
        }));
        if (selectedSession?.id === sessionId) {
            setSelectedSession(prev => prev ? { ...prev, customLabel: newLabel || undefined } : prev);
        }
    }, [selectedSession]);

    const handleSelectSession = useCallback(async (session) => {
        logger.info('Selecting session:', session.title);

        // Open as tab
        handleOpenTab(session);

        setSelectedSession(session);
        setView('terminal');

        // Update last accessed timestamp for sorting
        try {
            await MarkSessionAccessed(session.id);
        } catch (err) {
            logger.warn('Failed to mark session accessed:', err);
        }

        // Load git branch and worktree status using real-time cwd from tmux
        if (session.tmuxSession) {
            try {
                const metadata = await GetSessionMetadata(session.tmuxSession);
                setGitBranch(metadata.gitBranch || '');
                // Check worktree status for the actual cwd
                if (metadata.cwd) {
                    const worktree = await IsGitWorktree(metadata.cwd);
                    setIsWorktree(worktree);
                } else {
                    setIsWorktree(false);
                }
                logger.info('Git info:', { branch: metadata.gitBranch || '(not a git repo)', cwd: metadata.cwd, isWorktree: metadata.cwd ? 'checking...' : false });
            } catch (err) {
                logger.warn('Failed to get git info:', err);
                setGitBranch('');
                setIsWorktree(false);
            }
        } else {
            setGitBranch('');
            setIsWorktree(false);
        }
    }, [handleOpenTab]);

    const handleNewTerminal = useCallback(() => {
        logger.info('Starting new terminal');
        setSelectedSession(null);
        setView('terminal');
    }, []);

    const handleBackToSelector = useCallback(() => {
        logger.info('Returning to session selector');
        setView('selector');
        setSelectedSession(null);
        setShowSearch(false);
        setGitBranch('');
        setIsWorktree(false);
    }, []);

    // Handle opening help modal
    const handleOpenHelp = useCallback(() => {
        logger.info('Opening help modal');
        setShowHelpModal(true);
    }, []);

    // Handle font size change (delta: +1 or -1)
    const handleFontSizeChange = useCallback(async (delta) => {
        const newSize = Math.max(MIN_FONT_SIZE, Math.min(MAX_FONT_SIZE, fontSize + delta));
        if (newSize === fontSize) return; // No change (at limit)

        try {
            await SetFontSize(newSize);
            setFontSizeState(newSize);
            logger.info('Font size changed', { from: fontSize, to: newSize });
        } catch (err) {
            logger.error('Failed to set font size:', err);
        }
    }, [fontSize]);

    // Reset font size to default
    const handleFontSizeReset = useCallback(async () => {
        if (fontSize === DEFAULT_FONT_SIZE) return; // Already at default

        try {
            await SetFontSize(DEFAULT_FONT_SIZE);
            setFontSizeState(DEFAULT_FONT_SIZE);
            logger.info('Font size reset to default', { size: DEFAULT_FONT_SIZE });
        } catch (err) {
            logger.error('Failed to reset font size:', err);
        }
    }, [fontSize]);

    // Set font scale on document root so CSS variables in :root can use it
    useEffect(() => {
        const fontScale = fontSize / 14;
        document.documentElement.style.setProperty('--font-scale', fontScale);
        logger.debug('Font scale updated', { fontSize, fontScale });
    }, [fontSize]);

    // Handle keyboard shortcuts
    const handleKeyDown = useCallback((e) => {
        // Don't handle shortcuts when modals with input fields or their own handlers are open
        // This includes modals with text input (CommandMenu, RenameDialog, etc) and modal handlers (HelpModal, Settings)
        if (showHelpModal || showSettings || showCommandMenu || showLabelDialog || showRemotePathInput) {
            return;
        }

        // Don't intercept typing in input fields (fallback check if an input exists outside modals)
        const isTypingInInput = e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA';

        // Move mode keyboard handling - intercept number keys and Escape
        if (moveMode) {
            // Number keys 1-9 to select target pane
            if (e.key >= '1' && e.key <= '9' && !e.metaKey && !e.ctrlKey && !e.altKey) {
                e.preventDefault();
                e.stopPropagation();
                const paneNumber = parseInt(e.key, 10);
                handleMoveToPane(paneNumber);
                return;
            }
            // Escape to cancel move mode
            if (e.key === 'Escape') {
                e.preventDefault();
                e.stopPropagation();
                handleExitMoveMode();
                return;
            }
            // Block other keys during move mode
            return;
        }

        // Check custom shortcuts first (user-defined quick launch)
        // Skip when typing in input fields without modifiers
        const shortcutKey = buildShortcutKey(e);
        if (!isTypingInInput && shortcuts[shortcutKey]) {
            e.preventDefault();
            const fav = shortcuts[shortcutKey];
            logger.info('Custom shortcut triggered', { shortcut: shortcutKey, name: fav.name });
            handleLaunchProject(fav.path, fav.name, fav.tool);
            return;
        }

        // Check saved layout shortcuts (only in terminal view with active tab)
        // Skip when typing in input fields without modifiers
        if (!isTypingInInput && view === 'terminal' && activeTab && savedLayoutShortcuts[shortcutKey]) {
            e.preventDefault();
            const layout = savedLayoutShortcuts[shortcutKey];
            logger.info('Saved layout shortcut triggered', { shortcut: shortcutKey, name: layout.name });
            handleApplySavedLayout(layout);
            return;
        }

        // ? key to open help (only in selector view - Claude uses ? natively in terminal)
        // Skip when typing in input fields
        if (!isTypingInInput && view === 'selector' && !e.metaKey && !e.ctrlKey && e.key === '?') {
            e.preventDefault();
            e.stopPropagation();
            handleOpenHelp();
            return;
        }
        // Helper to check if app shortcut should fire (respects terminal passthrough on macOS)
        const inTerminal = view === 'terminal';
        const appMod = shouldInterceptShortcut(e, inTerminal);

        // Cmd+/ or Ctrl+/ to open help (works in both views)
        if (appMod && e.key === '/') {
            e.preventDefault();
            e.stopPropagation();
            handleOpenHelp();
            return;
        }
        // Cmd+N to open new terminal (in selector view)
        if (hasAppModifier(e) && e.key === 'n' && view === 'selector') {
            e.preventDefault();
            logger.info('Cmd+N pressed - opening new terminal');
            handleNewTerminal();
            return;
        }
        // Cmd+F to open search (only in terminal view)
        // On macOS, Ctrl+F passes through to terminal (forward-char in bash/readline)
        if (appMod && e.key === 'f' && inTerminal) {
            e.preventDefault();
            setShowSearch(true);
            // Always trigger focus - works whether search is opening or already open
            setSearchFocusTrigger(prev => prev + 1);
        }
        // Cmd+K - Open command menu
        // On macOS, Ctrl+K passes through to terminal (kill-line in bash/readline)
        if (appMod && e.key === 'k') {
            e.preventDefault();
            logger.info('Cmd+K pressed - opening command menu');
            setShowCommandMenu(true);
        }
        // Cmd+T - Open new tab (opens command menu to select session/project)
        // On macOS, Ctrl+T passes through to terminal (transpose-chars in bash)
        if (appMod && e.key === 't') {
            e.preventDefault();
            logger.info('Cmd+T pressed - opening command menu for new tab');
            setPaletteNewTabMode(true);
            setShowCommandMenu(true);
        }
        // Cmd+W to close current tab
        // On macOS, Ctrl+W passes through to terminal (delete-word in bash, search in nano)
        if (appMod && e.key === 'w' && inTerminal) {
            e.preventDefault();
            if (activeTabId) {
                logger.info('Cmd+W pressed - closing current tab');
                handleCloseTab(activeTabId);
            } else {
                // Fallback: show confirmation if no tabs
                logger.info('Cmd+W pressed - showing close confirmation');
                setShowCloseConfirm(true);
            }
        }
        // Cmd+1-9 to switch to tab by number
        if (appMod && e.key >= '1' && e.key <= '9') {
            e.preventDefault();
            const tabIndex = parseInt(e.key, 10) - 1;
            if (tabIndex < openTabs.length) {
                const tab = openTabs[tabIndex];
                logger.info('Tab shortcut pressed', { key: e.key, tabId: tab.id });
                handleSwitchTab(tab.id);
            }
        }
        // Cmd+[ for previous tab
        if (appMod && e.key === '[' && openTabs.length > 1) {
            e.preventDefault();
            const currentIndex = openTabs.findIndex(t => t.id === activeTabId);
            if (currentIndex <= 0) return; // Guard: -1 (not found) or 0 (already first)
            const prevTab = openTabs[currentIndex - 1];
            logger.info('Switching to previous tab');
            handleSwitchTab(prevTab.id);
        }
        // Cmd+] for next tab
        if (appMod && e.key === ']' && openTabs.length > 1) {
            e.preventDefault();
            const currentIndex = openTabs.findIndex(t => t.id === activeTabId);
            if (currentIndex === -1 || currentIndex >= openTabs.length - 1) return; // Guard: not found or already last
            const nextTab = openTabs[currentIndex + 1];
            logger.info('Switching to next tab');
            handleSwitchTab(nextTab.id);
        }
        // Cmd+Escape to go back to session selector (skip when any modal is open)
        const anyModalOpen = showSettings || showSearch || showLabelDialog || showCommandMenu;
        if (appMod && e.key === 'Escape' && inTerminal && !anyModalOpen) {
            e.preventDefault();
            handleBackToSelector();
        }
        // Cmd+R to add/edit custom label (only in terminal view with a session)
        // On macOS, Ctrl+R passes through to terminal (reverse-search in bash)
        if (appMod && e.key === 'r' && inTerminal && selectedSession) {
            e.preventDefault();
            logger.info('Cmd+R pressed - opening label dialog');
            setShowLabelDialog(true);
        }
        // Shift+5 (%) to cycle session status filter (only in selector view)
        // Skip when typing in input fields
        if (!isTypingInInput && e.key === '%' && view === 'selector') {
            e.preventDefault();
            handleCycleStatusFilter();
        }
        // Cmd+Shift+H to toggle collapse/expand all groups (only in selector view)
        if (appMod && e.shiftKey && e.key === 'H' && view === 'selector') {
            e.preventDefault();
            logger.info('Cmd+Shift+H pressed - toggling all groups');
            if (sessionSelectorRef.current?.toggleAllGroups) {
                sessionSelectorRef.current.toggleAllGroups();
            }
        }
        // Cmd+, to open settings (macOS standard, works in both views)
        if (appMod && !e.shiftKey && e.key === ',') {
            e.preventDefault();
            handleOpenSettings();
        }

        // ============================================================
        // PANE MANAGEMENT SHORTCUTS (terminal view only)
        // ============================================================

        // Cmd+D - Split pane right (vertical divider)
        if ((e.metaKey || e.ctrlKey) && !e.shiftKey && e.key === 'd' && view === 'terminal') {
            e.preventDefault();
            logger.info('Cmd+D pressed - split right');
            handleSplitPane('vertical');
            return;
        }

        // Cmd+Shift+D - Split pane down (horizontal divider)
        if ((e.metaKey || e.ctrlKey) && e.shiftKey && e.key === 'D' && view === 'terminal') {
            e.preventDefault();
            logger.info('Cmd+Shift+D pressed - split down');
            handleSplitPane('horizontal');
            return;
        }

        // Cmd+Shift+W - Close current pane
        if ((e.metaKey || e.ctrlKey) && e.shiftKey && e.key === 'W' && view === 'terminal') {
            e.preventDefault();
            logger.info('Cmd+Shift+W pressed - close pane');
            handleClosePane();
            return;
        }

        // Cmd+Option+Arrow keys - Navigate between panes
        if ((e.metaKey || e.ctrlKey) && e.altKey && view === 'terminal') {
            if (e.key === 'ArrowLeft') {
                e.preventDefault();
                handleNavigatePane('left');
                return;
            }
            if (e.key === 'ArrowRight') {
                e.preventDefault();
                handleNavigatePane('right');
                return;
            }
            if (e.key === 'ArrowUp') {
                e.preventDefault();
                handleNavigatePane('up');
                return;
            }
            if (e.key === 'ArrowDown') {
                e.preventDefault();
                handleNavigatePane('down');
                return;
            }
        }

        // Cmd+Option+[ and Cmd+Option+] - Cycle through panes
        if ((e.metaKey || e.ctrlKey) && e.altKey && e.key === '[' && view === 'terminal') {
            e.preventDefault();
            handleCyclicNavigatePane('prev');
            return;
        }
        if ((e.metaKey || e.ctrlKey) && e.altKey && e.key === ']' && view === 'terminal') {
            e.preventDefault();
            handleCyclicNavigatePane('next');
            return;
        }

        // Cmd+Shift+Z - Toggle zoom on current pane
        if ((e.metaKey || e.ctrlKey) && e.shiftKey && e.key === 'Z' && view === 'terminal') {
            e.preventDefault();
            logger.info('Cmd+Shift+Z pressed - toggle zoom');
            handleToggleZoom();
            return;
        }

        // Escape - Exit zoom mode (if zoomed)
        if (e.key === 'Escape' && view === 'terminal' && activeTab?.zoomedPaneId) {
            e.preventDefault();
            handleExitZoom();
            return;
        }

        // Cmd+Option+= - Balance pane sizes
        if ((e.metaKey || e.ctrlKey) && e.altKey && e.key === '=' && view === 'terminal') {
            e.preventDefault();
            handleBalancePanes();
            return;
        }

        // Layout presets: Cmd+Option+1/2/3/4
        if ((e.metaKey || e.ctrlKey) && e.altKey && view === 'terminal') {
            if (e.key === '1') {
                e.preventDefault();
                handleApplyPreset('single');
                return;
            }
            if (e.key === '2') {
                e.preventDefault();
                handleApplyPreset('2-col');
                return;
            }
            if (e.key === '3') {
                e.preventDefault();
                handleApplyPreset('2-row');
                return;
            }
            if (e.key === '4') {
                e.preventDefault();
                handleApplyPreset('2x2');
                return;
            }
        }

        // Cmd++ (Cmd+=) to increase font size (works everywhere)
        if (appMod && (e.key === '=' || e.key === '+')) {
            e.preventDefault();
            handleFontSizeChange(1);
        }
        // Cmd+- to decrease font size (works everywhere)
        if (appMod && e.key === '-') {
            e.preventDefault();
            handleFontSizeChange(-1);
        }
        // Cmd+0 to reset font size to default (works everywhere)
        if (appMod && e.key === '0') {
            e.preventDefault();
            handleFontSizeReset();
        }
    }, [view, showSearch, showHelpModal, showSettings, showLabelDialog, showCommandMenu, showRemotePathInput, handleBackToSelector, buildShortcutKey, shortcuts, savedLayoutShortcuts, handleLaunchProject, handleApplySavedLayout, handleCycleStatusFilter, handleOpenHelp, handleNewTerminal, handleOpenSettings, selectedSession, activeTabId, openTabs, handleCloseTab, handleSwitchTab, handleFontSizeChange, handleFontSizeReset, activeTab, handleSplitPane, handleClosePane, handleNavigatePane, handleCyclicNavigatePane, handleToggleZoom, handleExitZoom, handleBalancePanes, handleApplyPreset, moveMode, handleMoveToPane, handleExitMoveMode]);

    useEffect(() => {
        // Use capture phase to intercept keys before terminal swallows them
        document.addEventListener('keydown', handleKeyDown, true);
        return () => document.removeEventListener('keydown', handleKeyDown, true);
    }, [handleKeyDown]);

    // Show session selector
    if (view === 'selector') {
        return (
            <div id="App">
                {showQuickLaunch && (
                    <UnifiedTopBar
                        key={quickLaunchKey}
                        onLaunch={handleLaunchProject}
                        onShowToolPicker={handleShowToolPicker}
                        onOpenPalette={() => setShowCommandMenu(true)}
                        onOpenPaletteForPinning={handleOpenPaletteForPinning}
                        onShortcutsChanged={loadShortcuts}
                        openTabs={openTabs}
                        activeTabId={activeTabId}
                        onSwitchTab={handleSwitchTab}
                        onCloseTab={handleCloseTab}
                        onTabLabelUpdated={handleTabLabelUpdated}
                    />
                )}
                <SessionSelector
                    ref={sessionSelectorRef}
                    onSelect={handleSelectSession}
                    onNewTerminal={handleNewTerminal}
                    statusFilter={statusFilter}
                    onCycleFilter={handleCycleStatusFilter}
                    onOpenPalette={() => setShowCommandMenu(true)}
                    onOpenHelp={handleOpenHelp}
                />
                {showCommandMenu && (
                    <CommandMenu
                        onClose={handleClosePalette}
                        onSelectSession={handleSelectSession}
                        onAction={handlePaletteAction}
                        onLaunchProject={handleLaunchProject}
                        onShowToolPicker={handleShowToolPicker}
                        onPinToQuickLaunch={handlePinToQuickLaunch}
                        sessions={sessions}
                        projects={projects}
                        favorites={favorites}
                        pinMode={palettePinMode}
                        newTabMode={paletteNewTabMode}
                    />
                )}
                {showToolPicker && toolPickerProject && (
                    <ToolPicker
                        projectPath={toolPickerProject.path}
                        projectName={toolPickerProject.name}
                        onSelect={handleToolSelected}
                        onSelectWithConfig={handleToolSelectedWithConfig}
                        onCancel={handleCancelToolPicker}
                    />
                )}
                {showConfigPicker && toolPickerProject && configPickerTool && (
                    <ConfigPicker
                        tool={configPickerTool}
                        projectPath={toolPickerProject.path}
                        projectName={toolPickerProject.name}
                        onSelect={handleConfigSelected}
                        onCancel={handleCancelConfigPicker}
                    />
                )}
                {showHostPicker && (
                    <HostPicker
                        onSelect={handleHostSelected}
                        onCancel={handleCancelHostPicker}
                    />
                )}
                {showRemotePathInput && selectedRemoteHost && (
                    <RenameDialog
                        currentName=""
                        title={`Project Path on ${selectedRemoteHost}`}
                        placeholder="/home/user/projects/myproject"
                        onSave={handleRemotePathSubmit}
                        onCancel={handleCancelRemotePathInput}
                    />
                )}
                {showSettings && (
                    <SettingsModal
                        onClose={() => setShowSettings(false)}
                        fontSize={fontSize}
                        onFontSizeChange={(newSize) => setFontSizeState(newSize)}
                        scrollSpeed={scrollSpeed}
                        onScrollSpeedChange={(newSpeed) => setScrollSpeedState(newSpeed)}
                    />
                )}
                {showHelpModal && (
                    <KeyboardHelpModal onClose={() => setShowHelpModal(false)} />
                )}
            </div>
        );
    }

    // Determine what to render in the pane area
    const renderPaneContent = () => {
        if (!activeTab) {
            // No active tab - show empty state
            return (
                <div className="pane-empty">
                    <div className="pane-empty-icon">+</div>
                    <div className="pane-empty-text">No session open</div>
                    <div className="pane-empty-hint">
                        Press <kbd>Cmd</kbd>+<kbd>K</kbd> to open a session
                    </div>
                </div>
            );
        }

        // Determine which layout to render (zoomed or full)
        const layoutToRender = activeTab.zoomedPaneId
            ? findPane(activeTab.layout, activeTab.zoomedPaneId)
            : activeTab.layout;

        if (!layoutToRender) {
            return null;
        }

        // Build pane number map for move mode
        const paneNumberMap = buildPaneNumberMap(activeTab.layout);

        return (
            <PaneLayout
                node={layoutToRender}
                activePaneId={activeTab.activePaneId}
                onPaneFocus={handlePaneFocus}
                onRatioChange={handleRatioChange}
                onPaneSessionSelect={handlePaneSessionSelect}
                terminalRefs={terminalRefs}
                searchRefs={searchRefs}
                fontSize={fontSize}
                scrollSpeed={scrollSpeed}
                moveMode={moveMode}
                paneNumberMap={paneNumberMap}
            />
        );
    };

    // Show terminal
    return (
        <div id="App">
            {showQuickLaunch && (
                <UnifiedTopBar
                    key={quickLaunchKey}
                    onLaunch={handleLaunchProject}
                    onShowToolPicker={handleShowToolPicker}
                    onOpenPalette={() => setShowCommandMenu(true)}
                    onOpenPaletteForPinning={handleOpenPaletteForPinning}
                    onShortcutsChanged={loadShortcuts}
                    openTabs={openTabs}
                    activeTabId={activeTabId}
                    onSwitchTab={handleSwitchTab}
                    onCloseTab={handleCloseTab}
                    onTabLabelUpdated={handleTabLabelUpdated}
                />
            )}
            <div className="terminal-header">
                <button
                    className="back-button"
                    onClick={handleBackToSelector}
                    onMouseEnter={(e) => showBackTooltip(e, 'Back to sessions (⌘Esc)')}
                    onMouseLeave={hideBackTooltip}
                >
                    ← Sessions
                </button>
                <BackTooltip />
                {activeTab && (
                    <div className="session-title-header">
                        <span className="tab-pane-count">
                            {countPanes(activeTab.layout) > 1 && `${countPanes(activeTab.layout)} panes`}
                        </span>
                        {selectedSession && (
                            <>
                                {selectedSession.dangerousMode && (
                                    <span className="header-danger-icon" title="Dangerous mode enabled">!</span>
                                )}
                                {selectedSession.title}
                                {selectedSession.customLabel && (
                                    <span className="header-custom-label">{selectedSession.customLabel}</span>
                                )}
                                {gitBranch && (
                                    <span className={`git-branch${isWorktree ? ' is-worktree' : ''}`}>
                                        <span className="git-branch-icon">{isWorktree ? '🌿' : <BranchIcon size={12} />}</span>
                                        {gitBranch}
                                    </span>
                                )}
                                {selectedSession.launchConfigName && (
                                    <span className="header-config-badge">{selectedSession.launchConfigName}</span>
                                )}
                            </>
                        )}
                        {!selectedSession && (
                            <span style={{ color: 'var(--text-muted)' }}>Empty pane - Cmd+K to open session</span>
                        )}
                    </div>
                )}
            </div>
            <div className="terminal-container">
                {renderPaneContent()}
            </div>
            <ShortcutBar
                view="terminal"
                onBackToSessions={handleBackToSelector}
                onSplitPane={() => handleSplitPane('vertical')}
                onOpenSearch={() => {
                    setShowSearch(true);
                    setSearchFocusTrigger(prev => prev + 1);
                }}
                onOpenPalette={() => setShowCommandMenu(true)}
                onOpenHelp={handleOpenHelp}
                hasPanes={activeTab && countPanes(activeTab.layout) > 1}
            />
            {showSearch && (
                <Search
                    searchAddon={searchRefs.current?.[activeTab?.activePaneId] || searchAddonRef.current}
                    onClose={handleCloseSearch}
                    focusTrigger={searchFocusTrigger}
                />
            )}
            {showCloseConfirm && (
                <div className="modal-overlay" onClick={() => setShowCloseConfirm(false)}>
                    <div className="modal-content" onClick={(e) => e.stopPropagation()}>
                        <h3>Close Terminal?</h3>
                        <p>Are you sure you want to return to the session selector?</p>
                        <div className="modal-buttons">
                            <button className="modal-btn-cancel" onClick={() => setShowCloseConfirm(false)}>
                                Cancel
                            </button>
                            <button className="modal-btn-confirm" onClick={() => {
                                setShowCloseConfirm(false);
                                handleBackToSelector();
                            }}>
                                Close Terminal
                            </button>
                        </div>
                    </div>
                </div>
            )}
            {/* Zoom mode overlay */}
            {activeTab?.zoomedPaneId && (
                <FocusModeOverlay onExit={handleExitZoom} />
            )}
            {/* Move mode overlay */}
            {moveMode && (
                <MoveModeOverlay onExit={handleExitMoveMode} />
            )}
            {/* Save layout modal */}
            {showSaveLayoutModal && (
                <SaveLayoutModal
                    onSave={handleSaveLayout}
                    onClose={() => setShowSaveLayoutModal(false)}
                />
            )}
            {showCommandMenu && (
                <CommandMenu
                    onClose={handleClosePalette}
                    onSelectSession={(session) => {
                        // If we have an active pane without a session, assign to it
                        if (activeTab && !findPane(activeTab.layout, activeTab.activePaneId)?.session) {
                            handleAssignSessionToPane(session);
                        } else {
                            handleSelectSession(session);
                        }
                    }}
                    onAction={handlePaletteAction}
                    onLayoutAction={handleLayoutAction}
                    showLayoutActions={true}
                    savedLayouts={savedLayouts}
                    onLaunchProject={(path, name, tool, config, label) => {
                        // If we have an active empty pane, launch into it
                        // For now, use the default behavior (creates new tab)
                        handleLaunchProject(path, name, tool, config, label);
                    }}
                    onShowToolPicker={handleShowToolPicker}
                    onPinToQuickLaunch={handlePinToQuickLaunch}
                    sessions={sessions}
                    projects={projects}
                    favorites={favorites}
                    pinMode={palettePinMode}
                    newTabMode={paletteNewTabMode}
                />
            )}
            {showToolPicker && toolPickerProject && (
                <ToolPicker
                    projectPath={toolPickerProject.path}
                    projectName={toolPickerProject.name}
                    onSelect={handleToolSelected}
                    onSelectWithConfig={handleToolSelectedWithConfig}
                    onCancel={handleCancelToolPicker}
                />
            )}
            {showConfigPicker && toolPickerProject && configPickerTool && (
                <ConfigPicker
                    tool={configPickerTool}
                    projectPath={toolPickerProject.path}
                    projectName={toolPickerProject.name}
                    onSelect={handleConfigSelected}
                    onCancel={handleCancelConfigPicker}
                />
            )}
            {showHostPicker && (
                <HostPicker
                    onSelect={handleHostSelected}
                    onCancel={handleCancelHostPicker}
                />
            )}
            {showRemotePathInput && selectedRemoteHost && (
                <RenameDialog
                    currentName=""
                    title={`Project Path on ${selectedRemoteHost}`}
                    placeholder="/home/user/projects/myproject"
                    onSave={handleRemotePathSubmit}
                    onCancel={handleCancelRemotePathInput}
                />
            )}
            {showSettings && (
                <SettingsModal
                    onClose={() => setShowSettings(false)}
                    fontSize={fontSize}
                    onFontSizeChange={(newSize) => setFontSizeState(newSize)}
                    scrollSpeed={scrollSpeed}
                    onScrollSpeedChange={(newSpeed) => setScrollSpeedState(newSpeed)}
                />
            )}
            {showHelpModal && (
                <KeyboardHelpModal onClose={() => setShowHelpModal(false)} />
            )}
            {showLabelDialog && selectedSession && (
                <RenameDialog
                    currentName={selectedSession.customLabel || ''}
                    title={selectedSession.customLabel ? 'Edit Custom Label' : 'Add Custom Label'}
                    placeholder="Enter label..."
                    onSave={handleSaveSessionCustomLabel}
                    onCancel={() => setShowLabelDialog(false)}
                />
            )}
        </div>
    );
}

export default App;
