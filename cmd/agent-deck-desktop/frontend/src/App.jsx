import { useRef, useState, useEffect, useCallback } from 'react';
import './App.css';
import Terminal from './Terminal';
import Search from './Search';
import SessionSelector from './SessionSelector';
import CommandPalette from './CommandPalette';
import ToolPicker from './ToolPicker';
import ConfigPicker from './ConfigPicker';
import SettingsModal from './SettingsModal';
import UnifiedTopBar from './UnifiedTopBar';
import ShortcutBar from './ShortcutBar';
import KeyboardHelpModal from './KeyboardHelpModal';
import RenameDialog from './RenameDialog';
import { ListSessions, DiscoverProjects, CreateSession, RecordProjectUsage, GetQuickLaunchFavorites, AddQuickLaunchFavorite, GetQuickLaunchBarVisibility, SetQuickLaunchBarVisibility, GetGitBranch, IsGitWorktree, MarkSessionAccessed, GetDefaultLaunchConfig, UpdateSessionCustomLabel, GetFontSize, SetFontSize } from '../wailsjs/go/main/App';
import { createLogger } from './logger';
import { DEFAULT_FONT_SIZE, MIN_FONT_SIZE, MAX_FONT_SIZE } from './constants/terminal';
import { shouldInterceptShortcut, hasAppModifier } from './utils/platform';

const logger = createLogger('App');

function App() {
    const searchAddonRef = useRef(null);
    const [showSearch, setShowSearch] = useState(false);
    const [searchFocusTrigger, setSearchFocusTrigger] = useState(0); // Increments to trigger focus
    const [view, setView] = useState('selector'); // 'selector' or 'terminal'
    const [selectedSession, setSelectedSession] = useState(null);
    const [showCloseConfirm, setShowCloseConfirm] = useState(false);
    const [showCommandPalette, setShowCommandPalette] = useState(false);
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
    const [openTabs, setOpenTabs] = useState([]); // Array of {id, session, openedAt}
    const [activeTabId, setActiveTabId] = useState(null);
    const [fontSize, setFontSizeState] = useState(DEFAULT_FONT_SIZE);
    const sessionSelectorRef = useRef(null);

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
    }, [loadShortcuts]);

    const handleCloseSearch = useCallback(() => {
        setShowSearch(false);
    }, []);

    // Load sessions and projects for command palette
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
        if (showCommandPalette) {
            loadSessionsAndProjects();
        }
    }, [showCommandPalette, loadSessionsAndProjects]);

    // Handle command palette actions
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
            default:
                logger.warn('Unknown palette action:', actionId);
        }
    }, [view, loadSessionsAndProjects]);

    // Tab management handlers - defined early so other handlers can use them
    const handleOpenTab = useCallback((session) => {
        // Check if tab already exists (read state outside updater to keep it pure)
        const existingTab = openTabs.find(t => t.session.id === session.id);
        if (existingTab) {
            // Tab exists, just switch to it
            setActiveTabId(existingTab.id);
            return;
        }
        // Create new tab
        const newTab = {
            id: `tab-${session.id}-${Date.now()}`,
            session,
            openedAt: Date.now(),
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
                    setSelectedSession(newActiveTab.session);
                }
            }

            return newTabs;
        });
    }, [activeTabId]);

    const handleSwitchTab = useCallback(async (tabId) => {
        const tab = openTabs.find(t => t.id === tabId);
        if (!tab) return;

        logger.info('Switching to tab', { tabId, sessionTitle: tab.session.title });
        setActiveTabId(tabId);
        setSelectedSession(tab.session);
        setView('terminal');

        // Update git info for the new session
        if (tab.session.projectPath) {
            try {
                const [branch, worktree] = await Promise.all([
                    GetGitBranch(tab.session.projectPath),
                    IsGitWorktree(tab.session.projectPath)
                ]);
                setGitBranch(branch || '');
                setIsWorktree(worktree);
            } catch (err) {
                setGitBranch('');
                setIsWorktree(false);
            }
        } else {
            setGitBranch('');
            setIsWorktree(false);
        }
    }, [openTabs]);

    // Launch a project with the specified tool and optional config
    // customLabel is optional - if provided, will be set as the session's custom label
    const handleLaunchProject = useCallback(async (projectPath, projectName, tool, configKey = '', customLabel = '') => {
        try {
            logger.info('Launching project', { projectPath, projectName, tool, configKey, customLabel });

            // Create session with config key
            const session = await CreateSession(projectPath, projectName, tool, configKey);
            logger.info('Session created', { sessionId: session.id, tmuxSession: session.tmuxSession });

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

            // Load git branch and worktree status
            try {
                const [branch, worktree] = await Promise.all([
                    GetGitBranch(projectPath),
                    IsGitWorktree(projectPath)
                ]);
                setGitBranch(branch || '');
                setIsWorktree(worktree);
                logger.info('Git info:', { branch: branch || '(not a git repo)', isWorktree: worktree });
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
            // Could show an error toast here
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
        setShowCommandPalette(true);
    }, []);

    // Close palette and reset modes
    const handleClosePalette = useCallback(() => {
        setShowCommandPalette(false);
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
            handleLaunchProject(toolPickerProject.path, toolPickerProject.name, tool, configKey);
        }
        setShowToolPicker(false);
        setToolPickerProject(null);
    }, [toolPickerProject, handleLaunchProject]);

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
            handleLaunchProject(toolPickerProject.path, toolPickerProject.name, configPickerTool, configKey);
        }
        setShowConfigPicker(false);
        setConfigPickerTool(null);
        setToolPickerProject(null);
    }, [toolPickerProject, configPickerTool, handleLaunchProject]);

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

        // Load git branch and worktree status if session has a project path
        if (session.projectPath) {
            try {
                const [branch, worktree] = await Promise.all([
                    GetGitBranch(session.projectPath),
                    IsGitWorktree(session.projectPath)
                ]);
                setGitBranch(branch || '');
                setIsWorktree(worktree);
                logger.info('Git info:', { branch: branch || '(not a git repo)', isWorktree: worktree });
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

    // Calculate font scale for CSS custom property (14px is baseline)
    const fontScale = fontSize / 14;

    // Handle keyboard shortcuts
    const handleKeyDown = useCallback((e) => {
        // Don't handle shortcuts when help modal is open (it has its own handler)
        if (showHelpModal) {
            return;
        }

        // Check custom shortcuts first (user-defined)
        const shortcutKey = buildShortcutKey(e);
        if (shortcuts[shortcutKey]) {
            e.preventDefault();
            const fav = shortcuts[shortcutKey];
            logger.info('Custom shortcut triggered', { shortcut: shortcutKey, name: fav.name });
            handleLaunchProject(fav.path, fav.name, fav.tool);
            return;
        }

        // ? key to open help (only in selector view - Claude uses ? natively in terminal)
        if (view === 'selector' && !e.metaKey && !e.ctrlKey && e.key === '?') {
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
        // Cmd+K - Open command palette
        // On macOS, Ctrl+K passes through to terminal (kill-line in bash/readline)
        if (appMod && e.key === 'k') {
            e.preventDefault();
            logger.info('Cmd+K pressed - opening command palette');
            setShowCommandPalette(true);
        }
        // Cmd+T - Open new tab (opens command palette to select session/project)
        // On macOS, Ctrl+T passes through to terminal (transpose-chars in bash)
        if (appMod && e.key === 't') {
            e.preventDefault();
            logger.info('Cmd+T pressed - opening command palette for new tab');
            setPaletteNewTabMode(true);
            setShowCommandPalette(true);
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
        // Cmd+, to go back to session selector
        if (appMod && e.key === ',' && inTerminal) {
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
        if (e.key === '%' && view === 'selector') {
            e.preventDefault();
            handleCycleStatusFilter();
        }
        // Cmd+Shift+, to open settings (works in both views)
        if (appMod && e.shiftKey && e.key === ',') {
            e.preventDefault();
            handleOpenSettings();
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
    }, [view, showSearch, showHelpModal, handleBackToSelector, buildShortcutKey, shortcuts, handleLaunchProject, handleCycleStatusFilter, handleOpenHelp, handleNewTerminal, handleOpenSettings, selectedSession, activeTabId, openTabs, handleCloseTab, handleSwitchTab, handleFontSizeChange, handleFontSizeReset]);

    useEffect(() => {
        // Use capture phase to intercept keys before terminal swallows them
        document.addEventListener('keydown', handleKeyDown, true);
        return () => document.removeEventListener('keydown', handleKeyDown, true);
    }, [handleKeyDown]);

    // Show session selector
    if (view === 'selector') {
        return (
            <div id="App" style={{ '--font-scale': fontScale }}>
                {showQuickLaunch && (
                    <UnifiedTopBar
                        key={quickLaunchKey}
                        onLaunch={handleLaunchProject}
                        onShowToolPicker={handleShowToolPicker}
                        onOpenPalette={() => setShowCommandPalette(true)}
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
                    onSelect={handleSelectSession}
                    onNewTerminal={handleNewTerminal}
                    statusFilter={statusFilter}
                    onCycleFilter={handleCycleStatusFilter}
                    onOpenPalette={() => setShowCommandPalette(true)}
                    onOpenHelp={handleOpenHelp}
                />
                {showCommandPalette && (
                    <CommandPalette
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
                {showSettings && (
                    <SettingsModal
                        onClose={() => setShowSettings(false)}
                        fontSize={fontSize}
                        onFontSizeChange={(newSize) => setFontSizeState(newSize)}
                    />
                )}
                {showHelpModal && (
                    <KeyboardHelpModal onClose={() => setShowHelpModal(false)} />
                )}
            </div>
        );
    }

    // Show terminal
    return (
        <div id="App" style={{ '--font-scale': fontScale }}>
            {showQuickLaunch && (
                <UnifiedTopBar
                    key={quickLaunchKey}
                    onLaunch={handleLaunchProject}
                    onShowToolPicker={handleShowToolPicker}
                    onOpenPalette={() => setShowCommandPalette(true)}
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
                <button className="back-button" onClick={handleBackToSelector} title="Back to sessions (Cmd+,)">
                    ← Sessions
                </button>
                {selectedSession && (
                    <div className="session-title-header">
                        {selectedSession.dangerousMode && (
                            <span className="header-danger-icon" title="Dangerous mode enabled">⚠</span>
                        )}
                        {selectedSession.title}
                        {selectedSession.customLabel && (
                            <span className="header-custom-label">{selectedSession.customLabel}</span>
                        )}
                        {gitBranch && (
                            <span className={`git-branch${isWorktree ? ' is-worktree' : ''}`}>
                                <span className="git-branch-icon">{isWorktree ? '🌿' : '⎇'}</span>
                                {gitBranch}
                            </span>
                        )}
                        {selectedSession.launchConfigName && (
                            <span className="header-config-badge">{selectedSession.launchConfigName}</span>
                        )}
                    </div>
                )}
            </div>
            <div className="terminal-container">
                <Terminal
                    searchRef={searchAddonRef}
                    session={selectedSession}
                    fontSize={fontSize}
                />
            </div>
            <ShortcutBar
                view="terminal"
                onBackToSessions={handleBackToSelector}
                onOpenSearch={() => {
                    setShowSearch(true);
                    setSearchFocusTrigger(prev => prev + 1);
                }}
                onOpenPalette={() => setShowCommandPalette(true)}
                onOpenHelp={handleOpenHelp}
            />
            {showSearch && (
                <Search
                    searchAddon={searchAddonRef.current}
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
            {showCommandPalette && (
                <CommandPalette
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
            {showSettings && (
                <SettingsModal
                    onClose={() => setShowSettings(false)}
                    fontSize={fontSize}
                    onFontSizeChange={(newSize) => setFontSizeState(newSize)}
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
