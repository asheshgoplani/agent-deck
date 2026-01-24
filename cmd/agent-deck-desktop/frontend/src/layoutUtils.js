/**
 * Layout Tree Utilities for Multi-Pane System
 *
 * Layout nodes form a binary tree:
 * - Pane nodes are leaves: { type: 'pane', id: string, sessionId: string | null }
 * - Split nodes are internal: { type: 'split', direction: 'horizontal' | 'vertical', ratio: number, children: [LayoutNode, LayoutNode] }
 *
 * Direction terminology:
 * - 'horizontal' = horizontal divider = panes stacked top/bottom
 * - 'vertical' = vertical divider = panes side by side (left/right)
 */

let paneIdCounter = 0;

/**
 * Generate a unique pane ID
 */
export function generatePaneId() {
    paneIdCounter += 1;
    return `pane-${Date.now()}-${paneIdCounter}`;
}

/**
 * Create a new pane node
 * @param {string|null} sessionId - The session to display, or null for empty pane
 * @returns {Object} A pane node
 */
export function createPane(sessionId = null) {
    return {
        type: 'pane',
        id: generatePaneId(),
        sessionId,
    };
}

/**
 * Create a single-pane layout from a session
 * @param {Object|null} session - The session object
 * @returns {Object} A pane node
 */
export function createSinglePaneLayout(session = null) {
    return {
        type: 'pane',
        id: generatePaneId(),
        sessionId: session?.id || null,
        session: session || null,
    };
}

/**
 * Deep clone a layout tree
 * @param {Object} node - Layout node to clone
 * @returns {Object} Cloned layout
 */
export function cloneLayout(node) {
    if (node.type === 'pane') {
        return { ...node };
    }
    return {
        ...node,
        children: [cloneLayout(node.children[0]), cloneLayout(node.children[1])],
    };
}

/**
 * Find a pane by ID in the layout tree
 * @param {Object} node - Layout node to search
 * @param {string} paneId - ID of pane to find
 * @returns {Object|null} The pane node or null
 */
export function findPane(node, paneId) {
    if (node.type === 'pane') {
        return node.id === paneId ? node : null;
    }
    return findPane(node.children[0], paneId) || findPane(node.children[1], paneId);
}

/**
 * Find a pane's parent split node and which child index it is
 * @param {Object} root - Root layout node
 * @param {string} paneId - ID of pane to find
 * @param {Object|null} parent - Current parent (used in recursion)
 * @param {number} childIndex - Which child of parent (0 or 1)
 * @returns {Object|null} { parent, childIndex } or null if not found
 */
export function findPaneParent(root, paneId, parent = null, childIndex = -1) {
    if (root.type === 'pane') {
        if (root.id === paneId) {
            return { parent, childIndex };
        }
        return null;
    }

    const leftResult = findPaneParent(root.children[0], paneId, root, 0);
    if (leftResult) return leftResult;

    return findPaneParent(root.children[1], paneId, root, 1);
}

/**
 * Get all panes in the layout as a flat list (depth-first order)
 * @param {Object} node - Layout node
 * @returns {Array} Array of pane nodes
 */
export function getPaneList(node) {
    if (node.type === 'pane') {
        return [node];
    }
    return [...getPaneList(node.children[0]), ...getPaneList(node.children[1])];
}

/**
 * Count total panes in layout
 * @param {Object} node - Layout node
 * @returns {number} Number of panes
 */
export function countPanes(node) {
    return getPaneList(node).length;
}

/**
 * Split a pane in the given direction, creating a new empty pane
 * @param {Object} layout - Root layout node
 * @param {string} paneId - ID of pane to split
 * @param {'horizontal'|'vertical'} direction - Split direction
 * @returns {Object} { layout: newLayout, newPaneId: string }
 */
export function splitPane(layout, paneId, direction) {
    const newPane = createPane(null);

    function split(node) {
        if (node.type === 'pane') {
            if (node.id === paneId) {
                // Found the pane to split - replace with a split node
                // The original pane goes first (left/top), new pane goes second (right/bottom)
                return {
                    type: 'split',
                    direction,
                    ratio: 0.5,
                    children: [
                        { ...node }, // Clone the original pane
                        newPane,     // New empty pane
                    ],
                };
            }
            return node;
        }

        // Recurse into children
        return {
            ...node,
            children: [split(node.children[0]), split(node.children[1])],
        };
    }

    return {
        layout: split(layout),
        newPaneId: newPane.id,
    };
}

/**
 * Close a pane, promoting its sibling to take its place
 * @param {Object} layout - Root layout node
 * @param {string} paneId - ID of pane to close
 * @returns {Object|null} New layout, or null if this was the last pane
 */
export function closePane(layout, paneId) {
    // If closing the only pane, return null
    if (layout.type === 'pane') {
        if (layout.id === paneId) {
            return null;
        }
        return layout; // Not the pane we're looking for
    }

    // Check if either child is the pane to close
    const [left, right] = layout.children;

    if (left.type === 'pane' && left.id === paneId) {
        // Closing left child - promote right child
        return right;
    }

    if (right.type === 'pane' && right.id === paneId) {
        // Closing right child - promote left child
        return left;
    }

    // Recurse into children
    const newLeft = closePane(left, paneId);
    if (newLeft !== left) {
        // Pane was in left subtree
        if (newLeft === null) {
            // Left subtree is now empty (shouldn't happen with valid layouts)
            return right;
        }
        return { ...layout, children: [newLeft, right] };
    }

    const newRight = closePane(right, paneId);
    if (newRight !== right) {
        // Pane was in right subtree
        if (newRight === null) {
            // Right subtree is now empty
            return left;
        }
        return { ...layout, children: [left, newRight] };
    }

    // Pane not found in this subtree
    return layout;
}

/**
 * Update the ratio of a split node
 * @param {Object} layout - Root layout node
 * @param {string} paneId - ID of a pane in the split to update (finds its parent)
 * @param {number} newRatio - New ratio (0.0 to 1.0)
 * @returns {Object} Updated layout
 */
export function updateSplitRatio(layout, paneId, newRatio) {
    // Clamp ratio to valid range
    const ratio = Math.max(0.1, Math.min(0.9, newRatio));

    function update(node, targetSplit = null) {
        if (node.type === 'pane') {
            return node;
        }

        // Check if this split contains the target pane as a direct child
        const containsTarget = node.children.some(
            child => child.type === 'pane' && child.id === paneId
        );

        if (containsTarget) {
            return { ...node, ratio };
        }

        return {
            ...node,
            children: [update(node.children[0]), update(node.children[1])],
        };
    }

    return update(layout);
}

/**
 * Get the adjacent pane in a given direction
 * This uses a spatial approach - builds a grid of pane positions and finds neighbors
 * @param {Object} layout - Root layout node
 * @param {string} paneId - Current pane ID
 * @param {'left'|'right'|'up'|'down'} direction - Direction to look
 * @returns {string|null} ID of adjacent pane, or null if none
 */
export function getAdjacentPane(layout, paneId, direction) {
    // Build a list of pane bounding boxes (normalized 0-1 coordinates)
    const boxes = [];

    function buildBoxes(node, x = 0, y = 0, w = 1, h = 1) {
        if (node.type === 'pane') {
            boxes.push({
                id: node.id,
                x, y, w, h,
                cx: x + w / 2, // center x
                cy: y + h / 2, // center y
            });
            return;
        }

        const { direction: splitDir, ratio, children } = node;

        if (splitDir === 'vertical') {
            // Side by side - first child is left
            const leftW = w * ratio;
            buildBoxes(children[0], x, y, leftW, h);
            buildBoxes(children[1], x + leftW, y, w - leftW, h);
        } else {
            // Stacked - first child is top
            const topH = h * ratio;
            buildBoxes(children[0], x, y, w, topH);
            buildBoxes(children[1], x, y + topH, w, h - topH);
        }
    }

    buildBoxes(layout);

    // Find current pane's box
    const current = boxes.find(b => b.id === paneId);
    if (!current) return null;

    // Find candidates in the given direction
    let candidates = [];
    const epsilon = 0.001; // Small tolerance for floating point comparison

    switch (direction) {
        case 'left':
            // Panes whose right edge is at or near current's left edge
            // and vertically overlapping
            candidates = boxes.filter(b =>
                b.id !== paneId &&
                b.x + b.w <= current.x + epsilon &&
                b.y < current.y + current.h - epsilon &&
                b.y + b.h > current.y + epsilon
            );
            // Sort by distance (prefer closest, then most vertically aligned)
            candidates.sort((a, b) => {
                const distA = current.x - (a.x + a.w);
                const distB = current.x - (b.x + b.w);
                if (Math.abs(distA - distB) > epsilon) return distA - distB;
                return Math.abs(a.cy - current.cy) - Math.abs(b.cy - current.cy);
            });
            break;

        case 'right':
            candidates = boxes.filter(b =>
                b.id !== paneId &&
                b.x >= current.x + current.w - epsilon &&
                b.y < current.y + current.h - epsilon &&
                b.y + b.h > current.y + epsilon
            );
            candidates.sort((a, b) => {
                const distA = a.x - (current.x + current.w);
                const distB = b.x - (current.x + current.w);
                if (Math.abs(distA - distB) > epsilon) return distA - distB;
                return Math.abs(a.cy - current.cy) - Math.abs(b.cy - current.cy);
            });
            break;

        case 'up':
            candidates = boxes.filter(b =>
                b.id !== paneId &&
                b.y + b.h <= current.y + epsilon &&
                b.x < current.x + current.w - epsilon &&
                b.x + b.w > current.x + epsilon
            );
            candidates.sort((a, b) => {
                const distA = current.y - (a.y + a.h);
                const distB = current.y - (b.y + b.h);
                if (Math.abs(distA - distB) > epsilon) return distA - distB;
                return Math.abs(a.cx - current.cx) - Math.abs(b.cx - current.cx);
            });
            break;

        case 'down':
            candidates = boxes.filter(b =>
                b.id !== paneId &&
                b.y >= current.y + current.h - epsilon &&
                b.x < current.x + current.w - epsilon &&
                b.x + b.w > current.x + epsilon
            );
            candidates.sort((a, b) => {
                const distA = a.y - (current.y + current.h);
                const distB = b.y - (current.y + current.h);
                if (Math.abs(distA - distB) > epsilon) return distA - distB;
                return Math.abs(a.cx - current.cx) - Math.abs(b.cx - current.cx);
            });
            break;
    }

    return candidates.length > 0 ? candidates[0].id : null;
}

/**
 * Get next/previous pane in iteration order (depth-first)
 * @param {Object} layout - Root layout node
 * @param {string} paneId - Current pane ID
 * @param {'next'|'prev'} direction - Direction
 * @returns {string|null} ID of next/prev pane, or null (will wrap)
 */
export function getCyclicPane(layout, paneId, direction) {
    const panes = getPaneList(layout);
    const currentIndex = panes.findIndex(p => p.id === paneId);
    if (currentIndex === -1) return panes[0]?.id || null;

    if (direction === 'next') {
        const nextIndex = (currentIndex + 1) % panes.length;
        return panes[nextIndex].id;
    } else {
        const prevIndex = (currentIndex - 1 + panes.length) % panes.length;
        return panes[prevIndex].id;
    }
}

/**
 * Update a pane's session
 * @param {Object} layout - Root layout node
 * @param {string} paneId - ID of pane to update
 * @param {Object|null} session - Session to assign (or null to clear)
 * @returns {Object} Updated layout
 */
export function updatePaneSession(layout, paneId, session) {
    if (layout.type === 'pane') {
        if (layout.id === paneId) {
            return {
                ...layout,
                sessionId: session?.id || null,
                session: session || null,
            };
        }
        return layout;
    }

    return {
        ...layout,
        children: [
            updatePaneSession(layout.children[0], paneId, session),
            updatePaneSession(layout.children[1], paneId, session),
        ],
    };
}

/**
 * Swap sessions between two panes
 * @param {Object} layout - Root layout node
 * @param {string} paneId1 - First pane ID
 * @param {string} paneId2 - Second pane ID
 * @returns {Object} Updated layout
 */
export function swapPaneSessions(layout, paneId1, paneId2) {
    const pane1 = findPane(layout, paneId1);
    const pane2 = findPane(layout, paneId2);

    if (!pane1 || !pane2) return layout;

    let result = updatePaneSession(layout, paneId1, pane2.session);
    result = updatePaneSession(result, paneId2, pane1.session);
    return result;
}

/**
 * Balance all split ratios to 0.5
 * @param {Object} layout - Root layout node
 * @returns {Object} Updated layout with all ratios at 0.5
 */
export function balanceLayout(layout) {
    if (layout.type === 'pane') {
        return layout;
    }

    return {
        ...layout,
        ratio: 0.5,
        children: [balanceLayout(layout.children[0]), balanceLayout(layout.children[1])],
    };
}

// ============================================================
// Layout Presets
// ============================================================

/**
 * Create a preset layout
 * @param {'single'|'2-col'|'2-row'|'2x2'} preset - Preset type
 * @returns {Object} Layout node
 */
export function createPresetLayout(preset) {
    switch (preset) {
        case 'single':
            return createPane(null);

        case '2-col':
            return {
                type: 'split',
                direction: 'vertical',
                ratio: 0.5,
                children: [createPane(null), createPane(null)],
            };

        case '2-row':
            return {
                type: 'split',
                direction: 'horizontal',
                ratio: 0.5,
                children: [createPane(null), createPane(null)],
            };

        case '2x2':
            return {
                type: 'split',
                direction: 'vertical',
                ratio: 0.5,
                children: [
                    {
                        type: 'split',
                        direction: 'horizontal',
                        ratio: 0.5,
                        children: [createPane(null), createPane(null)],
                    },
                    {
                        type: 'split',
                        direction: 'horizontal',
                        ratio: 0.5,
                        children: [createPane(null), createPane(null)],
                    },
                ],
            };

        default:
            return createPane(null);
    }
}

/**
 * Apply a preset layout, preserving existing sessions where possible
 * Sessions are assigned to panes in order (first session to first pane, etc.)
 * @param {Object} currentLayout - Current layout
 * @param {Object} presetLayout - Preset layout to apply
 * @returns {Object} { layout: newLayout, closedSessions: Session[] }
 */
export function applyPreset(currentLayout, presetLayout) {
    // Get all sessions from current layout
    const currentPanes = getPaneList(currentLayout);
    const sessions = currentPanes
        .map(p => p.session)
        .filter(s => s != null);

    // Get panes in new layout
    const newPanes = getPaneList(presetLayout);

    // Assign sessions to new panes
    let result = cloneLayout(presetLayout);
    const closedSessions = [];

    for (let i = 0; i < sessions.length; i++) {
        if (i < newPanes.length) {
            result = updatePaneSession(result, newPanes[i].id, sessions[i]);
        } else {
            closedSessions.push(sessions[i]);
        }
    }

    return { layout: result, closedSessions };
}

/**
 * Get the first pane ID in a layout
 * @param {Object} layout - Layout node
 * @returns {string} First pane ID
 */
export function getFirstPaneId(layout) {
    if (layout.type === 'pane') {
        return layout.id;
    }
    return getFirstPaneId(layout.children[0]);
}

// ============================================================
// Saved Layouts
// ============================================================

/**
 * Convert a layout to a saveable format (strip session data, keep structure)
 * @param {Object} layout - Layout node with sessions
 * @returns {Object} Layout structure without sessions (for saving as template)
 */
export function layoutToSaveFormat(layout) {
    if (layout.type === 'pane') {
        return {
            type: 'pane',
            id: generatePaneId(), // Generate new IDs for the template
        };
    }

    return {
        type: 'split',
        direction: layout.direction,
        ratio: layout.ratio,
        children: [
            layoutToSaveFormat(layout.children[0]),
            layoutToSaveFormat(layout.children[1]),
        ],
    };
}

/**
 * Apply a saved layout template to the current layout, preserving sessions
 * Similar to applyPreset but takes a SavedLayout structure
 * @param {Object} currentLayout - Current layout with sessions
 * @param {Object} savedLayout - Saved layout structure (from SavedLayout.layout)
 * @returns {Object} { layout: newLayout, closedSessions: Session[] }
 */
export function applySavedLayout(currentLayout, savedLayout) {
    // Get all sessions from current layout
    const currentPanes = getPaneList(currentLayout);
    const sessions = currentPanes
        .map(p => p.session)
        .filter(s => s != null);

    // Clone the saved layout and assign new pane IDs
    const clonedLayout = cloneLayoutWithNewIds(savedLayout);

    // Get panes in new layout
    const newPanes = getPaneList(clonedLayout);

    // Assign sessions to new panes
    let result = clonedLayout;
    const closedSessions = [];

    for (let i = 0; i < sessions.length; i++) {
        if (i < newPanes.length) {
            result = updatePaneSession(result, newPanes[i].id, sessions[i]);
        } else {
            closedSessions.push(sessions[i]);
        }
    }

    return { layout: result, closedSessions };
}

/**
 * Clone a layout tree with new pane IDs
 * @param {Object} node - Layout node to clone
 * @returns {Object} Cloned layout with fresh IDs
 */
function cloneLayoutWithNewIds(node) {
    if (node.type === 'pane') {
        return {
            type: 'pane',
            id: generatePaneId(),
            sessionId: null,
            session: null,
        };
    }
    return {
        type: 'split',
        direction: node.direction,
        ratio: node.ratio,
        children: [
            cloneLayoutWithNewIds(node.children[0]),
            cloneLayoutWithNewIds(node.children[1]),
        ],
    };
}
