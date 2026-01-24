import { useState, useEffect, useCallback, useRef } from 'react';
import './QuickLaunchBar.css';
import { GetQuickLaunchFavorites, RemoveQuickLaunchFavorite, UpdateQuickLaunchShortcut, UpdateQuickLaunchFavoriteName } from '../wailsjs/go/main/App';
import ShortcutEditor from './ShortcutEditor';
import { createLogger } from './logger';
import { formatShortcut } from './utils/shortcuts';
import { getToolColor } from './utils/tools';
import ToolIcon from './ToolIcon';
import { useTooltip } from './Tooltip';

const logger = createLogger('QuickLaunchBar');

export default function QuickLaunchBar({ onLaunch, onShowToolPicker, onOpenPalette, onOpenPaletteForPinning, onShortcutsChanged }) {
    const [favorites, setFavorites] = useState([]);
    const [contextMenu, setContextMenu] = useState(null);
    const [editingShortcut, setEditingShortcut] = useState(null); // { path, name, shortcut }
    const [editingName, setEditingName] = useState(null); // { path, name }
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

    // Tooltip content builder
    const getTooltipContent = useCallback((fav) => {
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
                        onMouseEnter={(e) => showTooltip(e, getTooltipContent(fav))}
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

            {editingName && (
                <RenameDialog
                    currentName={editingName.name}
                    onSave={handleSaveName}
                    onCancel={() => setEditingName(null)}
                />
            )}

            <Tooltip />
        </div>
    );
}

// Simple rename dialog component
function RenameDialog({ currentName, onSave, onCancel }) {
    const [name, setName] = useState(currentName);
    const inputRef = useRef(null);

    useEffect(() => {
        // Focus and select all text on mount
        if (inputRef.current) {
            inputRef.current.focus();
            inputRef.current.select();
        }
    }, []);

    const handleKeyDown = (e) => {
        if (e.key === 'Enter') {
            e.preventDefault();
            onSave(name);
        } else if (e.key === 'Escape') {
            e.preventDefault();
            onCancel();
        }
    };

    return (
        <div className="rename-dialog-overlay" onClick={onCancel}>
            <div className="rename-dialog" onClick={(e) => e.stopPropagation()}>
                <div className="rename-dialog-title">Rename</div>
                <input
                    ref={inputRef}
                    type="text"
                    className="rename-dialog-input"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    onKeyDown={handleKeyDown}
                />
                <div className="rename-dialog-buttons">
                    <button className="rename-dialog-cancel" onClick={onCancel}>
                        Cancel
                    </button>
                    <button
                        className="rename-dialog-save"
                        onClick={() => onSave(name)}
                        disabled={!name.trim()}
                    >
                        Save
                    </button>
                </div>
            </div>
        </div>
    );
}
