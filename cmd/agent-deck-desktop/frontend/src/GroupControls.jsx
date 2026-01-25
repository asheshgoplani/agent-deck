import { useCallback, useMemo } from 'react';
import './GroupControls.css';
import { createLogger } from './logger';

const logger = createLogger('GroupControls');

/**
 * GroupControls provides UI controls for managing all session groups at once.
 *
 * @param {Object} props
 * @param {Array} props.groups - Array of group objects with { path, expanded, ... }
 * @param {Object} props.expandedGroups - Map of group path -> expanded state (desktop overrides)
 * @param {function} props.onCollapseAll - Callback to collapse all groups
 * @param {function} props.onExpandAll - Callback to expand all groups
 */
export default function GroupControls({ groups, expandedGroups, onCollapseAll, onExpandAll }) {
    // Calculate current state: are all groups expanded, all collapsed, or mixed?
    const groupState = useMemo(() => {
        if (!groups || groups.length === 0) return 'empty';

        let expandedCount = 0;
        let collapsedCount = 0;

        for (const group of groups) {
            // Check desktop override first, then fall back to TUI default
            const isExpanded = expandedGroups.hasOwnProperty(group.path)
                ? expandedGroups[group.path]
                : (group.expanded ?? true);

            if (isExpanded) {
                expandedCount++;
            } else {
                collapsedCount++;
            }
        }

        if (expandedCount === groups.length) return 'all-expanded';
        if (collapsedCount === groups.length) return 'all-collapsed';
        return 'mixed';
    }, [groups, expandedGroups]);

    const handleToggle = useCallback(() => {
        logger.info('Toggle all groups', { currentState: groupState });

        if (groupState === 'all-expanded' || groupState === 'mixed') {
            // If all expanded or mixed, collapse all
            onCollapseAll();
        } else {
            // If all collapsed, expand all
            onExpandAll();
        }
    }, [groupState, onCollapseAll, onExpandAll]);

    // Don't render if no groups
    if (!groups || groups.length === 0) {
        return null;
    }

    // Determine button appearance based on state
    const isExpanded = groupState === 'all-expanded';
    const isMixed = groupState === 'mixed';
    const buttonTitle = isExpanded
        ? 'Collapse all groups (Cmd+Shift+H)'
        : isMixed
            ? 'Collapse all groups (Cmd+Shift+H)'
            : 'Expand all groups (Cmd+Shift+H)';
    const buttonLabel = isExpanded || isMixed ? '▼' : '▶';

    return (
        <button
            className={`group-controls-btn ${isExpanded ? 'expanded' : 'collapsed'} ${isMixed ? 'mixed' : ''}`}
            onClick={handleToggle}
            title={buttonTitle}
            aria-label={buttonTitle}
        >
            <span className="group-controls-icon">{buttonLabel}</span>
            <span className="group-controls-label">
                {isExpanded || isMixed ? 'Collapse All' : 'Expand All'}
            </span>
        </button>
    );
}
