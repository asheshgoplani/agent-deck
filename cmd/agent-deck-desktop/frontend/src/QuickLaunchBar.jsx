import { useState, useEffect, useCallback } from 'react';
import './QuickLaunchBar.css';
import { GetQuickLaunchFavorites, RemoveQuickLaunchFavorite, UpdateQuickLaunchShortcut } from '../wailsjs/go/main/App';
import ShortcutEditor from './ShortcutEditor';
import { createLogger } from './logger';

const logger = createLogger('QuickLaunchBar');

export default function QuickLaunchBar({ onLaunch, onShowToolPicker, onOpenPalette, onShortcutsChanged }) {
    const [favorites, setFavorites] = useState([]);
    const [contextMenu, setContextMenu] = useState(null);
    const [editingShortcut, setEditingShortcut] = useState(null); // { path, name, shortcut }

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

    const getToolIcon = (tool) => {
        switch (tool) {
            case 'claude': return 'C';
            case 'gemini': return 'G';
            case 'opencode': return 'O';
            default: return '$';
        }
    };

    const getToolColor = (tool) => {
        switch (tool) {
            case 'claude': return '#4cc9f0';
            case 'gemini': return '#ffe66d';
            case 'opencode': return '#6c757d';
            default: return '#6c757d';
        }
    };

    const formatShortcut = (s) => {
        if (!s) return '';
        return s
            .replace(/cmd/g, '⌘')
            .replace(/ctrl/g, '⌃')
            .replace(/shift/g, '⇧')
            .replace(/alt/g, '⌥')
            .replace(/\+/g, '')
            .toUpperCase();
    };

    const handleClick = (fav, e) => {
        if (e.metaKey || e.ctrlKey) {
            // Cmd+Click shows tool picker
            logger.info('Cmd+Click on favorite, showing tool picker', { name: fav.name });
            onShowToolPicker?.(fav.path, fav.name);
        } else {
            // Normal click launches with configured tool
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

    // Close context menu on click outside
    useEffect(() => {
        if (contextMenu) {
            document.addEventListener('click', closeContextMenu);
            return () => document.removeEventListener('click', closeContextMenu);
        }
    }, [contextMenu, closeContextMenu]);

    if (favorites.length === 0) {
        return null; // Don't show bar if no favorites
    }

    return (
        <div className="quick-launch-bar">
            <div className="quick-launch-items">
                {favorites.map((fav) => (
                    <button
                        key={fav.path}
                        className="quick-launch-item"
                        onClick={(e) => handleClick(fav, e)}
                        onContextMenu={(e) => handleContextMenu(e, fav)}
                        title={`${fav.name} (${fav.path})\nClick: Launch with ${fav.tool}\nCmd+Click: Choose tool\nRight-click: Menu${fav.shortcut ? `\nShortcut: ${fav.shortcut}` : ''}`}
                    >
                        <span
                            className="quick-launch-icon"
                            style={{ backgroundColor: getToolColor(fav.tool) }}
                        >
                            {getToolIcon(fav.tool)}
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
                    onClick={onOpenPalette}
                    title="Add favorite (Cmd+K)"
                >
                    +
                </button>
            </div>
            <button
                className="quick-launch-palette-btn"
                onClick={onOpenPalette}
                title="Command Palette (Cmd+K)"
            >
                ⌘K
            </button>

            {contextMenu && (
                <div
                    className="quick-launch-context-menu"
                    style={{ left: contextMenu.x, top: contextMenu.y }}
                    onClick={(e) => e.stopPropagation()}
                >
                    <button onClick={handleEditShortcut}>
                        {contextMenu.favorite?.shortcut ? 'Edit Shortcut' : 'Set Shortcut'}
                    </button>
                    <button onClick={handleRemoveFavorite} className="danger">
                        Remove from Quick Launch
                    </button>
                </div>
            )}

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
        </div>
    );
}
