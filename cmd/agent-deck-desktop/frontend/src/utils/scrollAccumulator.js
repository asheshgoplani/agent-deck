/**
 * Scroll Accumulator Utility
 *
 * This module provides a smooth scrolling accumulator pattern for terminal
 * wheel events. Trackpads generate many small deltaY values, and this
 * accumulator collects them until a threshold is reached before triggering
 * a scroll action.
 *
 * The scroll speed setting (50-250%) inversely affects the threshold:
 * - 100% (default): PIXELS_PER_LINE = 50 (baseline)
 * - 50% (slower): PIXELS_PER_LINE = 100 (need more delta to scroll)
 * - 200% (faster): PIXELS_PER_LINE = 25 (less delta needed to scroll)
 *
 * Formula: effectiveThreshold = DEFAULT_PIXELS_PER_LINE / (scrollSpeed / 100)
 */

export const DEFAULT_PIXELS_PER_LINE = 50;
export const MIN_SCROLL_SPEED = 50;
export const MAX_SCROLL_SPEED = 250;
export const DEFAULT_SCROLL_SPEED = 100;

/**
 * Calculate the effective pixels-per-line threshold based on scroll speed setting
 *
 * @param {number} scrollSpeedPercent - Scroll speed as percentage (50-250, default 100)
 * @returns {number} Effective pixels per line threshold
 */
export function calculatePixelsPerLine(scrollSpeedPercent = DEFAULT_SCROLL_SPEED) {
    // Clamp to valid range
    const clampedSpeed = Math.max(MIN_SCROLL_SPEED, Math.min(MAX_SCROLL_SPEED, scrollSpeedPercent));
    return DEFAULT_PIXELS_PER_LINE / (clampedSpeed / 100);
}

/**
 * Create a scroll accumulator instance
 *
 * @param {number} scrollSpeedPercent - Scroll speed as percentage (50-250)
 * @returns {Object} Accumulator instance with methods
 */
export function createScrollAccumulator(scrollSpeedPercent = DEFAULT_SCROLL_SPEED) {
    let accumulator = 0;
    let pixelsPerLine = calculatePixelsPerLine(scrollSpeedPercent);

    return {
        /**
         * Add a delta value to the accumulator and calculate lines to scroll
         *
         * @param {number} deltaY - The wheel event deltaY value
         * @returns {number} Number of lines to scroll (positive = down, negative = up)
         */
        accumulate(deltaY) {
            accumulator += deltaY;

            if (Math.abs(accumulator) >= pixelsPerLine) {
                const linesToScroll = Math.trunc(accumulator / pixelsPerLine);
                accumulator -= linesToScroll * pixelsPerLine;
                return linesToScroll;
            }

            return 0;
        },

        /**
         * Get the current accumulator value
         * @returns {number} Current accumulator value
         */
        getValue() {
            return accumulator;
        },

        /**
         * Get the current pixels per line threshold
         * @returns {number} Current threshold
         */
        getThreshold() {
            return pixelsPerLine;
        },

        /**
         * Reset the accumulator to zero
         */
        reset() {
            accumulator = 0;
        },

        /**
         * Update the scroll speed setting
         * @param {number} newSpeedPercent - New scroll speed percentage
         */
        setScrollSpeed(newSpeedPercent) {
            pixelsPerLine = calculatePixelsPerLine(newSpeedPercent);
            // Don't reset accumulator - allow smooth transition
        },
    };
}
