import { useState, useEffect, useRef, useCallback } from 'react';
import './ConfigPicker.css';
import { GetLaunchConfigsForTool } from '../wailsjs/go/main/App';
import { createLogger } from './logger';
import { TOOLS } from './utils/tools';

const logger = createLogger('ConfigPicker');

export default function ConfigPicker({ tool, projectPath, projectName, onSelect, onCancel }) {
    const [configs, setConfigs] = useState([]);
    const [selectedIndex, setSelectedIndex] = useState(0);
    const [loading, setLoading] = useState(true);
    const containerRef = useRef(null);

    // Get tool info for display
    const toolInfo = TOOLS.find(t => t.id === tool) || { name: tool, icon: '?', color: '#666' };

    // Load configs on mount
    useEffect(() => {
        const loadConfigs = async () => {
            try {
                setLoading(true);
                const result = await GetLaunchConfigsForTool(tool);
                // Add a "Default (no config)" option at the start
                const configsWithDefault = [
                    { key: '', name: 'Default', description: 'No custom configuration', isDefault: false },
                    ...(result || [])
                ];
                setConfigs(configsWithDefault);

                // Find and select the default config if one exists
                const defaultIndex = configsWithDefault.findIndex(c => c.isDefault);
                if (defaultIndex > 0) {
                    setSelectedIndex(defaultIndex);
                }

                logger.info('Loaded configs for tool', { tool, count: result?.length || 0 });
            } catch (err) {
                logger.error('Failed to load configs:', err);
                // Still show default option on error
                setConfigs([{ key: '', name: 'Default', description: 'No custom configuration', isDefault: false }]);
            } finally {
                setLoading(false);
            }
        };

        loadConfigs();
        containerRef.current?.focus();
    }, [tool]);

    // Handle keyboard navigation
    const handleKeyDown = useCallback((e) => {
        switch (e.key) {
            case 'ArrowDown':
            case 'ArrowRight':
                e.preventDefault();
                setSelectedIndex(prev => (prev + 1) % configs.length);
                break;
            case 'ArrowUp':
            case 'ArrowLeft':
                e.preventDefault();
                setSelectedIndex(prev => (prev - 1 + configs.length) % configs.length);
                break;
            case 'Enter':
            case ' ':
                e.preventDefault();
                if (configs[selectedIndex]) {
                    handleSelect(configs[selectedIndex]);
                }
                break;
            case 'Escape':
                e.preventDefault();
                logger.info('Config picker cancelled');
                onCancel();
                break;
        }
    }, [configs, selectedIndex, onCancel]);

    const handleSelect = (config) => {
        logger.info('Config selected', { key: config.key, name: config.name, tool });
        onSelect(config.key);
    };

    return (
        <div className="config-picker-overlay" onClick={onCancel}>
            <div
                ref={containerRef}
                className="config-picker-container"
                onClick={(e) => e.stopPropagation()}
                onKeyDown={handleKeyDown}
                tabIndex={0}
            >
                <div className="config-picker-header">
                    <span
                        className="config-picker-tool-icon"
                        style={{ backgroundColor: toolInfo.color }}
                    >
                        {toolInfo.icon}
                    </span>
                    <div className="config-picker-header-text">
                        <h3>Select Configuration</h3>
                        <p className="config-picker-path">{projectName}</p>
                    </div>
                </div>

                <div className="config-picker-options">
                    {loading ? (
                        <div className="config-picker-loading">Loading configurations...</div>
                    ) : (
                        configs.map((config, index) => (
                            <button
                                key={config.key || 'default'}
                                className={`config-picker-option ${index === selectedIndex ? 'selected' : ''}`}
                                onClick={() => handleSelect(config)}
                                onMouseEnter={() => setSelectedIndex(index)}
                            >
                                <div className="config-picker-option-info">
                                    <div className="config-picker-option-header">
                                        {config.dangerousMode && (
                                            <span
                                                className="config-picker-danger-icon"
                                                title="Dangerous mode enabled"
                                            >
                                                ⚠
                                            </span>
                                        )}
                                        <span className="config-picker-option-name">
                                            {config.name}
                                        </span>
                                        {config.isDefault && (
                                            <span className="config-picker-default-badge">
                                                default
                                            </span>
                                        )}
                                    </div>
                                    {config.description && (
                                        <div className="config-picker-option-desc">
                                            {config.description}
                                        </div>
                                    )}
                                    {config.mcpNames && config.mcpNames.length > 0 && (
                                        <div className="config-picker-option-mcps">
                                            MCPs: {config.mcpNames.join(', ')}
                                        </div>
                                    )}
                                </div>
                            </button>
                        ))
                    )}
                </div>

                <div className="config-picker-footer">
                    <span className="config-picker-hint"><kbd>↑↓</kbd> Navigate</span>
                    <span className="config-picker-hint"><kbd>Enter</kbd> Select</span>
                    <span className="config-picker-hint"><kbd>Esc</kbd> Cancel</span>
                </div>
            </div>
        </div>
    );
}
