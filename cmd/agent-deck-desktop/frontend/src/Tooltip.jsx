import { useState, useEffect, useRef, useCallback } from 'react';
import './Tooltip.css';

/**
 * Reusable tooltip component that handles boundary detection
 *
 * Usage:
 * const { tooltipProps, Tooltip } = useTooltip();
 *
 * <div {...tooltipProps(content)} onMouseEnter={...}>
 *   Hover me
 * </div>
 * <Tooltip />
 */
export function useTooltip({ delay = 200 } = {}) {
    const [tooltip, setTooltip] = useState(null);
    const timeoutRef = useRef(null);

    const show = useCallback((e, content) => {
        if (timeoutRef.current) {
            clearTimeout(timeoutRef.current);
        }

        const rect = e.currentTarget.getBoundingClientRect();

        timeoutRef.current = setTimeout(() => {
            // Calculate initial position (centered below target)
            let x = rect.left + rect.width / 2;
            let y = rect.bottom + 8;

            // We'll adjust position in CSS based on boundaries
            // Pass the raw values and let the component handle clamping
            setTooltip({
                content,
                x,
                y,
                targetRect: rect,
            });
        }, delay);
    }, [delay]);

    const hide = useCallback(() => {
        if (timeoutRef.current) {
            clearTimeout(timeoutRef.current);
            timeoutRef.current = null;
        }
        setTooltip(null);
    }, []);

    // Cleanup on unmount
    useEffect(() => {
        return () => {
            if (timeoutRef.current) {
                clearTimeout(timeoutRef.current);
            }
        };
    }, []);

    const tooltipProps = useCallback((content) => ({
        onMouseEnter: (e) => show(e, content),
        onMouseLeave: hide,
    }), [show, hide]);

    const TooltipComponent = useCallback(() => {
        if (!tooltip) return null;
        return <TooltipElement tooltip={tooltip} />;
    }, [tooltip]);

    return {
        show,
        hide,
        tooltipProps,
        Tooltip: TooltipComponent,
        isVisible: !!tooltip,
    };
}

// Internal component that handles the actual positioning
function TooltipElement({ tooltip }) {
    const ref = useRef(null);
    const [position, setPosition] = useState({ left: 0, top: 0, align: 'center' });

    useEffect(() => {
        if (!ref.current) return;

        const tooltipRect = ref.current.getBoundingClientRect();
        const viewportWidth = window.innerWidth;
        const viewportHeight = window.innerHeight;
        const padding = 8; // Minimum distance from edge

        let left = tooltip.x;
        let top = tooltip.y;
        let align = 'center';

        // Horizontal boundary detection
        const halfWidth = tooltipRect.width / 2;

        if (left - halfWidth < padding) {
            // Too close to left edge - anchor to left
            left = tooltip.targetRect.left;
            align = 'left';
        } else if (left + halfWidth > viewportWidth - padding) {
            // Too close to right edge - anchor to right
            left = tooltip.targetRect.right;
            align = 'right';
        }

        // Vertical boundary detection
        if (top + tooltipRect.height > viewportHeight - padding) {
            // Too close to bottom - show above
            top = tooltip.targetRect.top - tooltipRect.height - 8;
        }

        // Ensure we don't go above the viewport
        if (top < padding) {
            top = padding;
        }

        setPosition({ left, top, align });
    }, [tooltip]);

    // Initial render off-screen to measure
    const style = {
        left: position.left,
        top: position.top,
        transform: position.align === 'center' ? 'translateX(-50%)' :
                   position.align === 'right' ? 'translateX(-100%)' : 'none',
    };

    return (
        <div ref={ref} className="tooltip" style={style}>
            {tooltip.content}
        </div>
    );
}

export default useTooltip;
