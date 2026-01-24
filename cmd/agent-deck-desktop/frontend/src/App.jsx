import { useRef, useState, useEffect, useCallback } from 'react';
import './App.css';
import Terminal from './Terminal';
import Search from './Search';
import SessionSelector from './SessionSelector';
import { createLogger } from './logger';

const logger = createLogger('App');

function App() {
    const searchAddonRef = useRef(null);
    const [showSearch, setShowSearch] = useState(false);
    const [view, setView] = useState('selector'); // 'selector' or 'terminal'
    const [selectedSession, setSelectedSession] = useState(null);
    const [showCloseConfirm, setShowCloseConfirm] = useState(false);

    const handleCloseSearch = useCallback(() => {
        setShowSearch(false);
    }, []);

    const handleSelectSession = useCallback((session) => {
        logger.info('Selecting session:', session.title);
        setSelectedSession(session);
        setView('terminal');
    }, []);

    const handleNewTerminal = useCallback(() => {
        logger.info('Starting new terminal');
        setSelectedSession(null);
        setView('terminal');
    }, []);

    const handleBackToSelector = useCallback(() => {
        logger.info('Returning to session selector');
        setView('selector');
        setSelectedSession(null);
        setShowSearch(false);
    }, []);

    // Handle keyboard shortcuts
    const handleKeyDown = useCallback((e) => {
        // Cmd+F or Ctrl+F to open search (only in terminal view)
        if ((e.metaKey || e.ctrlKey) && e.key === 'f' && view === 'terminal') {
            e.preventDefault();
            setShowSearch(true);
        }
        // Cmd+K - Reserved for command palette (future feature)
        if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
            e.preventDefault();
            logger.info('Cmd+K pressed - command palette not yet implemented');
            // TODO: Open command palette (fuzzy search for sessions, actions, shortcuts)
        }
        // Cmd+W to close/back with confirmation
        if ((e.metaKey || e.ctrlKey) && e.key === 'w' && view === 'terminal') {
            e.preventDefault();
            logger.info('Cmd+W pressed - showing close confirmation');
            setShowCloseConfirm(true);
        }
        // Cmd+, to go back to session selector
        if ((e.metaKey || e.ctrlKey) && e.key === ',' && view === 'terminal') {
            e.preventDefault();
            handleBackToSelector();
        }
    }, [view, showSearch, handleBackToSelector]);

    useEffect(() => {
        document.addEventListener('keydown', handleKeyDown);
        return () => document.removeEventListener('keydown', handleKeyDown);
    }, [handleKeyDown]);

    // Show session selector
    if (view === 'selector') {
        return (
            <div id="App">
                <SessionSelector
                    onSelect={handleSelectSession}
                    onNewTerminal={handleNewTerminal}
                />
            </div>
        );
    }

    // Show terminal
    return (
        <div id="App">
            <div className="terminal-header">
                <button className="back-button" onClick={handleBackToSelector} title="Back to sessions (Cmd+,)">
                    ← Sessions
                </button>
                {selectedSession && (
                    <div className="session-title-header">
                        {selectedSession.title}
                    </div>
                )}
            </div>
            <div className="terminal-container">
                <Terminal
                    searchRef={searchAddonRef}
                    session={selectedSession}
                />
            </div>
            {showSearch && (
                <Search
                    searchAddon={searchAddonRef.current}
                    onClose={handleCloseSearch}
                />
            )}
            {showCloseConfirm && (
                <div className="modal-overlay" onClick={() => setShowCloseConfirm(false)}>
                    <div className="modal-content" onClick={(e) => e.stopPropagation()}>
                        <h3>Close Terminal?</h3>
                        <p>Are you sure you want to return to the session selector?</p>
                        <div className="modal-buttons">
                            <button className="modal-btn-cancel" onClick={() => setShowCloseConfirm(false)}>
                                Cancel
                            </button>
                            <button className="modal-btn-confirm" onClick={() => {
                                setShowCloseConfirm(false);
                                handleBackToSelector();
                            }}>
                                Close Terminal
                            </button>
                        </div>
                    </div>
                </div>
            )}
        </div>
    );
}

export default App;
