import { useState, useEffect, useCallback } from 'react';
import './SettingsModal.css';
import LaunchConfigEditor from './LaunchConfigEditor';
import { GetLaunchConfigs, DeleteLaunchConfig, GetSoftNewlineMode, SetSoftNewlineMode } from '../wailsjs/go/main/App';
import { createLogger } from './logger';
import { TOOLS } from './utils/tools';
import ToolIcon from './ToolIcon';
import { useTheme } from './context/ThemeContext';

const logger = createLogger('SettingsModal');

export default function SettingsModal({ onClose }) {
    const [configs, setConfigs] = useState([]);
    const [loading, setLoading] = useState(true);
    const [editingConfig, setEditingConfig] = useState(null); // null = list view, object = editing
    const [creatingForTool, setCreatingForTool] = useState(null); // tool name when creating new
    const [softNewlineMode, setSoftNewlineMode] = useState('both');
    const { themePreference, setTheme } = useTheme();

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
        document.addEventListener('keydown', handleKeyDown);
        return () => document.removeEventListener('keydown', handleKeyDown);
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
            <div className="settings-container" onClick={(e) => e.stopPropagation()}>
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
