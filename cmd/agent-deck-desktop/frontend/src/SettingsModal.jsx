import { useState, useEffect, useCallback, useRef } from 'react';
import './SettingsModal.css';
import LaunchConfigEditor from './LaunchConfigEditor';
import { GetLaunchConfigs, DeleteLaunchConfig, GetSoftNewlineMode, SetSoftNewlineMode, SetFontSize, GetScrollSpeed, SetScrollSpeed, ResetGroupSettings } from '../wailsjs/go/main/App';
import { createLogger } from './logger';
import { TOOLS } from './utils/tools';
import ToolIcon from './ToolIcon';
import { useTheme } from './context/ThemeContext';
import { DEFAULT_FONT_SIZE, MIN_FONT_SIZE, MAX_FONT_SIZE } from './constants/terminal';

const logger = createLogger('SettingsModal');

const DEFAULT_SCROLL_SPEED = 100;
const MIN_SCROLL_SPEED = 50;
const MAX_SCROLL_SPEED = 250;

export default function SettingsModal({ onClose, fontSize = DEFAULT_FONT_SIZE, onFontSizeChange, scrollSpeed = DEFAULT_SCROLL_SPEED, onScrollSpeedChange }) {
    const [configs, setConfigs] = useState([]);
    const [loading, setLoading] = useState(true);
    const [editingConfig, setEditingConfig] = useState(null); // null = list view, object = editing
    const [creatingForTool, setCreatingForTool] = useState(null); // tool name when creating new
    const [softNewlineMode, setSoftNewlineMode] = useState('both');
    const { themePreference, setTheme } = useTheme();
    const containerRef = useRef(null);

    // Focus the modal container on mount so Escape works immediately
    useEffect(() => {
        if (containerRef.current) {
            containerRef.current.focus();
        }
    }, []);

    // Load configs and terminal settings on mount
    useEffect(() => {
        loadConfigs();
        loadTerminalSettings();
    }, []);

    const loadTerminalSettings = async () => {
        try {
            const mode = await GetSoftNewlineMode();
            setSoftNewlineMode(mode || 'both');
            logger.info('Loaded soft newline mode:', mode);
        } catch (err) {
            logger.error('Failed to load terminal settings:', err);
        }
        // Font size is passed as prop from App.jsx, no need to load here
    };

    const handleSoftNewlineModeChange = async (mode) => {
        try {
            await SetSoftNewlineMode(mode);
            setSoftNewlineMode(mode);
            logger.info('Set soft newline mode:', mode);
        } catch (err) {
            logger.error('Failed to set soft newline mode:', err);
            alert('Failed to save setting: ' + err.message);
        }
    };

    const handleFontSizeChange = async (delta) => {
        const newSize = Math.max(MIN_FONT_SIZE, Math.min(MAX_FONT_SIZE, fontSize + delta));
        if (newSize === fontSize) return;
        try {
            await SetFontSize(newSize);
            if (onFontSizeChange) onFontSizeChange(newSize);
            logger.info('Set font size:', newSize);
        } catch (err) {
            logger.error('Failed to set font size:', err);
            alert('Failed to save setting: ' + err.message);
        }
    };

    const handleFontSizeReset = async () => {
        try {
            await SetFontSize(DEFAULT_FONT_SIZE);
            if (onFontSizeChange) onFontSizeChange(DEFAULT_FONT_SIZE);
            logger.info('Reset font size to default');
        } catch (err) {
            logger.error('Failed to reset font size:', err);
            alert('Failed to save setting: ' + err.message);
        }
    };

    const handleScrollSpeedChange = async (newSpeed) => {
        const clampedSpeed = Math.max(MIN_SCROLL_SPEED, Math.min(MAX_SCROLL_SPEED, newSpeed));
        if (clampedSpeed === scrollSpeed) return;
        try {
            await SetScrollSpeed(clampedSpeed);
            if (onScrollSpeedChange) onScrollSpeedChange(clampedSpeed);
            logger.info('Set scroll speed:', clampedSpeed);
        } catch (err) {
            logger.error('Failed to set scroll speed:', err);
            alert('Failed to save setting: ' + err.message);
        }
    };

    const handleScrollSpeedReset = async () => {
        try {
            await SetScrollSpeed(DEFAULT_SCROLL_SPEED);
            if (onScrollSpeedChange) onScrollSpeedChange(DEFAULT_SCROLL_SPEED);
            logger.info('Reset scroll speed to default');
        } catch (err) {
            logger.error('Failed to reset scroll speed:', err);
            alert('Failed to save setting: ' + err.message);
        }
    };

    const handleResetGroupSettings = async () => {
        if (!confirm('Reset all group expand/collapse settings to TUI defaults?')) return;
        try {
            await ResetGroupSettings();
            logger.info('Reset group settings to TUI defaults');
            alert('Group settings reset. Refresh the session list to see changes.');
        } catch (err) {
            logger.error('Failed to reset group settings:', err);
            alert('Failed to reset settings: ' + err.message);
        }
    };

    const loadConfigs = async () => {
        try {
            setLoading(true);
            const result = await GetLaunchConfigs();
            setConfigs(result || []);
            logger.info('Loaded launch configs', { count: result?.length || 0 });
        } catch (err) {
            logger.error('Failed to load launch configs:', err);
        } finally {
            setLoading(false);
        }
    };

    // Handle keyboard shortcuts
    const handleKeyDown = useCallback((e) => {
        if (e.key === 'Escape') {
            e.preventDefault();
            e.stopPropagation();
            if (editingConfig || creatingForTool) {
                // Close editor, go back to list
                setEditingConfig(null);
                setCreatingForTool(null);
            } else {
                // Close modal
                onClose();
            }
        }
    }, [editingConfig, creatingForTool, onClose]);

    useEffect(() => {
        // Use capture phase to intercept before xterm can swallow the event
        document.addEventListener('keydown', handleKeyDown, true);
        return () => document.removeEventListener('keydown', handleKeyDown, true);
    }, [handleKeyDown]);

    // Group configs by tool
    const configsByTool = {};
    TOOLS.forEach(tool => {
        configsByTool[tool.id] = configs.filter(c => c.tool === tool.id);
    });

    const handleEdit = (config) => {
        logger.info('Editing config', { key: config.key });
        setEditingConfig(config);
    };

    const handleDelete = async (config) => {
        if (!confirm(`Delete "${config.name}"?`)) return;

        try {
            await DeleteLaunchConfig(config.key);
            logger.info('Deleted config', { key: config.key });
            await loadConfigs();
        } catch (err) {
            logger.error('Failed to delete config:', err);
            alert('Failed to delete config: ' + err.message);
        }
    };

    const handleCreate = (toolId) => {
        logger.info('Creating new config for tool', { tool: toolId });
        setCreatingForTool(toolId);
    };

    const handleEditorSave = async () => {
        setEditingConfig(null);
        setCreatingForTool(null);
        await loadConfigs();
    };

    const handleEditorCancel = () => {
        setEditingConfig(null);
        setCreatingForTool(null);
    };

    // Show editor if editing or creating
    if (editingConfig || creatingForTool) {
        return (
            <div className="settings-overlay" onClick={onClose}>
                <div className="settings-container" onClick={(e) => e.stopPropagation()}>
                    <LaunchConfigEditor
                        config={editingConfig}
                        tool={creatingForTool || editingConfig?.tool}
                        onSave={handleEditorSave}
                        onCancel={handleEditorCancel}
                    />
                </div>
            </div>
        );
    }

    return (
        <div className="settings-overlay" onClick={onClose}>
            <div className="settings-container" ref={containerRef} tabIndex={-1} onClick={(e) => e.stopPropagation()}>
                <div className="settings-header">
                    <h2>Settings</h2>
                    <button className="settings-close" onClick={onClose}>
                        &times;
                    </button>
                </div>

                <div className="settings-content">
                    {/* Theme Section */}
                    <div className="settings-theme-section">
                        <div className="settings-theme-header">
                            <span className="settings-theme-icon">🎨</span>
                            <h3>Appearance</h3>
                        </div>
                        <div className="settings-theme-options">
                            <button
                                className={`settings-theme-option ${themePreference === 'dark' ? 'active' : ''}`}
                                onClick={() => setTheme('dark')}
                            >
                                <span className="settings-theme-option-icon">🌙</span>
                                Dark
                            </button>
                            <button
                                className={`settings-theme-option ${themePreference === 'light' ? 'active' : ''}`}
                                onClick={() => setTheme('light')}
                            >
                                <span className="settings-theme-option-icon">☀️</span>
                                Light
                            </button>
                            <button
                                className={`settings-theme-option ${themePreference === 'auto' ? 'active' : ''}`}
                                onClick={() => setTheme('auto')}
                            >
                                <span className="settings-theme-option-icon">💻</span>
                                Auto
                            </button>
                        </div>
                    </div>

                    {/* Terminal Input Settings */}
                    <div className="settings-theme-section">
                        <div className="settings-theme-header">
                            <span className="settings-theme-icon">⌨️</span>
                            <h3>Terminal Input</h3>
                        </div>
                        <div className="settings-input-description">
                            Soft newline inserts a new line without executing the command.
                            You can also use <code>\</code> at the end of a line (backslash continuation).
                        </div>
                        <div className="settings-theme-options settings-input-options">
                            <button
                                className={`settings-theme-option ${softNewlineMode === 'both' ? 'active' : ''}`}
                                onClick={() => handleSoftNewlineModeChange('both')}
                                title="Both Shift+Enter and Option/Alt+Enter insert newline"
                            >
                                Both
                            </button>
                            <button
                                className={`settings-theme-option ${softNewlineMode === 'shift_enter' ? 'active' : ''}`}
                                onClick={() => handleSoftNewlineModeChange('shift_enter')}
                                title="Only Shift+Enter inserts newline"
                            >
                                Shift+Enter
                            </button>
                            <button
                                className={`settings-theme-option ${softNewlineMode === 'alt_enter' ? 'active' : ''}`}
                                onClick={() => handleSoftNewlineModeChange('alt_enter')}
                                title="Only Option/Alt+Enter inserts newline"
                            >
                                Opt+Enter
                            </button>
                            <button
                                className={`settings-theme-option ${softNewlineMode === 'disabled' ? 'active' : ''}`}
                                onClick={() => handleSoftNewlineModeChange('disabled')}
                                title="Disable soft newline (use backslash continuation instead)"
                            >
                                Disabled
                            </button>
                        </div>
                    </div>

                    {/* Font Size Settings */}
                    <div className="settings-theme-section">
                        <div className="settings-theme-header">
                            <span className="settings-theme-icon">🔤</span>
                            <h3>Font Size</h3>
                        </div>
                        <div className="settings-input-description">
                            Terminal font size. Use <kbd>⌘+</kbd> / <kbd>⌘-</kbd> to adjust while in terminal.
                        </div>
                        <div className="settings-font-size-controls">
                            <button
                                className="settings-font-btn"
                                onClick={() => handleFontSizeChange(-1)}
                                disabled={fontSize <= MIN_FONT_SIZE}
                                title="Decrease font size"
                            >
                                −
                            </button>
                            <span className="settings-font-size-value">{fontSize}px</span>
                            <button
                                className="settings-font-btn"
                                onClick={() => handleFontSizeChange(1)}
                                disabled={fontSize >= MAX_FONT_SIZE}
                                title="Increase font size"
                            >
                                +
                            </button>
                            <button
                                className="settings-font-reset-btn"
                                onClick={handleFontSizeReset}
                                disabled={fontSize === DEFAULT_FONT_SIZE}
                                title={`Reset to default (${DEFAULT_FONT_SIZE}px)`}
                            >
                                Reset
                            </button>
                        </div>
                    </div>

                    {/* Scroll Speed Settings */}
                    <div className="settings-theme-section">
                        <div className="settings-theme-header">
                            <span className="settings-theme-icon">🖱️</span>
                            <h3>Scroll Speed</h3>
                        </div>
                        <div className="settings-input-description">
                            Mouse and trackpad scroll speed in terminal. Higher values scroll faster.
                        </div>
                        <div className="settings-scroll-speed-controls">
                            <input
                                type="range"
                                className="settings-scroll-slider"
                                min={MIN_SCROLL_SPEED}
                                max={MAX_SCROLL_SPEED}
                                step={10}
                                value={scrollSpeed}
                                onChange={(e) => handleScrollSpeedChange(parseInt(e.target.value, 10))}
                                title={`Scroll speed: ${scrollSpeed}%`}
                            />
                            <span className="settings-scroll-speed-value">{scrollSpeed}%</span>
                            <button
                                className="settings-font-reset-btn"
                                onClick={handleScrollSpeedReset}
                                disabled={scrollSpeed === DEFAULT_SCROLL_SPEED}
                                title="Reset to default (100%)"
                            >
                                Reset
                            </button>
                        </div>
                    </div>

                    {/* Group Settings */}
                    <div className="settings-theme-section">
                        <div className="settings-theme-header">
                            <span className="settings-theme-icon">📁</span>
                            <h3>Session Groups</h3>
                        </div>
                        <div className="settings-input-description">
                            Group expand/collapse state is saved separately from the TUI.
                            Reset to use the expand/collapse state from Agent Deck TUI.
                        </div>
                        <div className="settings-group-controls">
                            <button
                                className="settings-reset-groups-btn"
                                onClick={handleResetGroupSettings}
                                title="Reset group expand/collapse settings to TUI defaults"
                            >
                                Reset to TUI Defaults
                            </button>
                        </div>
                    </div>

                    {/* Launch Configurations */}
                    {loading ? (
                        <div className="settings-loading">Loading...</div>
                    ) : (
                        TOOLS.map(tool => (
                            <div key={tool.id} className="settings-tool-section">
                                <div className="settings-tool-header">
                                    <span
                                        className="settings-tool-icon"
                                        style={{ backgroundColor: tool.color }}
                                    >
                                        <ToolIcon tool={tool.id} size={14} />
                                    </span>
                                    <h3>{tool.name}</h3>
                                    <button
                                        className="settings-add-btn"
                                        onClick={() => handleCreate(tool.id)}
                                        title={`Add ${tool.name} configuration`}
                                    >
                                        +
                                    </button>
                                </div>

                                {configsByTool[tool.id].length === 0 ? (
                                    <div className="settings-empty">
                                        No configurations. Click + to add one.
                                    </div>
                                ) : (
                                    <div className="settings-config-list">
                                        {configsByTool[tool.id].map(config => (
                                            <div key={config.key} className="settings-config-item">
                                                <div className="settings-config-info">
                                                    {config.dangerousMode && (
                                                        <span
                                                            className="settings-danger-icon"
                                                            title="Dangerous mode enabled: auto-approves tool actions"
                                                        >
                                                            ⚠
                                                        </span>
                                                    )}
                                                    <span className="settings-config-name">
                                                        {config.name}
                                                    </span>
                                                    {config.isDefault && (
                                                        <span className="settings-default-badge">
                                                            default
                                                        </span>
                                                    )}
                                                </div>
                                                {config.description && (
                                                    <div className="settings-config-desc">
                                                        {config.description}
                                                    </div>
                                                )}
                                                {config.mcpNames && config.mcpNames.length > 0 && (
                                                    <div className="settings-config-mcps">
                                                        MCPs: {config.mcpNames.join(', ')}
                                                    </div>
                                                )}
                                                <div className="settings-config-actions">
                                                    <button
                                                        className="settings-edit-btn"
                                                        onClick={() => handleEdit(config)}
                                                    >
                                                        Edit
                                                    </button>
                                                    <button
                                                        className="settings-delete-btn"
                                                        onClick={() => handleDelete(config)}
                                                    >
                                                        Delete
                                                    </button>
                                                </div>
                                            </div>
                                        ))}
                                    </div>
                                )}
                            </div>
                        ))
                    )}
                </div>

                <div className="settings-footer">
                    <span className="settings-hint">
                        Press <kbd>Esc</kbd> to close
                    </span>
                </div>
            </div>
        </div>
    );
}
