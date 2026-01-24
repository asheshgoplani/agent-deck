/**
 * xterm.js theme configurations for dark and light modes
 */

export const darkTerminalTheme = {
    background: '#1a1a2e',
    foreground: '#eee',
    cursor: '#4cc9f0',
    cursorAccent: '#1a1a2e',
    selectionBackground: 'rgba(76, 201, 240, 0.3)',
    selectionForeground: undefined,
    selectionInactiveBackground: 'rgba(76, 201, 240, 0.2)',
    // Standard ANSI colors
    black: '#1a1a2e',
    red: '#ff6b6b',
    green: '#4ecdc4',
    yellow: '#ffe66d',
    blue: '#4cc9f0',
    magenta: '#f72585',
    cyan: '#7b8cde',
    white: '#eee',
    // Bright ANSI colors
    brightBlack: '#6c757d',
    brightRed: '#ff8787',
    brightGreen: '#69d9d0',
    brightYellow: '#fff3a3',
    brightBlue: '#72d4f7',
    brightMagenta: '#f85ca2',
    brightCyan: '#9ba8e8',
    brightWhite: '#fff',
};

export const lightTerminalTheme = {
    background: '#f5f5f7',
    foreground: '#1a1a2e',
    cursor: '#0077cc',
    cursorAccent: '#f5f5f7',
    selectionBackground: 'rgba(0, 119, 204, 0.25)',
    selectionForeground: undefined,
    selectionInactiveBackground: 'rgba(0, 119, 204, 0.15)',
    // Standard ANSI colors - adjusted for light background
    black: '#1a1a2e',
    red: '#c33',
    green: '#008577',
    yellow: '#997700',
    blue: '#0066bb',
    magenta: '#aa2277',
    cyan: '#0077aa',
    white: '#888',
    // Bright ANSI colors
    brightBlack: '#555',
    brightRed: '#dd4444',
    brightGreen: '#00a693',
    brightYellow: '#bb9900',
    brightBlue: '#0088dd',
    brightMagenta: '#cc3399',
    brightCyan: '#0099cc',
    brightWhite: '#333',
};

/**
 * Get the terminal theme based on the current theme mode
 * @param {'dark' | 'light'} theme - The current theme
 * @returns {object} xterm.js theme object
 */
export function getTerminalTheme(theme) {
    return theme === 'light' ? lightTerminalTheme : darkTerminalTheme;
}
