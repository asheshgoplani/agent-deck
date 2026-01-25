/**
 * Tests for scroll accumulator utility
 *
 * The scroll accumulator handles smooth scrolling for terminal wheel events.
 * It accumulates small deltaY values (from trackpads) until a threshold is
 * reached, then triggers scroll actions.
 */

import { describe, it, expect, beforeEach } from 'vitest';
import {
    DEFAULT_PIXELS_PER_LINE,
    DEFAULT_SCROLL_SPEED,
    MIN_SCROLL_SPEED,
    MAX_SCROLL_SPEED,
    calculatePixelsPerLine,
    createScrollAccumulator,
} from '../utils/scrollAccumulator';

describe('calculatePixelsPerLine', () => {
    it('returns default threshold at 100% speed', () => {
        expect(calculatePixelsPerLine(100)).toBe(DEFAULT_PIXELS_PER_LINE);
        expect(calculatePixelsPerLine(100)).toBe(50);
    });

    it('returns higher threshold (slower scroll) at 50% speed', () => {
        // 50 / (50/100) = 50 / 0.5 = 100
        expect(calculatePixelsPerLine(50)).toBe(100);
    });

    it('returns lower threshold (faster scroll) at 200% speed', () => {
        // 50 / (200/100) = 50 / 2 = 25
        expect(calculatePixelsPerLine(200)).toBe(25);
    });

    it('returns lower threshold (fastest scroll) at 250% speed', () => {
        // 50 / (250/100) = 50 / 2.5 = 20
        expect(calculatePixelsPerLine(250)).toBe(20);
    });

    it('clamps speed below minimum to minimum', () => {
        // Should use MIN_SCROLL_SPEED (50) when given 0 or negative
        expect(calculatePixelsPerLine(0)).toBe(calculatePixelsPerLine(MIN_SCROLL_SPEED));
        expect(calculatePixelsPerLine(-100)).toBe(calculatePixelsPerLine(MIN_SCROLL_SPEED));
        expect(calculatePixelsPerLine(25)).toBe(calculatePixelsPerLine(MIN_SCROLL_SPEED));
    });

    it('clamps speed above maximum to maximum', () => {
        // Should use MAX_SCROLL_SPEED (250) when given higher values
        expect(calculatePixelsPerLine(300)).toBe(calculatePixelsPerLine(MAX_SCROLL_SPEED));
        expect(calculatePixelsPerLine(500)).toBe(calculatePixelsPerLine(MAX_SCROLL_SPEED));
    });

    it('uses default speed when no argument provided', () => {
        expect(calculatePixelsPerLine()).toBe(calculatePixelsPerLine(DEFAULT_SCROLL_SPEED));
    });
});

describe('createScrollAccumulator', () => {
    describe('accumulator initialization', () => {
        it('starts with zero accumulator value', () => {
            const acc = createScrollAccumulator();
            expect(acc.getValue()).toBe(0);
        });

        it('uses default threshold at 100% speed', () => {
            const acc = createScrollAccumulator(100);
            expect(acc.getThreshold()).toBe(50);
        });

        it('uses custom threshold based on scroll speed', () => {
            const acc50 = createScrollAccumulator(50);
            const acc200 = createScrollAccumulator(200);

            expect(acc50.getThreshold()).toBe(100); // Slower = higher threshold
            expect(acc200.getThreshold()).toBe(25); // Faster = lower threshold
        });
    });

    describe('accumulating small deltaY values', () => {
        it('accumulates small values without triggering scroll', () => {
            const acc = createScrollAccumulator(100); // threshold = 50

            // Small deltas that don't reach threshold
            expect(acc.accumulate(10)).toBe(0);
            expect(acc.getValue()).toBe(10);

            expect(acc.accumulate(15)).toBe(0);
            expect(acc.getValue()).toBe(25);

            expect(acc.accumulate(10)).toBe(0);
            expect(acc.getValue()).toBe(35);
        });

        it('accumulates negative values correctly', () => {
            const acc = createScrollAccumulator(100);

            expect(acc.accumulate(-10)).toBe(0);
            expect(acc.getValue()).toBe(-10);

            expect(acc.accumulate(-20)).toBe(0);
            expect(acc.getValue()).toBe(-30);
        });

        it('accumulates mixed positive and negative values', () => {
            const acc = createScrollAccumulator(100);

            acc.accumulate(30);
            expect(acc.getValue()).toBe(30);

            acc.accumulate(-20);
            expect(acc.getValue()).toBe(10);

            acc.accumulate(-15);
            expect(acc.getValue()).toBe(-5);
        });
    });

    describe('triggering scroll when threshold is reached', () => {
        it('returns 1 line when exactly reaching threshold (scroll down)', () => {
            const acc = createScrollAccumulator(100); // threshold = 50

            acc.accumulate(25);
            const lines = acc.accumulate(25); // total = 50, exactly threshold

            expect(lines).toBe(1);
        });

        it('returns -1 line when exactly reaching negative threshold (scroll up)', () => {
            const acc = createScrollAccumulator(100); // threshold = 50

            acc.accumulate(-25);
            const lines = acc.accumulate(-25); // total = -50, exactly -threshold

            expect(lines).toBe(-1);
        });

        it('returns 1 line when exceeding threshold (scroll down)', () => {
            const acc = createScrollAccumulator(100);

            const lines = acc.accumulate(75); // exceeds threshold of 50

            expect(lines).toBe(1);
        });

        it('returns -1 line when exceeding negative threshold (scroll up)', () => {
            const acc = createScrollAccumulator(100);

            const lines = acc.accumulate(-75);

            expect(lines).toBe(-1);
        });

        it('returns multiple lines for large delta', () => {
            const acc = createScrollAccumulator(100); // threshold = 50

            const lines = acc.accumulate(150); // 3x threshold

            expect(lines).toBe(3);
        });

        it('returns multiple lines for large negative delta', () => {
            const acc = createScrollAccumulator(100);

            const lines = acc.accumulate(-120); // More than 2x threshold

            expect(lines).toBe(-2);
        });
    });

    describe('remainder handling after scroll', () => {
        it('preserves positive remainder after scrolling', () => {
            const acc = createScrollAccumulator(100); // threshold = 50

            acc.accumulate(75); // Returns 1, remainder should be 25

            expect(acc.getValue()).toBe(25);
        });

        it('preserves negative remainder after scrolling', () => {
            const acc = createScrollAccumulator(100);

            acc.accumulate(-75); // Returns -1, remainder should be -25

            expect(acc.getValue()).toBe(-25);
        });

        it('preserves remainder across multiple scroll triggers', () => {
            const acc = createScrollAccumulator(100); // threshold = 50

            // First trigger: 75 -> 1 line, remainder 25
            let lines = acc.accumulate(75);
            expect(lines).toBe(1);
            expect(acc.getValue()).toBe(25);

            // Add more: 25 + 40 = 65 -> 1 line, remainder 15
            lines = acc.accumulate(40);
            expect(lines).toBe(1);
            expect(acc.getValue()).toBe(15);

            // Not enough to trigger: 15 + 30 = 45
            lines = acc.accumulate(30);
            expect(lines).toBe(0);
            expect(acc.getValue()).toBe(45);

            // Trigger again: 45 + 10 = 55 -> 1 line, remainder 5
            lines = acc.accumulate(10);
            expect(lines).toBe(1);
            expect(acc.getValue()).toBe(5);
        });

        it('handles remainder when crossing zero direction', () => {
            const acc = createScrollAccumulator(100);

            // Build up positive
            acc.accumulate(30);
            expect(acc.getValue()).toBe(30);

            // Scroll back past zero
            acc.accumulate(-50);
            expect(acc.getValue()).toBe(-20);

            // Continue negative until threshold
            const lines = acc.accumulate(-35);
            expect(lines).toBe(-1);
            expect(acc.getValue()).toBe(-5);
        });
    });

    describe('different scroll speed settings', () => {
        it('scrolls faster at 200% speed (lower threshold)', () => {
            const acc = createScrollAccumulator(200); // threshold = 25

            // 30 pixels should trigger at 200% but not at 100%
            const lines = acc.accumulate(30);

            expect(lines).toBe(1);
            expect(acc.getValue()).toBe(5); // 30 - 25 = 5
        });

        it('scrolls slower at 50% speed (higher threshold)', () => {
            const acc = createScrollAccumulator(50); // threshold = 100

            // 75 pixels should NOT trigger at 50%
            const lines = acc.accumulate(75);

            expect(lines).toBe(0);
            expect(acc.getValue()).toBe(75);

            // Need to reach 100
            const lines2 = acc.accumulate(30); // total = 105

            expect(lines2).toBe(1);
            expect(acc.getValue()).toBe(5);
        });

        it('correctly handles 150% speed', () => {
            const acc = createScrollAccumulator(150);
            // threshold = 50 / 1.5 = 33.33...

            expect(acc.getThreshold()).toBeCloseTo(33.33, 1);

            // 40 should trigger one scroll
            const lines = acc.accumulate(40);
            expect(lines).toBe(1);
        });

        it('correctly handles 75% speed', () => {
            const acc = createScrollAccumulator(75);
            // threshold = 50 / 0.75 = 66.67

            expect(acc.getThreshold()).toBeCloseTo(66.67, 1);

            // 60 should NOT trigger
            expect(acc.accumulate(60)).toBe(0);

            // 10 more should trigger (total 70)
            expect(acc.accumulate(10)).toBe(1);
        });
    });

    describe('reset method', () => {
        it('resets accumulator to zero', () => {
            const acc = createScrollAccumulator();

            acc.accumulate(30);
            expect(acc.getValue()).toBe(30);

            acc.reset();
            expect(acc.getValue()).toBe(0);
        });

        it('does not change threshold on reset', () => {
            const acc = createScrollAccumulator(150);
            const originalThreshold = acc.getThreshold();

            acc.accumulate(20);
            acc.reset();

            expect(acc.getThreshold()).toBe(originalThreshold);
        });
    });

    describe('setScrollSpeed method', () => {
        it('updates threshold when speed changes', () => {
            const acc = createScrollAccumulator(100);
            expect(acc.getThreshold()).toBe(50);

            acc.setScrollSpeed(200);
            expect(acc.getThreshold()).toBe(25);

            acc.setScrollSpeed(50);
            expect(acc.getThreshold()).toBe(100);
        });

        it('preserves accumulator value when speed changes', () => {
            const acc = createScrollAccumulator(100);

            acc.accumulate(30);
            expect(acc.getValue()).toBe(30);

            acc.setScrollSpeed(200);

            // Accumulator value preserved (allows smooth transition)
            expect(acc.getValue()).toBe(30);

            // With new threshold of 25, adding 5 more should trigger
            const lines = acc.accumulate(5); // 35 total, threshold 25
            expect(lines).toBe(1);
            expect(acc.getValue()).toBe(10); // 35 - 25 = 10
        });

        it('clamps invalid speed values', () => {
            const acc = createScrollAccumulator(100);

            acc.setScrollSpeed(0);
            expect(acc.getThreshold()).toBe(calculatePixelsPerLine(MIN_SCROLL_SPEED));

            acc.setScrollSpeed(500);
            expect(acc.getThreshold()).toBe(calculatePixelsPerLine(MAX_SCROLL_SPEED));
        });
    });

    describe('edge cases', () => {
        it('handles zero deltaY', () => {
            const acc = createScrollAccumulator();

            const lines = acc.accumulate(0);

            expect(lines).toBe(0);
            expect(acc.getValue()).toBe(0);
        });

        it('handles very small deltaY values (sub-pixel)', () => {
            const acc = createScrollAccumulator(100);

            // Simulate many small trackpad events
            for (let i = 0; i < 50; i++) {
                acc.accumulate(0.5);
            }

            // 50 * 0.5 = 25, not enough for threshold
            expect(acc.getValue()).toBe(25);
            expect(acc.accumulate(0)).toBe(0);

            // Add more to trigger
            for (let i = 0; i < 50; i++) {
                acc.accumulate(0.5);
            }

            // Now at 50, should have triggered
            // After 100 iterations total, we have 50 pixels, which triggers once
            // The last iteration would have been the trigger
            expect(acc.getValue()).toBe(0); // 50 - 50 = 0
        });

        it('handles floating point precision', () => {
            const acc = createScrollAccumulator(100);

            // Accumulate values that might cause floating point issues
            // 16.666... * 3 = 50, which triggers a scroll
            acc.accumulate(16.666666666666668);
            acc.accumulate(16.666666666666668);
            const lines = acc.accumulate(16.666666666666668);

            // Total is ~50 which triggers exactly 1 scroll, remainder ~0
            expect(lines).toBe(1);
            expect(acc.getValue()).toBeCloseTo(0, 5);
        });

        it('handles maximum speed scroll correctly', () => {
            const acc = createScrollAccumulator(MAX_SCROLL_SPEED); // 250%
            // threshold = 50 / 2.5 = 20

            expect(acc.getThreshold()).toBe(20);

            // Even small movement triggers scroll
            expect(acc.accumulate(25)).toBe(1);
            expect(acc.getValue()).toBe(5);
        });

        it('handles minimum speed scroll correctly', () => {
            const acc = createScrollAccumulator(MIN_SCROLL_SPEED); // 50%
            // threshold = 50 / 0.5 = 100

            expect(acc.getThreshold()).toBe(100);

            // Large movement needed to trigger
            expect(acc.accumulate(90)).toBe(0);
            expect(acc.accumulate(20)).toBe(1);
            expect(acc.getValue()).toBe(10);
        });
    });

    describe('realistic usage scenarios', () => {
        it('simulates typical trackpad scroll sequence', () => {
            const acc = createScrollAccumulator(100);

            // Typical trackpad generates many small events
            const deltas = [2, 4, 8, 12, 10, 8, 6, 4, 2, 1];
            let totalLines = 0;

            for (const delta of deltas) {
                totalLines += acc.accumulate(delta);
            }

            // Total delta = 57, should scroll 1 line with 7 remainder
            expect(totalLines).toBe(1);
            expect(acc.getValue()).toBe(7);
        });

        it('simulates mouse wheel scroll (larger discrete steps)', () => {
            const acc = createScrollAccumulator(100);

            // Mouse wheel typically sends larger, discrete values
            expect(acc.accumulate(100)).toBe(2); // 2 lines
            expect(acc.getValue()).toBe(0);

            expect(acc.accumulate(-100)).toBe(-2); // 2 lines up
            expect(acc.getValue()).toBe(0);
        });

        it('simulates rapid bidirectional scrolling', () => {
            const acc = createScrollAccumulator(100);

            // User scrolls down then quickly up
            acc.accumulate(30);
            acc.accumulate(25);
            // Now at 55, should have scrolled 1 line, remainder 5

            expect(acc.getValue()).toBe(5);

            // Now quickly scroll up
            acc.accumulate(-60);
            // 5 - 60 = -55, should scroll -1 line, remainder -5

            expect(acc.getValue()).toBe(-5);
        });

        it('simulates user changing scroll speed mid-session', () => {
            const acc = createScrollAccumulator(100);

            // Start scrolling
            acc.accumulate(30);
            expect(acc.getValue()).toBe(30);

            // User goes to settings and increases scroll speed
            acc.setScrollSpeed(200); // threshold now 25

            // Continue scrolling - should trigger immediately
            const lines = acc.accumulate(5);
            expect(lines).toBe(1); // 35 > 25
            expect(acc.getValue()).toBe(10);
        });
    });
});

describe('constant exports', () => {
    it('exports correct default values', () => {
        expect(DEFAULT_PIXELS_PER_LINE).toBe(50);
        expect(DEFAULT_SCROLL_SPEED).toBe(100);
        expect(MIN_SCROLL_SPEED).toBe(50);
        expect(MAX_SCROLL_SPEED).toBe(250);
    });
});
