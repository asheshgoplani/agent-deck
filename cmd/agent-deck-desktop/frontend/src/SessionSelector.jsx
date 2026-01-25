import { useState, useEffect, useCallback, useRef, forwardRef, useImperativeHandle } from 'react';
import { UpdateSessionCustomLabel, MarkSessionAccessed } from '../wailsjs/go/main/App';
import './SessionSelector.css';
import { createLogger } from './logger';
import { useTooltip } from './Tooltip';
import ShortcutBar from './ShortcutBar';
import RenameDialog from './RenameDialog';
import SessionList from './SessionList';
import SessionPreview from './SessionPreview';

const logger = createLogger('SessionSelector');

const SessionSelector = forwardRef(function SessionSelector({ onSelect, onNewTerminal, statusFilter = 'all', onCycleFilter, onOpenPalette, onOpenHelp }, ref) {
    const [previewSession, setPreviewSession] = useState(null);
    const [contextMenu, setContextMenu] = useState(null);
    const [labelingSession, setLabelingSession] = useState(null);
    const { Tooltip } = useTooltip();
    const containerRef = useRef(null);
    const sessionListRef = useRef(null);

    // Handle session selection for preview
    const handlePreviewSession = useCallback((session) => {
        logger.debug('Preview session:', session?.title);
        setPreviewSession(session);
    }, []);

    // Handle session selection (attach)
    const handleSelectSession = useCallback(async (session) => {
        logger.info('Attaching to session:', session.title);

        // Update last accessed timestamp
        try {
            await MarkSessionAccessed(session.id);
        } catch (err) {
            logger.warn('Failed to mark session accessed:', err);
        }

        onSelect(session);
    }, [onSelect]);

    // Context menu handlers
    const handleContextMenu = useCallback((e, session) => {
        e.preventDefault();
        logger.debug('Context menu on session', { title: session.title });
        const menuWidth = 200;
        const menuHeight = 100;
        const x = Math.min(e.clientX, window.innerWidth - menuWidth);
        const y = Math.min(e.clientY, window.innerHeight - menuHeight);
        setContextMenu({ x, y, session });
    }, []);

    const closeContextMenu = useCallback(() => {
        setContextMenu(null);
    }, []);

    // Close context menu on click outside
    useEffect(() => {
        if (contextMenu) {
            document.addEventListener('click', closeContextMenu);
            return () => document.removeEventListener('click', closeContextMenu);
        }
    }, [contextMenu, closeContextMenu]);

    const handleAddCustomLabel = useCallback(() => {
        if (!contextMenu?.session) return;
        logger.info('Opening label dialog', { session: contextMenu.session.title });
        setLabelingSession(contextMenu.session);
        setContextMenu(null);
    }, [contextMenu]);

    const handleRemoveCustomLabel = useCallback(async () => {
        if (!contextMenu?.session) return;
        try {
            logger.info('Removing custom label', { sessionId: contextMenu.session.id });
            await UpdateSessionCustomLabel(contextMenu.session.id, '');
        } catch (err) {
            logger.error('Failed to remove custom label:', err);
        }
        setContextMenu(null);
    }, [contextMenu]);

    const handleSaveCustomLabel = useCallback(async (newLabel) => {
        if (!labelingSession || !newLabel.trim()) return;
        try {
            logger.info('Saving custom label', { sessionId: labelingSession.id, label: newLabel });
            await UpdateSessionCustomLabel(labelingSession.id, newLabel.trim());
        } catch (err) {
            logger.error('Failed to save custom label:', err);
        }
        setLabelingSession(null);
    }, [labelingSession]);

    // Expose toggle all groups function to parent via ref
    useImperativeHandle(ref, () => ({
        toggleAllGroups: () => {
            if (sessionListRef.current?.toggleAllGroups) {
                sessionListRef.current.toggleAllGroups();
            }
        },
    }), []);

    return (
        <div className="session-selector" ref={containerRef}>
            <div className="session-selector-header">
                <h2>Agent Deck Sessions</h2>
            </div>

            <div className="session-selector-split">
                <SessionList
                    ref={sessionListRef}
                    onSelect={handleSelectSession}
                    onPreview={handlePreviewSession}
                    selectedSessionId={previewSession?.id}
                    statusFilter={statusFilter}
                    onCycleFilter={onCycleFilter}
                />

                <div className="session-selector-divider" />

                <SessionPreview
                    session={previewSession}
                    onAttach={handleSelectSession}
                />
            </div>

            <ShortcutBar
                view="selector"
                onNewTerminal={onNewTerminal}
                onOpenPalette={onOpenPalette}
                onCycleFilter={onCycleFilter}
                onOpenHelp={onOpenHelp}
            />

            <Tooltip />

            {contextMenu && (
                <div
                    className="session-context-menu"
                    style={{ left: contextMenu.x, top: contextMenu.y }}
                    onClick={(e) => e.stopPropagation()}
                >
                    <button onClick={handleAddCustomLabel}>
                        {contextMenu.session?.customLabel ? 'Edit Custom Label' : 'Add Custom Label'}
                    </button>
                    {contextMenu.session?.customLabel && (
                        <button onClick={handleRemoveCustomLabel}>
                            Remove Custom Label
                        </button>
                    )}
                </div>
            )}

            {labelingSession && (
                <RenameDialog
                    currentName={labelingSession.customLabel || ''}
                    title={labelingSession.customLabel ? 'Edit Custom Label' : 'Add Custom Label'}
                    placeholder="Enter label..."
                    onSave={handleSaveCustomLabel}
                    onCancel={() => setLabelingSession(null)}
                />
            )}
        </div>
    );
}
