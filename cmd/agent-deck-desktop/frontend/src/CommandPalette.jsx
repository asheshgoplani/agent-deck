import { useState, useEffect, useRef, useMemo } from 'react';
import Fuse from 'fuse.js';
import './CommandPalette.css';
import { createLogger } from './logger';
import ToolIcon from './ToolIcon';
import { formatShortcut } from './utils/shortcuts';
import RenameDialog from './RenameDialog';

const logger = createLogger('CommandPalette');

// Quick actions available in the palette
const QUICK_ACTIONS = [
    { id: 'new-terminal', type: 'action', title: 'New Terminal', description: 'Start a new shell session' },
    { id: 'refresh-sessions', type: 'action', title: 'Refresh Sessions', description: 'Reload session list' },
    { id: 'toggle-quick-launch', type: 'action', title: 'Toggle Quick Launch Bar', description: 'Show/hide quick launch bar' },
];

// Layout actions - only shown when in terminal view
const LAYOUT_ACTIONS = [
    { id: 'split-right', type: 'layout', title: 'Split Right', description: 'Split pane vertically (Cmd+D)', shortcutHint: '⌘D' },
    { id: 'split-down', type: 'layout', title: 'Split Down', description: 'Split pane horizontally (Cmd+Shift+D)', shortcutHint: '⌘⇧D' },
    { id: 'close-pane', type: 'layout', title: 'Close Pane', description: 'Close current pane (Cmd+Option+W)', shortcutHint: '⌘⌥W' },
    { id: 'move-to-pane', type: 'layout', title: 'Move Session to Pane...', description: 'Swap sessions between panes (press number)', shortcutHint: '1-9' },
    { id: 'balance-panes', type: 'layout', title: 'Balance Panes', description: 'Make all panes equal size (Cmd+Option+=)', shortcutHint: '⌘⌥=' },
    { id: 'toggle-zoom', type: 'layout', title: 'Toggle Zoom', description: 'Zoom/unzoom current pane (Cmd+Shift+Z)', shortcutHint: '⌘⇧Z' },
    { id: 'save-layout', type: 'layout', title: 'Save Current Layout...', description: 'Save this layout as a template' },
    { id: 'layout-single', type: 'layout', title: 'Layout: Single Pane', description: 'Single pane layout (Cmd+Option+1)', shortcutHint: '⌘⌥1' },
    { id: 'layout-2-col', type: 'layout', title: 'Layout: 2 Columns', description: 'Two columns side by side (Cmd+Option+2)', shortcutHint: '⌘⌥2' },
    { id: 'layout-2-row', type: 'layout', title: 'Layout: 2 Rows', description: 'Two rows stacked (Cmd+Option+3)', shortcutHint: '⌘⌥3' },
    { id: 'layout-2x2', type: 'layout', title: 'Layout: 2x2 Grid', description: 'Four panes in a grid (Cmd+Option+4)', shortcutHint: '⌘⌥4' },
];

export default function CommandPalette({
    onClose,
    onSelectSession,
    onAction,
    onLaunchProject, // (projectPath, projectName, tool, configKey?, customLabel?) => void
    onShowToolPicker, // (projectPath, projectName) => void - for Cmd+Enter
    onPinToQuickLaunch, // (projectPath, projectName) => void - for Cmd+P
    onLayoutAction, // (actionId) => void - for layout actions
    sessions = [],
    projects = [],
    favorites = [], // Quick launch favorites for shortcut hints
    savedLayouts = [], // Saved layout templates
    pinMode = false, // When true, selecting pins instead of launching
    showLayoutActions = false, // Show layout commands (when in terminal view)
    newTabMode = false, // When true, opened via Cmd+T for new tab
}) {
    const [query, setQuery] = useState('');
    const [selectedIndex, setSelectedIndex] = useState(0);
    const [labelingProject, setLabelingProject] = useState(null); // { path, name } - for Shift+Enter
    const inputRef = useRef(null);
    const listRef = useRef(null);

    // Log when palette opens
    useEffect(() => {
        logger.info('Command palette opened', { sessionCount: sessions.length, projectCount: projects.length });
        return () => logger.info('Command palette closed');
    }, [sessions.length, projects.length]);

    // Build lookup for favorites by path
    const favoritesLookup = useMemo(() => {
        const lookup = {};
        for (const fav of favorites) {
            lookup[fav.path] = fav;
        }
        return lookup;
    }, [favorites]);

    // Convert projects to palette items
    const projectItems = useMemo(() => {
        return projects
            .filter(p => !p.hasSession) // Only show projects without existing sessions
            .map(p => ({
                id: `project:${p.path}`,
                type: 'project',
                title: p.name,
                projectPath: p.path,
                score: p.score,
                description: p.path,
                isPinned: !!favoritesLookup[p.path],
                shortcut: favoritesLookup[p.path]?.shortcut,
            }));
    }, [projects, favoritesLookup]);

    // Convert saved layouts to action format
    const savedLayoutItems = useMemo(() => {
        return savedLayouts.map(layout => ({
            id: `saved-layout:${layout.id}`,
            type: 'saved-layout',
            title: `Layout: ${layout.name}`,
            description: 'Apply saved layout',
            shortcutHint: layout.shortcut || null,
            layoutId: layout.id,
        }));
    }, [savedLayouts]);

    // Build list of all actions including layout actions if enabled
    const allActions = useMemo(() => {
        if (!showLayoutActions) return QUICK_ACTIONS;
        return [...QUICK_ACTIONS, ...LAYOUT_ACTIONS, ...savedLayoutItems];
    }, [showLayoutActions, savedLayoutItems]);

    // Fuse.js configuration for fuzzy search
    const fuse = useMemo(() => new Fuse([...sessions, ...projectItems, ...allActions], {
        keys: [
            { name: 'title', weight: 0.4 },
            { name: 'projectPath', weight: 0.3 },
            { name: 'tool', weight: 0.1 },
            { name: 'description', weight: 0.2 },
            { name: 'name', weight: 0.4 },
        ],
        threshold: 0.4,
        includeScore: true,
    }), [sessions, projectItems, allActions]);

    // Get filtered results
    const results = useMemo(() => {
        if (pinMode) {
            // Pin mode: only show projects (not already pinned)
            const unpinnedProjects = projectItems.filter(p => !p.isPinned);
            if (!query.trim()) {
                logger.debug('Pin mode: showing unpinned projects', { count: unpinnedProjects.length });
                return unpinnedProjects;
            }
            // Filter with fuse but only from unpinned projects
            const pinFuse = new Fuse(unpinnedProjects, {
                keys: [{ name: 'title', weight: 0.5 }, { name: 'projectPath', weight: 0.5 }],
                threshold: 0.4,
            });
            return pinFuse.search(query).map(r => r.item);
        }

        if (!query.trim()) {
            // No query: show sessions first, then projects, then actions
            const allItems = [...sessions, ...projectItems, ...allActions];
            logger.debug('Showing all items (no query)', { count: allItems.length });
            return allItems;
        }
        const searchResults = fuse.search(query).map(result => result.item);
        logger.debug('Search results', { query, count: searchResults.length });
        return searchResults;
    }, [query, fuse, sessions, projectItems, allActions, pinMode]);

    // Reset selection when results change
    useEffect(() => {
        setSelectedIndex(0);
    }, [results]);

    // Focus input on mount
    useEffect(() => {
        inputRef.current?.focus();
    }, []);

    // Scroll selected item into view
    useEffect(() => {
        if (listRef.current) {
            const selectedItem = listRef.current.querySelector('.palette-item.selected');
            if (selectedItem) {
                selectedItem.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
            }
        }
    }, [selectedIndex]);

    const handleKeyDown = (e) => {
        switch (e.key) {
            case 'ArrowDown':
                e.preventDefault();
                setSelectedIndex(prev => {
                    const newIndex = Math.min(prev + 1, results.length - 1);
                    logger.debug('Navigate down', { from: prev, to: newIndex });
                    return newIndex;
                });
                break;
            case 'ArrowUp':
                e.preventDefault();
                setSelectedIndex(prev => {
                    const newIndex = Math.max(prev - 1, 0);
                    logger.debug('Navigate up', { from: prev, to: newIndex });
                    return newIndex;
                });
                break;
            case 'Enter':
                e.preventDefault();
                if (results[selectedIndex]) {
                    const item = results[selectedIndex];
                    // Cmd+Enter on project shows tool picker
                    if ((e.metaKey || e.ctrlKey) && item.type === 'project') {
                        logger.info('Cmd+Enter on project, showing tool picker', { path: item.projectPath });
                        onShowToolPicker?.(item.projectPath, item.title);
                        onClose();
                    } else if (e.shiftKey && item.type === 'project') {
                        // Shift+Enter on project prompts for custom label
                        logger.info('Shift+Enter on project, prompting for label', { path: item.projectPath });
                        setLabelingProject({ path: item.projectPath, name: item.title });
                    } else {
                        logger.info('Enter pressed, selecting item', { index: selectedIndex });
                        handleSelect(item);
                    }
                }
                break;
            case 'p':
            case 'P':
                // Cmd+P to pin project to Quick Launch
                if ((e.metaKey || e.ctrlKey) && results[selectedIndex]?.type === 'project') {
                    e.preventDefault();
                    const item = results[selectedIndex];
                    logger.info('Cmd+P on project, pinning to Quick Launch', { path: item.projectPath });
                    onPinToQuickLaunch?.(item.projectPath, item.title);
                    onClose();
                }
                break;
            case 'Escape':
                e.preventDefault();
                logger.info('Escape pressed, closing palette');
                onClose();
                break;
        }
    };

    const handleSelect = (item) => {
        logger.info('Item selected', {
            title: item.title,
            type: item.type || 'session',
            id: item.id,
            pinMode
        });

        // In pin mode, selecting a project pins it
        if (pinMode && item.type === 'project') {
            logger.info('Pin mode: pinning project', { path: item.projectPath });
            onPinToQuickLaunch?.(item.projectPath, item.title);
            onClose();
            return;
        }

        if (item.type === 'action') {
            logger.info('Executing action', { actionId: item.id });
            onAction?.(item.id);
        } else if (item.type === 'layout' || item.type === 'saved-layout') {
            logger.info('Executing layout action', { actionId: item.id });
            onLayoutAction?.(item.id);
        } else if (item.type === 'project') {
            // Launch project with default tool (Claude)
            logger.info('Launching project with default tool', { path: item.projectPath, tool: 'claude' });
            onLaunchProject?.(item.projectPath, item.title, 'claude');
        } else {
            logger.info('Navigating to session', { sessionId: item.id, title: item.title });
            onSelectSession?.(item);
        }
        onClose();
    };

    const getStatusColor = (status) => {
        switch (status) {
            case 'running': return '#4ecdc4';
            case 'waiting': return '#ffe66d';
            case 'idle': return '#6c757d';
            case 'error': return '#ff6b6b';
            default: return '#6c757d';
        }
    };

    // Handle saving custom label and launching project
    const handleSaveProjectLabel = (customLabel) => {
        if (!labelingProject) return;
        logger.info('Launching project with custom label', { path: labelingProject.path, customLabel });
        onLaunchProject?.(labelingProject.path, labelingProject.name, 'claude', '', customLabel);
        setLabelingProject(null);
        onClose();
    };

    return (
        <div className="palette-overlay" onClick={onClose}>
            <div className="palette-container" onClick={(e) => e.stopPropagation()}>
                <input
                    ref={inputRef}
                    type="text"
                    className="palette-input"
                    placeholder={pinMode ? "Search projects to pin..." : newTabMode ? "Search for a session to open in a new tab..." : "Search sessions or actions..."}
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                    onKeyDown={handleKeyDown}
                />
                <div className="palette-list" ref={listRef}>
                    {results.length === 0 ? (
                        <div className="palette-empty">No results found</div>
                    ) : (
                        results.map((item, index) => (
                            <button
                                key={item.id}
                                className={`palette-item ${index === selectedIndex ? 'selected' : ''} ${item.type || 'session'}`}
                                onClick={() => handleSelect(item)}
                                onMouseEnter={() => setSelectedIndex(index)}
                            >
                                {item.type === 'action' ? (
                                    <>
                                        <span className="palette-action-icon">{'>'}</span>
                                        <div className="palette-item-info">
                                            <div className="palette-item-title">{item.title}</div>
                                            <div className="palette-item-subtitle">{item.description}</div>
                                        </div>
                                    </>
                                ) : item.type === 'layout' ? (
                                    <>
                                        <span className="palette-layout-icon">{'#'}</span>
                                        <div className="palette-item-info">
                                            <div className="palette-item-title">{item.title}</div>
                                            <div className="palette-item-subtitle">{item.description}</div>
                                        </div>
                                        {item.shortcutHint && (
                                            <span className="palette-shortcut-hint">{item.shortcutHint}</span>
                                        )}
                                    </>
                                ) : item.type === 'saved-layout' ? (
                                    <>
                                        <span className="palette-layout-icon saved">{'★'}</span>
                                        <div className="palette-item-info">
                                            <div className="palette-item-title">{item.title}</div>
                                            <div className="palette-item-subtitle">{item.description}</div>
                                        </div>
                                        {item.shortcutHint && (
                                            <span className="palette-shortcut-hint">{item.shortcutHint}</span>
                                        )}
                                    </>
                                ) : item.type === 'project' ? (
                                    <>
                                        <span className="palette-project-icon">{item.isPinned ? '★' : '+'}</span>
                                        <div className="palette-item-info">
                                            <div className="palette-item-title">{item.title}</div>
                                            <div className="palette-item-subtitle">{item.projectPath}</div>
                                        </div>
                                        {item.shortcut && (
                                            <span className="palette-shortcut-hint">
                                                {formatShortcut(item.shortcut)}
                                            </span>
                                        )}
                                        <span className="palette-project-hint">
                                            {pinMode ? 'Enter to pin' : (item.isPinned ? 'Pinned' : '⌘P Pin')}
                                        </span>
                                    </>
                                ) : (
                                    <>
                                        <span
                                            className="palette-tool-icon"
                                            style={{ backgroundColor: getStatusColor(item.status) }}
                                        >
                                            <ToolIcon tool={item.tool} size={14} />
                                        </span>
                                        <div className="palette-item-info">
                                            <div className="palette-item-title">{item.title}</div>
                                            <div className="palette-item-subtitle">
                                                {item.projectPath || item.groupPath || 'ungrouped'}
                                            </div>
                                        </div>
                                        {favoritesLookup[item.projectPath]?.shortcut && (
                                            <span className="palette-shortcut-hint">
                                                {formatShortcut(favoritesLookup[item.projectPath].shortcut)}
                                            </span>
                                        )}
                                        <span
                                            className="palette-status"
                                            style={{ color: getStatusColor(item.status) }}
                                        >
                                            {item.status}
                                        </span>
                                    </>
                                )}
                            </button>
                        ))
                    )}
                </div>
                <div className="palette-footer">
                    <span className="palette-hint"><kbd>↑↓</kbd> Navigate</span>
                    <span className="palette-hint"><kbd>Enter</kbd> {pinMode ? 'Pin to Quick Launch' : 'Select'}</span>
                    {!pinMode && <span className="palette-hint"><kbd>⇧Enter</kbd> Label</span>}
                    {!pinMode && <span className="palette-hint"><kbd>⌘Enter</kbd> Tool picker</span>}
                    {!pinMode && <span className="palette-hint"><kbd>⌘P</kbd> Pin</span>}
                    <span className="palette-hint"><kbd>Esc</kbd> Close</span>
                </div>
            </div>

            {labelingProject && (
                <RenameDialog
                    currentName=""
                    title="Add Custom Label"
                    placeholder="Enter label..."
                    onSave={handleSaveProjectLabel}
                    onCancel={() => setLabelingProject(null)}
                />
            )}
        </div>
    );
}
