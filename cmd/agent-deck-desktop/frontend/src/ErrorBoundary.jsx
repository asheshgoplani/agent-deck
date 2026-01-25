import React from 'react';
import { createLogger } from './logger';

const logger = createLogger('ErrorBoundary');

class ErrorBoundary extends React.Component {
    constructor(props) {
        super(props);
        this.state = { hasError: false, error: null, errorInfo: null };
    }

    static getDerivedStateFromError(error) {
        return { hasError: true, error };
    }

    componentDidCatch(error, errorInfo) {
        // Log to backend file
        logger.error('React error caught:', error, 'Component stack:', errorInfo.componentStack);
        this.setState({ errorInfo });
    }

    render() {
        if (this.state.hasError) {
            return (
                <div style={{
                    padding: '20px',
                    backgroundColor: '#1e1e1e',
                    color: '#ff6b6b',
                    fontFamily: 'monospace',
                    height: '100vh',
                    overflow: 'auto'
                }}>
                    <h2>Something went wrong</h2>
                    <p style={{ color: '#ccc' }}>
                        The error has been logged to: <code>/tmp/agent-deck-desktop-debug.log</code>
                    </p>
                    <details style={{ marginTop: '20px' }}>
                        <summary style={{ cursor: 'pointer', color: '#4cc9f0' }}>
                            Error details
                        </summary>
                        <pre style={{
                            marginTop: '10px',
                            padding: '10px',
                            backgroundColor: '#2a2a2a',
                            borderRadius: '4px',
                            overflow: 'auto',
                            whiteSpace: 'pre-wrap',
                            wordBreak: 'break-word'
                        }}>
                            {this.state.error?.toString()}
                            {this.state.errorInfo?.componentStack}
                        </pre>
                    </details>
                    <button
                        onClick={() => window.location.reload()}
                        style={{
                            marginTop: '20px',
                            padding: '10px 20px',
                            backgroundColor: '#4cc9f0',
                            color: '#1e1e1e',
                            border: 'none',
                            borderRadius: '4px',
                            cursor: 'pointer',
                            fontWeight: 'bold'
                        }}
                    >
                        Reload App
                    </button>
                </div>
            );
        }

        return this.props.children;
    }
}

export default ErrorBoundary;
