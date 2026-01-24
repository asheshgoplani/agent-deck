import { useState, useEffect, useCallback } from 'react';
import './UnifiedTopBar.css';
import { GetQuickLaunchFavorites, RemoveQuickLaunchFavorite, UpdateQuickLaunchShortcut, UpdateQuickLaunchFavoriteName, UpdateSessionCustomLabel } from '../wailsjs/go/main/App';
import ShortcutEditor from './ShortcutEditor';
import RenameDialog from './RenameDialog';
import SessionTab from './SessionTab';
import { createLogger } from './logger';
import { formatShortcut } from './utils/shortcuts';
import { getToolColor } from './utils/tools';
import ToolIcon from './ToolIcon';
import { useTooltip } from './Tooltip';

const logger = createLogger('UnifiedTopBar');

export default function UnifiedTopBar({
    onLaunch,
    onShowToolPicker,
    onOpenPalette,
    onOpenPaletteForPinning,
    onShortcutsChanged,
    openTabs = [],
    activeTabId = null,
    onSwitchTab,
    onCloseTab,
    onTabLabelUpdated,
}) {
    const [favorites, setFavorites] = useState([]);
    const [contextMenu, setContextMenu] = useState(null);
    const [editingShortcut, setEditingShortcut] = useState(null);
    const [editingName, setEditingName] = useState(null);
    const [tabContextMenu, setTabContextMenu] = useState(null); // { x, y, tab }
    const [labelingTab, setLabelingTab] = useState(null);
    const { show: showTooltip, hide: hideTooltip, Tooltip } = useTooltip();

    // Load favorites
    const loadFavorites = useCallback(async () => {
        try {
            const result = await GetQuickLaunchFavorites();
            logger.info('Loaded favorites', { count: result?.length || 0 });
            setFavorites(result || []);
        } catch (err) {
            logger.error('Failed to load favorites:', err);
        }
    }, []);

    useEffect(() => {
        loadFavorites();
    }, [loadFavorites]);

    const handleFavoriteClick = (fav, e) => {
        if (e.metaKey || e.ctrlKey) {
            logger.info('Cmd+Click on favorite, showing tool picker', { name: fav.name });
            onShowToolPicker?.(fav.path, fav.name);
        } else {
            logger.info('Launching favorite', { name: fav.name, tool: fav.tool });
            onLaunch?.(fav.path, fav.name, fav.tool);
        }
    };

    const handleContextMenu = (e, fav) => {
        e.preventDefault();
        logger.debug('Context menu on favorite', { name: fav.name });
        setContextMenu({
            x: e.clientX,
            y: e.clientY,
            favorite: fav,
        });
    };

    const handleRemoveFavorite = async () => {
        if (!contextMenu?.favorite) return;

        try {
            logger.info('Removing favorite', { path: contextMenu.favorite.path });
            await RemoveQuickLaunchFavorite(contextMenu.favorite.path);
            loadFavorites();
            onShortcutsChanged?.();
        } catch (err) {
            logger.error('Failed to remove favorite:', err);
        }
        setContextMenu(null);
    };

    const handleEditShortcut = () => {
        if (!contextMenu?.favorite) return;
        const fav = contextMenu.favorite;
        logger.info('Opening shortcut editor', { name: fav.name });
        setEditingShortcut({
            path: fav.path,
            name: fav.name,
            shortcut: fav.shortcut || '',
        });
        setContextMenu(null);
    };

    const handleRename = () => {
        if (!contextMenu?.favorite) return;
        const fav = contextMenu.favorite;
        logger.info('Opening rename dialog', { name: fav.name });
        setEditingName({
            path: fav.path,
            name: fav.name,
        });
        setContextMenu(null);
    };

    const handleSaveName = async (newName) => {
        if (!editingName || !newName.trim()) return;

        try {
            logger.info('Saving name', { path: editingName.path, name: newName });
            await UpdateQuickLaunchFavoriteName(editingName.path, newName.trim());
            loadFavorites();
        } catch (err) {
            logger.error('Failed to save name:', err);
        }
        setEditingName(null);
    };

    const handleSaveShortcut = async (newShortcut) => {
        if (!editingShortcut) return;

        try {
            logger.info('Saving shortcut', { path: editingShortcut.path, shortcut: newShortcut });
            await UpdateQuickLaunchShortcut(editingShortcut.path, newShortcut);
            loadFavorites();
            onShortcutsChanged?.();
        } catch (err) {
            logger.error('Failed to save shortcut:', err);
        }
        setEditingShortcut(null);
    };

    // Tooltip content for favorites
    const getFavoriteTooltipContent = useCallback((fav) => {
        return `${fav.name}\n${fav.path}${fav.shortcut ? `\n${formatShortcut(fav.shortcut)}` : ''}`;
    }, []);

    // Build existing shortcuts map for conflict detection
    const existingShortcuts = favorites.reduce((acc, fav) => {
        if (fav.shortcut) {
            acc[fav.shortcut] = fav.path;
        }
        return acc;
    }, {});

    const closeContextMenu = useCallback(() => {
        setContextMenu(null);
    }, []);

    // Tab context menu handlers
    const handleTabContextMenu = useCallback((e, tab) => {
        e.preventDefault();
        hideTooltip();
        const x = Math.min(e.clientX, window.innerWidth - 200);
        const y = Math.min(e.clientY, window.innerHeight - 100);
        setTabContextMenu({ x, y, tab });
    }, [hideTooltip]);

    const closeTabContextMenu = useCallback(() => {
        setTabContextMenu(null);
    }, []);

    const handleTabAddLabel = useCallback(() => {
        if (!tabContextMenu?.tab) return;
        setLabelingTab(tabContextMenu.tab);
        setTabContextMenu(null);
    }, [tabContextMenu]);

    const handleTabRemoveLabel = useCallback(async () => {
        if (!tabContextMenu?.tab) return;
        try {
            await UpdateSessionCustomLabel(tabContextMenu.tab.session.id, '');
            onTabLabelUpdated?.(tabContextMenu.tab.session.id, '');
        } catch (err) {
            logger.error('Failed to remove tab label:', err);
        }
        setTabContextMenu(null);
    }, [tabContextMenu, onTabLabelUpdated]);

    const handleTabSaveLabel = useCallback(async (newLabel) => {
        if (!labelingTab || !newLabel.trim()) return;
        try {
            await UpdateSessionCustomLabel(labelingTab.session.id, newLabel.trim());
            onTabLabelUpdated?.(labelingTab.session.id, newLabel.trim());
        } catch (err) {
            logger.error('Failed to save tab label:', err);
        }
        setLabelingTab(null);
    }, [labelingTab, onTabLabelUpdated]);

    // Close context menu on click outside
    useEffect(() => {
        if (contextMenu) {
            document.addEventListener('click', closeContextMenu);
            return () => document.removeEventListener('click', closeContextMenu);
        }
    }, [contextMenu, closeContextMenu]);

    // Close tab context menu on click outside or Escape
    useEffect(() => {
        if (tabContextMenu) {
            const handleClick = () => setTabContextMenu(null);
            const handleEscape = (e) => { if (e.key === 'Escape') setTabContextMenu(null); };
            document.addEventListener('click', handleClick);
            document.addEventListener('keydown', handleEscape);
            return () => {
                document.removeEventListener('click', handleClick);
                document.removeEventListener('keydown', handleEscape);
            };
        }
    }, [tabContextMenu]);

    const hasFavorites = favorites.length > 0;

    return (
        <div className="unified-top-bar">
            {/* Quick Launch Section */}
            {hasFavorites && (
                <div className="quick-launch-section">
                    {favorites.map((fav) => (
                        <button
                            key={fav.path}
                            className="quick-launch-item"
                            onClick={(e) => handleFavoriteClick(fav, e)}
                            onContextMenu={(e) => handleContextMenu(e, fav)}
                            onMouseEnter={(e) => showTooltip(e, getFavoriteTooltipContent(fav))}
                            onMouseLeave={hideTooltip}
                        >
                            <span
                                className="quick-launch-icon"
                                style={{ backgroundColor: getToolColor(fav.tool) }}
                            >
                                <ToolIcon tool={fav.tool} size={12} />
                            </span>
                            <span className="quick-launch-name">{fav.name}</span>
                            {fav.shortcut && (
                                <span className="quick-launch-shortcut">
                                    {formatShortcut(fav.shortcut)}
                                </span>
                            )}
                        </button>
                    ))}
                    <button
                        className="quick-launch-add"
                        onClick={onOpenPaletteForPinning}
                        title="Add favorite"
                    >
                        +
                    </button>
                </div>
            )}

            {/* Divider - only shown when favorites exist */}
            {hasFavorites && <div className="top-bar-divider" />}

            {/* Session Tabs Section */}
            <div className={`session-tabs-section${!hasFavorites ? ' no-favorites' : ''}`}>
                {openTabs.map((tab, index) => (
                    <SessionTab
                        key={tab.id}
                        tab={tab}
                        index={index}
                        isActive={tab.id === activeTabId}
                        onSwitch={() => onSwitchTab?.(tab.id)}
                        onClose={() => onCloseTab?.(tab.id)}
                        onContextMenu={(e) => handleTabContextMenu(e, tab)}
                    />
                ))}
            </div>

            {/* Palette Button */}
            <button
                className="top-bar-palette-btn"
                onClick={onOpenPalette}
                title="Command Palette (Cmd+K)"
            >
                ⌘K
            </button>

            {/* Context Menu */}
            {contextMenu && (
                <div
                    className="quick-launch-context-menu"
                    style={{ left: contextMenu.x, top: contextMenu.y }}
                    onClick={(e) => e.stopPropagation()}
                >
                    <button onClick={handleRename}>
                        Rename
                    </button>
                    <button onClick={handleEditShortcut}>
                        {contextMenu.favorite?.shortcut ? 'Edit Shortcut' : 'Set Shortcut'}
                    </button>
                    <button onClick={handleRemoveFavorite} className="danger">
                        Remove from Quick Launch
                    </button>
                </div>
            )}

            {/* Shortcut Editor Modal */}
            {editingShortcut && (
                <ShortcutEditor
                    projectName={editingShortcut.name}
                    projectPath={editingShortcut.path}
                    currentShortcut={editingShortcut.shortcut}
                    existingShortcuts={existingShortcuts}
                    onSave={handleSaveShortcut}
                    onCancel={() => setEditingShortcut(null)}
                />
            )}

            {/* Rename Dialog */}
            {editingName && (
                <RenameDialog
                    currentName={editingName.name}
                    onSave={handleSaveName}
                    onCancel={() => setEditingName(null)}
                />
            )}

            {/* Tab Context Menu */}
            {tabContextMenu && (
                <div
                    className="session-tab-context-menu"
                    style={{ left: tabContextMenu.x, top: tabContextMenu.y }}
                    onClick={(e) => e.stopPropagation()}
                >
                    <button onClick={handleTabAddLabel}>
                        {tabContextMenu.tab.session.customLabel ? 'Edit Custom Label' : 'Add Custom Label'}
                    </button>
                    {tabContextMenu.tab.session.customLabel && (
                        <button onClick={handleTabRemoveLabel}>
                            Remove Custom Label
                        </button>
                    )}
                </div>
            )}

            {/* Tab Label Dialog */}
            {labelingTab && (
                <RenameDialog
                    currentName={labelingTab.session.customLabel || ''}
                    title={labelingTab.session.customLabel ? 'Edit Custom Label' : 'Add Custom Label'}
                    placeholder="Enter label..."
                    onSave={handleTabSaveLabel}
                    onCancel={() => setLabelingTab(null)}
                />
            )}

            <Tooltip />
        </div>
    );
}
