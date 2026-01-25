import { useState, useEffect, useCallback, useRef } from 'react';
import { GetSessionMetadata, GetGitBranch, IsGitWorktree } from '../../wailsjs/go/main/App';
import { createLogger } from '../logger';

const logger = createLogger('useSessionMetadata');

// Polling interval for metadata updates (5 seconds)
const POLL_INTERVAL = 5000;

/**
 * Hook to fetch and maintain session metadata for the status bar.
 * Provides hostname, current working directory, and git branch information.
 *
 * @param {Object} session - The session object with tmuxSession and projectPath
 * @returns {Object} - { hostname, cwd, gitBranch, isWorktree, isLoading, error, refresh }
 */
export function useSessionMetadata(session) {
    const [metadata, setMetadata] = useState({
        hostname: '',
        cwd: '',
        gitBranch: '',
        isWorktree: false,
    });
    const [isLoading, setIsLoading] = useState(true);
    const [error, setError] = useState(null);
    const pollIntervalRef = useRef(null);

    // Fetch metadata from the backend
    const fetchMetadata = useCallback(async () => {
        if (!session?.tmuxSession) {
            setMetadata({ hostname: '', cwd: '', gitBranch: '', isWorktree: false });
            setIsLoading(false);
            return;
        }

        try {
            // Get runtime metadata from tmux (hostname, cwd, git branch from cwd)
            const result = await GetSessionMetadata(session.tmuxSession);

            // Also check if it's a worktree for the current cwd
            let isWorktree = false;
            if (result.cwd) {
                try {
                    isWorktree = await IsGitWorktree(result.cwd);
                } catch {
                    // Ignore worktree check errors
                }
            }

            setMetadata({
                hostname: result.hostname || '',
                cwd: result.cwd || session.projectPath || '',
                gitBranch: result.gitBranch || '',
                isWorktree,
            });
            setError(null);
        } catch (err) {
            logger.warn('Failed to fetch session metadata:', err);
            // Fall back to session data
            setMetadata(prev => ({
                ...prev,
                cwd: session.projectPath || '',
                gitBranch: session.gitBranch || '',
                isWorktree: session.isWorktree || false,
            }));
            setError(err);
        } finally {
            setIsLoading(false);
        }
    }, [session?.tmuxSession, session?.projectPath, session?.gitBranch, session?.isWorktree]);

    // Initial fetch and polling setup
    useEffect(() => {
        fetchMetadata();

        // Set up polling for live updates
        pollIntervalRef.current = setInterval(fetchMetadata, POLL_INTERVAL);

        return () => {
            if (pollIntervalRef.current) {
                clearInterval(pollIntervalRef.current);
            }
        };
    }, [fetchMetadata]);

    // Manual refresh function
    const refresh = useCallback(() => {
        setIsLoading(true);
        fetchMetadata();
    }, [fetchMetadata]);

    return {
        ...metadata,
        isLoading,
        error,
        refresh,
    };
}

export default useSessionMetadata;
