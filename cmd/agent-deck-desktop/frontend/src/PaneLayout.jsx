import { useCallback } from 'react';
import './PaneLayout.css';
import Pane from './Pane';
import SplitHandle from './SplitHandle';

/**
 * PaneLayout - Recursive renderer for layout tree
 *
 * Renders either:
 * - A single Pane (leaf node)
 * - A split container with two children and a draggable handle
 */
export default function PaneLayout({
    node,
    activePaneId,
    onPaneFocus,
    onRatioChange,
    onPaneSessionSelect,
    terminalRefs,
    searchRefs,
    fontSize,
    moveMode = false,
    paneNumberMap = {},
}) {
    // Handle ratio changes from the split handle
    const handleRatioChange = useCallback((newRatio) => {
        if (onRatioChange && node.type === 'split') {
            // Find a pane ID in this subtree to identify the split
            const findFirstPane = (n) => {
                if (n.type === 'pane') return n.id;
                return findFirstPane(n.children[0]);
            };
            const paneId = findFirstPane(node.children[0]);
            onRatioChange(paneId, newRatio);
        }
    }, [node, onRatioChange]);

    // Render a pane leaf node
    if (node.type === 'pane') {
        return (
            <Pane
                paneId={node.id}
                session={node.session}
                isActive={node.id === activePaneId}
                onFocus={onPaneFocus}
                onSessionSelect={onPaneSessionSelect}
                terminalRefs={terminalRefs}
                searchRefs={searchRefs}
                fontSize={fontSize}
                moveMode={moveMode}
                paneNumber={paneNumberMap[node.id] || 0}
            />
        );
    }

    // Render a split container
    const { direction, ratio, children } = node;
    const isVertical = direction === 'vertical';

    // Calculate flex values - first child gets ratio, second gets 1-ratio
    const firstFlex = ratio;
    const secondFlex = 1 - ratio;

    return (
        <div className={`pane-split ${isVertical ? 'split-vertical' : 'split-horizontal'}`}>
            <div
                className="pane-split-child"
                style={{ flex: firstFlex }}
            >
                <PaneLayout
                    node={children[0]}
                    activePaneId={activePaneId}
                    onPaneFocus={onPaneFocus}
                    onRatioChange={onRatioChange}
                    onPaneSessionSelect={onPaneSessionSelect}
                    terminalRefs={terminalRefs}
                    searchRefs={searchRefs}
                    fontSize={fontSize}
                    moveMode={moveMode}
                    paneNumberMap={paneNumberMap}
                />
            </div>

            <SplitHandle
                direction={direction}
                onDrag={handleRatioChange}
                currentRatio={ratio}
            />

            <div
                className="pane-split-child"
                style={{ flex: secondFlex }}
            >
                <PaneLayout
                    node={children[1]}
                    activePaneId={activePaneId}
                    onPaneFocus={onPaneFocus}
                    onRatioChange={onRatioChange}
                    onPaneSessionSelect={onPaneSessionSelect}
                    terminalRefs={terminalRefs}
                    searchRefs={searchRefs}
                    fontSize={fontSize}
                    moveMode={moveMode}
                    paneNumberMap={paneNumberMap}
                />
            </div>
        </div>
    );
}
