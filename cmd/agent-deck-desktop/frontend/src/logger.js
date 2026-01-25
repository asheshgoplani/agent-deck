// Development logger utility with backend file logging
import { LogFrontendDiagnostic } from '../wailsjs/go/main/App';

const isDev = import.meta.env.DEV;

const LOG_STYLES = {
    debug: 'color: #888',
    info: 'color: #4cc9f0',
    warn: 'color: #ffe66d; font-weight: bold',
    error: 'color: #ff6b6b; font-weight: bold',
};

// Format args for file logging (stringify objects, handle errors)
function formatArgs(args) {
    return args.map(arg => {
        if (arg instanceof Error) {
            return `${arg.message}\n${arg.stack || ''}`;
        }
        if (typeof arg === 'object') {
            try {
                return JSON.stringify(arg);
            } catch {
                return String(arg);
            }
        }
        return String(arg);
    }).join(' ');
}

class Logger {
    constructor(context) {
        this.context = context;
    }

    _log(level, ...args) {
        if (!isDev && level === 'debug') return;

        const prefix = `[${this.context}]`;
        const style = LOG_STYLES[level] || '';

        // Always log to console
        console[level](`%c${prefix}`, style, ...args);

        // For warn and error, also write to backend log file
        if (level === 'warn' || level === 'error') {
            const timestamp = new Date().toISOString();
            const message = `[${timestamp}] [${level.toUpperCase()}] ${prefix} ${formatArgs(args)}`;
            LogFrontendDiagnostic(message).catch(() => {
                // Ignore errors from logging - don't create infinite loop
            });
        }
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

// Global error handler - call once at app startup
let globalErrorHandlerInstalled = false;
export function installGlobalErrorHandler() {
    if (globalErrorHandlerInstalled) return;
    globalErrorHandlerInstalled = true;

    const globalLogger = createLogger('GLOBAL');

    // Catch unhandled promise rejections
    window.addEventListener('unhandledrejection', (event) => {
        globalLogger.error('Unhandled promise rejection:', event.reason);
    });

    // Catch uncaught errors
    window.addEventListener('error', (event) => {
        globalLogger.error('Uncaught error:', event.error || event.message);
    });

    // Log that we've installed the handler
    LogFrontendDiagnostic('[INIT] Global error handler installed').catch(() => {});
}

export default { createLogger, installGlobalErrorHandler };
