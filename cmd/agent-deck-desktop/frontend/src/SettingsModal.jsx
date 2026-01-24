import { useState, useEffect, useCallback } from 'react';
import './SettingsModal.css';
import LaunchConfigEditor from './LaunchConfigEditor';
import { GetLaunchConfigs, DeleteLaunchConfig } from '../wailsjs/go/main/App';
import { createLogger } from './logger';
import { TOOLS } from './utils/tools';

const logger = createLogger('SettingsModal');

export default function SettingsModal({ onClose }) {
    const [configs, setConfigs] = useState([]);
    const [loading, setLoading] = useState(true);
    const [editingConfig, setEditingConfig] = useState(null); // null = list view, object = editing
    const [creatingForTool, setCreatingForTool] = useState(null); // tool name when creating new

    // Load configs on mount
    useEffect(() => {
        loadConfigs();
    }, []);

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
                    <h2>Launch Configurations</h2>
                    <button className="settings-close" onClick={onClose}>
                        &times;
                    </button>
                </div>

                <div className="settings-content">
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
                                        {tool.icon}
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
