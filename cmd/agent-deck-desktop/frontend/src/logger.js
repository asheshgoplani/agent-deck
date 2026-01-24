// Development logger utility
const isDev = import.meta.env.DEV;

const LOG_STYLES = {
    debug: 'color: #888',
    info: 'color: #4cc9f0',
    warn: 'color: #ffe66d; font-weight: bold',
    error: 'color: #ff6b6b; font-weight: bold',
};

class Logger {
    constructor(context) {
        this.context = context;
    }

    _log(level, ...args) {
        if (!isDev && level === 'debug') return;

        const prefix = `[${this.context}]`;
        const style = LOG_STYLES[level] || '';

        console[level](`%c${prefix}`, style, ...args);
    }

    debug(...args) {
        this._log('debug', ...args);
    }

    info(...args) {
        this._log('info', ...args);
    }

    warn(...args) {
        this._log('warn', ...args);
    }

    error(...args) {
        this._log('error', ...args);
    }
}

export function createLogger(context) {
    return new Logger(context);
}

export default { createLogger };
