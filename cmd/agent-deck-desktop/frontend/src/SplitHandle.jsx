import { useCallback, useRef, useState, useEffect } from 'react';
import { createLogger } from './logger';

const logger = createLogger('SplitHandle');

/**
 * SplitHandle - Draggable divider between panes
 *
 * Handles:
 * - Mouse drag for resizing
 * - Visual feedback (highlight on hover/drag)
 * - Cursor style based on split direction
 */
export default function SplitHandle({
    direction,
    onDrag,
    currentRatio,
}) {
    const handleRef = useRef(null);
    const [isDragging, setIsDragging] = useState(false);
    const dragStartRef = useRef({ x: 0, y: 0, ratio: 0, parentSize: 0 });

    const isVertical = direction === 'vertical';

    // Start drag
    const handleMouseDown = useCallback((e) => {
        e.preventDefault();
        e.stopPropagation();

        // Get parent container size for calculating ratio
        const parent = handleRef.current?.parentElement;
        if (!parent) return;

        const parentRect = parent.getBoundingClientRect();
        const parentSize = isVertical ? parentRect.width : parentRect.height;

        dragStartRef.current = {
            x: e.clientX,
            y: e.clientY,
            ratio: currentRatio,
            parentSize,
        };

        setIsDragging(true);
        logger.debug('Drag started', { direction, currentRatio });
    }, [direction, currentRatio, isVertical]);

    // Handle drag
    useEffect(() => {
        if (!isDragging) return;

        const handleMouseMove = (e) => {
            const { x, y, ratio, parentSize } = dragStartRef.current;

            // Calculate delta in pixels
            const delta = isVertical
                ? e.clientX - x
                : e.clientY - y;

            // Convert to ratio change
            const ratioChange = delta / parentSize;
            const newRatio = Math.max(0.1, Math.min(0.9, ratio + ratioChange));

            if (onDrag) {
                onDrag(newRatio);
            }
        };

        const handleMouseUp = () => {
            setIsDragging(false);
            logger.debug('Drag ended');
        };

        // Add listeners to document for smooth dragging
        document.addEventListener('mousemove', handleMouseMove);
        document.addEventListener('mouseup', handleMouseUp);

        // Prevent text selection during drag
        document.body.style.userSelect = 'none';
        document.body.style.cursor = isVertical ? 'col-resize' : 'row-resize';

        return () => {
            document.removeEventListener('mousemove', handleMouseMove);
            document.removeEventListener('mouseup', handleMouseUp);
            document.body.style.userSelect = '';
            document.body.style.cursor = '';
        };
    }, [isDragging, isVertical, onDrag]);

    return (
        <div
            ref={handleRef}
            className={`split-handle ${isVertical ? 'handle-vertical' : 'handle-horizontal'} ${isDragging ? 'dragging' : ''}`}
            onMouseDown={handleMouseDown}
        />
    );
}
