import { useState, useEffect, useCallback, useRef } from 'react';
import './LaunchConfigEditor.css';
import {
    SaveLaunchConfig,
    ValidateMCPConfigPath,
    GenerateConfigKey
} from '../wailsjs/go/main/App';
import { createLogger } from './logger';
import { TOOLS } from './utils/tools';

const logger = createLogger('LaunchConfigEditor');

// Dangerous mode flag hints for each tool
const DANGER_FLAGS = {
    claude: '--dangerously-skip-permissions',
    gemini: '--yolo',
    opencode: '',
};

export default function LaunchConfigEditor({ config, tool, onSave, onCancel }) {
    const isEditing = !!config;

    // Form state
    const [name, setName] = useState(config?.name || '');
    const [description, setDescription] = useState(config?.description || '');
    const [dangerousMode, setDangerousMode] = useState(config?.dangerousMode || false);
    const [mcpConfigPath, setMcpConfigPath] = useState(config?.mcpConfigPath || '');
    const [extraArgs, setExtraArgs] = useState(config?.extraArgs?.join(' ') || '');
    const [isDefault, setIsDefault] = useState(config?.isDefault || false);

    // Validation state
    const [mcpValidation, setMcpValidation] = useState({ valid: true, names: [], error: '' });
    const [saving, setSaving] = useState(false);
    const [error, setError] = useState('');

    // For debounced MCP validation
    const mcpValidationTimer = useRef(null);

    // Get tool info
    const toolInfo = TOOLS.find(t => t.id === tool) || { name: tool, icon: '?', color: '#666' };
    const dangerFlag = DANGER_FLAGS[tool] || '';

    // Validate MCP config path with debounce
    useEffect(() => {
        if (mcpValidationTimer.current) {
            clearTimeout(mcpValidationTimer.current);
        }

        if (!mcpConfigPath.trim()) {
            setMcpValidation({ valid: true, names: [], error: '' });
            return;
        }

        mcpValidationTimer.current = setTimeout(async () => {
            try {
                const names = await ValidateMCPConfigPath(mcpConfigPath.trim());
                setMcpValidation({ valid: true, names: names || [], error: '' });
            } catch (err) {
                setMcpValidation({ valid: false, names: [], error: err.message || 'Invalid path' });
            }
        }, 500);

        return () => {
            if (mcpValidationTimer.current) {
                clearTimeout(mcpValidationTimer.current);
            }
        };
    }, [mcpConfigPath]);

    // Handle save
    const handleSave = useCallback(async () => {
        // Validate name
        const trimmedName = name.trim();
        if (!trimmedName) {
            setError('Name is required');
            return;
        }

        if (!mcpValidation.valid) {
            setError('Please fix the MCP config path error');
            return;
        }

        setSaving(true);
        setError('');

        try {
            // Generate key for new configs, use existing key for edits
            let key = config?.key;
            if (!key) {
                key = await GenerateConfigKey(tool, trimmedName);
            }

            // Parse extra args (split by whitespace)
            const parsedArgs = extraArgs.trim()
                ? extraArgs.trim().split(/\s+/).filter(Boolean)
                : [];

            await SaveLaunchConfig(
                key,
                trimmedName,
                tool,
                description.trim(),
                dangerousMode,
                mcpConfigPath.trim(),
                parsedArgs,
                isDefault
            );

            logger.info('Saved launch config', { key, name: trimmedName, tool });
            onSave();
        } catch (err) {
            logger.error('Failed to save launch config:', err);
            setError(err.message || 'Failed to save configuration');
        } finally {
            setSaving(false);
        }
    }, [name, description, dangerousMode, mcpConfigPath, extraArgs, isDefault, config, tool, mcpValidation.valid, onSave]);

    // Handle keyboard
    const handleKeyDown = useCallback((e) => {
        if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            handleSave();
        }
    }, [handleSave]);

    return (
        <div className="editor-container" onKeyDown={handleKeyDown}>
            <div className="editor-header">
                <span
                    className="editor-tool-icon"
                    style={{ backgroundColor: toolInfo.color }}
                >
                    {toolInfo.icon}
                </span>
                <h2>{isEditing ? 'Edit Configuration' : 'New Configuration'}</h2>
            </div>

            <div className="editor-form">
                {/* Tool (read-only display) */}
                <div className="editor-field">
                    <label>Tool</label>
                    <div className="editor-readonly">{toolInfo.name}</div>
                </div>

                {/* Name */}
                <div className="editor-field">
                    <label htmlFor="config-name">
                        Name <span className="editor-required">*</span>
                    </label>
                    <input
                        id="config-name"
                        type="text"
                        value={name}
                        onChange={(e) => setName(e.target.value)}
                        placeholder="e.g., Minimal MCPs, Full Stack, etc."
                        autoFocus
                    />
                </div>

                {/* Description */}
                <div className="editor-field">
                    <label htmlFor="config-desc">Description</label>
                    <input
                        id="config-desc"
                        type="text"
                        value={description}
                        onChange={(e) => setDescription(e.target.value)}
                        placeholder="Optional description"
                    />
                </div>

                {/* Dangerous Mode */}
                <div className="editor-field editor-checkbox-field">
                    <label className="editor-checkbox">
                        <input
                            type="checkbox"
                            checked={dangerousMode}
                            onChange={(e) => setDangerousMode(e.target.checked)}
                        />
                        <span>Enable dangerous mode</span>
                    </label>
                    {dangerFlag && (
                        <div className="editor-hint">
                            Adds <code>{dangerFlag}</code> flag
                        </div>
                    )}
                </div>

                {/* MCP Config Path */}
                <div className="editor-field">
                    <label htmlFor="config-mcp">MCP Config Path</label>
                    <input
                        id="config-mcp"
                        type="text"
                        value={mcpConfigPath}
                        onChange={(e) => setMcpConfigPath(e.target.value)}
                        placeholder="~/.config/custom-mcps.json"
                    />
                    {mcpConfigPath.trim() && (
                        <div className={`editor-mcp-preview ${mcpValidation.valid ? 'valid' : 'invalid'}`}>
                            {mcpValidation.error ? (
                                <span className="editor-mcp-error">{mcpValidation.error}</span>
                            ) : mcpValidation.names.length > 0 ? (
                                <span className="editor-mcp-names">
                                    MCPs: {mcpValidation.names.join(', ')}
                                </span>
                            ) : (
                                <span className="editor-mcp-empty">No MCPs found in file</span>
                            )}
                        </div>
                    )}
                </div>

                {/* Extra Arguments */}
                <div className="editor-field">
                    <label htmlFor="config-args">Extra Arguments</label>
                    <input
                        id="config-args"
                        type="text"
                        value={extraArgs}
                        onChange={(e) => setExtraArgs(e.target.value)}
                        placeholder="--model opus --resume"
                    />
                    <div className="editor-hint">
                        Additional CLI arguments (space-separated)
                    </div>
                </div>

                {/* Default */}
                <div className="editor-field editor-checkbox-field">
                    <label className="editor-checkbox">
                        <input
                            type="checkbox"
                            checked={isDefault}
                            onChange={(e) => setIsDefault(e.target.checked)}
                        />
                        <span>Set as default for {toolInfo.name}</span>
                    </label>
                </div>

                {/* Error */}
                {error && (
                    <div className="editor-error">
                        {error}
                    </div>
                )}
            </div>

            <div className="editor-footer">
                <div className="editor-footer-hint">
                    <kbd>{navigator.platform.includes('Mac') ? '⌘' : 'Ctrl'}</kbd>+<kbd>Enter</kbd> to save
                </div>
                <div className="editor-footer-actions">
                    <button
                        className="editor-cancel-btn"
                        onClick={onCancel}
                        disabled={saving}
                    >
                        Cancel
                    </button>
                    <button
                        className="editor-save-btn"
                        onClick={handleSave}
                        disabled={saving}
                    >
                        {saving ? 'Saving...' : 'Save'}
                    </button>
                </div>
            </div>
        </div>
    );
}
