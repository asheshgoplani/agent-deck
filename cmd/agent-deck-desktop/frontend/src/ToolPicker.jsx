import { useState, useEffect, useRef } from 'react';
import './ToolPicker.css';
import { createLogger } from './logger';
import { TOOLS } from './utils/tools';
import ToolIcon from './ToolIcon';

const logger = createLogger('ToolPicker');

export default function ToolPicker({ projectPath, projectName, onSelect, onSelectWithConfig, onCancel }) {
    const [selectedIndex, setSelectedIndex] = useState(0);
    const containerRef = useRef(null);

    useEffect(() => {
        logger.info('Tool picker opened', { projectPath, projectName });
        containerRef.current?.focus();
    }, [projectPath, projectName]);

    const handleKeyDown = (e) => {
        const withConfig = e.metaKey || e.ctrlKey;

        switch (e.key) {
            case 'ArrowDown':
            case 'ArrowRight':
                e.preventDefault();
                setSelectedIndex(prev => (prev + 1) % TOOLS.length);
                break;
            case 'ArrowUp':
            case 'ArrowLeft':
                e.preventDefault();
                setSelectedIndex(prev => (prev - 1 + TOOLS.length) % TOOLS.length);
                break;
            case 'Enter':
            case ' ':
                e.preventDefault();
                handleSelect(TOOLS[selectedIndex], withConfig);
                break;
            case 'Escape':
                e.preventDefault();
                logger.info('Tool picker cancelled');
                onCancel();
                break;
            case '1':
                e.preventDefault();
                handleSelect(TOOLS[0], withConfig);
                break;
            case '2':
                e.preventDefault();
                handleSelect(TOOLS[1], withConfig);
                break;
            case '3':
                e.preventDefault();
                handleSelect(TOOLS[2], withConfig);
                break;
        }
    };

    const handleSelect = (tool, withConfig = false) => {
        if (withConfig && onSelectWithConfig) {
            logger.info('Tool selected with config picker', { tool: tool.id, projectPath });
            onSelectWithConfig(tool.id);
        } else {
            logger.info('Tool selected', { tool: tool.id, projectPath });
            onSelect(tool.id);
        }
    };

    return (
        <div className="tool-picker-overlay" onClick={onCancel}>
            <div
                ref={containerRef}
                className="tool-picker-container"
                onClick={(e) => e.stopPropagation()}
                onKeyDown={handleKeyDown}
                tabIndex={0}
            >
                <div className="tool-picker-header">
                    <h3>Select Tool for {projectName}</h3>
                    <p className="tool-picker-path">{projectPath}</p>
                </div>
                <div className="tool-picker-options">
                    {TOOLS.map((tool, index) => (
                        <button
                            key={tool.id}
                            className={`tool-picker-option ${index === selectedIndex ? 'selected' : ''}`}
                            onClick={(e) => handleSelect(tool, e.metaKey || e.ctrlKey)}
                            onMouseEnter={() => setSelectedIndex(index)}
                        >
                            <span
                                className="tool-picker-icon"
                                style={{ backgroundColor: tool.color }}
                            >
                                <ToolIcon tool={tool.id} size={18} />
                            </span>
                            <div className="tool-picker-info">
                                <div className="tool-picker-name">{tool.name}</div>
                                <div className="tool-picker-desc">{tool.description}</div>
                            </div>
                            <span className="tool-picker-shortcut">{index + 1}</span>
                        </button>
                    ))}
                </div>
                <div className="tool-picker-footer">
                    <span className="tool-picker-hint"><kbd>↑↓</kbd> Navigate</span>
                    <span className="tool-picker-hint"><kbd>1-3</kbd> Quick select</span>
                    <span className="tool-picker-hint"><kbd>Enter</kbd> Launch</span>
                    <span className="tool-picker-hint"><kbd>⌘Enter</kbd> Pick config</span>
                    <span className="tool-picker-hint"><kbd>Esc</kbd> Cancel</span>
                </div>
            </div>
        </div>
    );
}
