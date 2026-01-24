import { createContext, useContext, useState, useEffect, useCallback } from 'react';
import { GetDesktopTheme, SetDesktopTheme } from '../../wailsjs/go/main/App';
import { createLogger } from '../logger';

const logger = createLogger('ThemeContext');

// Create context
const ThemeContext = createContext(null);

/**
 * Get the system color scheme preference
 * @returns {'dark' | 'light'}
 */
function getSystemTheme() {
    if (typeof window !== 'undefined' && window.matchMedia) {
        return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
    }
    return 'dark';
}

/**
 * Resolve the effective theme based on preference
 * @param {'dark' | 'light' | 'auto'} preference
 * @returns {'dark' | 'light'}
 */
function resolveTheme(preference) {
    if (preference === 'auto') {
        return getSystemTheme();
    }
    return preference;
}

/**
 * ThemeProvider component
 * Provides theme context to the entire app
 */
export function ThemeProvider({ children }) {
    // User's preference: 'dark', 'light', or 'auto'
    const [themePreference, setThemePreference] = useState('dark');
    // Resolved effective theme: 'dark' or 'light'
    const [theme, setTheme] = useState('dark');
    // Loading state
    const [loading, setLoading] = useState(true);

    // Load theme preference on mount
    useEffect(() => {
        const loadTheme = async () => {
            try {
                const savedPreference = await GetDesktopTheme();
                logger.info('Loaded theme preference:', savedPreference);
                setThemePreference(savedPreference);
                setTheme(resolveTheme(savedPreference));
            } catch (err) {
                logger.error('Failed to load theme:', err);
                // Default to dark
                setThemePreference('dark');
                setTheme('dark');
            } finally {
                setLoading(false);
            }
        };

        loadTheme();
    }, []);

    // Listen for system preference changes (for auto mode)
    useEffect(() => {
        if (typeof window === 'undefined' || !window.matchMedia) {
            return;
        }

        const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)');

        const handleChange = (e) => {
            logger.info('System theme changed:', e.matches ? 'dark' : 'light');
            if (themePreference === 'auto') {
                setTheme(e.matches ? 'dark' : 'light');
            }
        };

        // Modern browsers
        if (mediaQuery.addEventListener) {
            mediaQuery.addEventListener('change', handleChange);
            return () => mediaQuery.removeEventListener('change', handleChange);
        }
        // Fallback for older browsers
        mediaQuery.addListener(handleChange);
        return () => mediaQuery.removeListener(handleChange);
    }, [themePreference]);

    // Apply theme to document
    useEffect(() => {
        document.documentElement.setAttribute('data-theme', theme);
        logger.debug('Applied theme to document:', theme);
    }, [theme]);

    // Set theme function
    const setThemePref = useCallback(async (newPreference) => {
        try {
            logger.info('Setting theme preference:', newPreference);
            await SetDesktopTheme(newPreference);
            setThemePreference(newPreference);
            setTheme(resolveTheme(newPreference));
        } catch (err) {
            logger.error('Failed to save theme:', err);
        }
    }, []);

    const value = {
        // The current effective theme ('dark' or 'light')
        theme,
        // The user's preference ('dark', 'light', or 'auto')
        themePreference,
        // Function to change the theme
        setTheme: setThemePref,
        // Whether we're still loading
        loading,
    };

    return (
        <ThemeContext.Provider value={value}>
            {children}
        </ThemeContext.Provider>
    );
}

/**
 * Hook to access theme context
 * @returns {{ theme: 'dark' | 'light', themePreference: 'dark' | 'light' | 'auto', setTheme: (theme: string) => void, loading: boolean }}
 */
export function useTheme() {
    const context = useContext(ThemeContext);
    if (!context) {
        throw new Error('useTheme must be used within a ThemeProvider');
    }
    return context;
}

export default ThemeContext;
