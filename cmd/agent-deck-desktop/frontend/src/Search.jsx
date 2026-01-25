import { useState, useEffect, useRef, useCallback } from 'react';
import './Search.css';

export default function Search({ searchAddon, onClose, focusTrigger }) {
    const [query, setQuery] = useState('');
    const [matchCount, setMatchCount] = useState(null);
    const inputRef = useRef(null);

    // Focus input on mount and whenever focusTrigger changes (Cmd+F pressed again)
    useEffect(() => {
        inputRef.current?.focus();
        inputRef.current?.select(); // Also select text for easy replacement
    }, [focusTrigger]);

    // Handle search
    const doSearch = useCallback((searchQuery, direction = 'next') => {
        if (!searchAddon || !searchQuery) {
            setMatchCount(null);
            return;
        }

        const options = {
            regex: false,
            wholeWord: false,
            caseSensitive: false,
            incremental: true,
        };

        let found;
        if (direction === 'next') {
            found = searchAddon.findNext(searchQuery, options);
        } else {
            found = searchAddon.findPrevious(searchQuery, options);
        }

        // xterm search addon doesn't provide match count directly
        // We just indicate if there was a match
        setMatchCount(found ? 'found' : 'not found');
    }, [searchAddon]);

    // Handle input change
    const handleChange = (e) => {
        const newQuery = e.target.value;
        setQuery(newQuery);
        doSearch(newQuery, 'next');
    };

    // Handle keyboard
    const handleKeyDown = (e) => {
        if (e.key === 'Escape') {
            onClose?.();
        } else if (e.key === 'Enter') {
            if (e.shiftKey) {
                doSearch(query, 'prev');
            } else {
                doSearch(query, 'next');
            }
        }
    };

    return (
        <div className="search-overlay">
            <div className="search-box">
                <input
                    ref={inputRef}
                    type="text"
                    value={query}
                    onChange={handleChange}
                    onKeyDown={handleKeyDown}
                    placeholder="Search..."
                    data-testid="search-input"
                    autoComplete="off"
                    autoCorrect="off"
                    autoCapitalize="off"
                    spellCheck="false"
                />
                <span className="search-status">
                    {matchCount && (
                        <span className={matchCount === 'found' ? 'found' : 'not-found'}>
                            {matchCount === 'found' ? 'Match found' : 'No matches'}
                        </span>
                    )}
                </span>
                <span className="search-hint">
                    Enter: next | Shift+Enter: prev | Esc: close
                </span>
            </div>
        </div>
    );
}
