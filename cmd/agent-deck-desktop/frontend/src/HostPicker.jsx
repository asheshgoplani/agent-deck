import { useState, useEffect, useRef } from 'react';
import './HostPicker.css';
import { createLogger } from './logger';
import { ListSSHHosts, GetSSHHostStatus, TestSSHConnection } from '../wailsjs/go/main/App';

const logger = createLogger('HostPicker');

export default function HostPicker({ onSelect, onCancel }) {
    const [hosts, setHosts] = useState([]);
    const [hostStatuses, setHostStatuses] = useState({});
    const [selectedIndex, setSelectedIndex] = useState(0);
    const [loading, setLoading] = useState(true);
    const [testing, setTesting] = useState(null); // hostId being tested
    const containerRef = useRef(null);

    // Load hosts on mount
    useEffect(() => {
        const loadHosts = async () => {
            try {
                const hostList = await ListSSHHosts();
                setHosts(hostList || []);
                logger.info('Loaded SSH hosts', { count: hostList?.length || 0 });

                // Get connection statuses
                if (hostList && hostList.length > 0) {
                    const statuses = await GetSSHHostStatus();
                    const statusMap = {};
                    for (const status of statuses || []) {
                        statusMap[status.hostId] = status;
                    }
                    setHostStatuses(statusMap);
                }
            } catch (err) {
                logger.error('Failed to load SSH hosts:', err);
            }
            setLoading(false);
        };
        loadHosts();
        containerRef.current?.focus();
    }, []);

    const handleTestConnection = async (hostId, e) => {
        e.stopPropagation();
        setTesting(hostId);
        try {
            await TestSSHConnection(hostId);
            // Update status to connected
            setHostStatuses(prev => ({
                ...prev,
                [hostId]: { hostId, connected: true, lastError: '' }
            }));
            logger.info('SSH connection test passed', { hostId });
        } catch (err) {
            // Update status with error
            setHostStatuses(prev => ({
                ...prev,
                [hostId]: { hostId, connected: false, lastError: err.toString() }
            }));
            logger.error('SSH connection test failed', { hostId, error: err });
        }
        setTesting(null);
    };

    const handleKeyDown = (e) => {
        if (hosts.length === 0) {
            if (e.key === 'Escape') {
                onCancel();
            }
            return;
        }

        switch (e.key) {
            case 'ArrowDown':
                e.preventDefault();
                setSelectedIndex(prev => (prev + 1) % hosts.length);
                break;
            case 'ArrowUp':
                e.preventDefault();
                setSelectedIndex(prev => (prev - 1 + hosts.length) % hosts.length);
                break;
            case 'Enter':
            case ' ':
                e.preventDefault();
                handleSelect(hosts[selectedIndex]);
                break;
            case 'Escape':
                e.preventDefault();
                logger.info('Host picker cancelled');
                onCancel();
                break;
        }
        // Number keys for quick select
        if (e.key >= '1' && e.key <= '9') {
            const idx = parseInt(e.key, 10) - 1;
            if (idx < hosts.length) {
                e.preventDefault();
                handleSelect(hosts[idx]);
            }
        }
    };

    const handleSelect = (hostId) => {
        logger.info('Host selected', { hostId });
        onSelect(hostId);
    };

    if (loading) {
        return (
            <div className="host-picker-overlay" onClick={onCancel}>
                <div className="host-picker-container" onClick={e => e.stopPropagation()}>
                    <div className="host-picker-loading">Loading SSH hosts...</div>
                </div>
            </div>
        );
    }

    if (hosts.length === 0) {
        return (
            <div className="host-picker-overlay" onClick={onCancel}>
                <div
                    ref={containerRef}
                    className="host-picker-container"
                    onClick={e => e.stopPropagation()}
                    onKeyDown={handleKeyDown}
                    tabIndex={0}
                >
                    <div className="host-picker-header">
                        <h3>No SSH Hosts Configured</h3>
                    </div>
                    <div className="host-picker-empty">
                        <p>Add SSH hosts in <code>~/.agent-deck/config.toml</code>:</p>
                        <pre>{`[ssh_hosts.myserver]
host = "myserver.example.com"
user = "deploy"
identity_file = "~/.ssh/id_ed25519"`}</pre>
                    </div>
                    <div className="host-picker-footer">
                        <span className="host-picker-hint"><kbd>Esc</kbd> Close</span>
                    </div>
                </div>
            </div>
        );
    }

    return (
        <div className="host-picker-overlay" onClick={onCancel}>
            <div
                ref={containerRef}
                className="host-picker-container"
                onClick={e => e.stopPropagation()}
                onKeyDown={handleKeyDown}
                tabIndex={0}
            >
                <div className="host-picker-header">
                    <h3>Select Remote Host</h3>
                    <p className="host-picker-subtitle">Choose an SSH host for the new session</p>
                </div>
                <div className="host-picker-options">
                    {hosts.map((hostId, index) => {
                        const status = hostStatuses[hostId];
                        const isConnected = status?.connected;
                        const hasError = status?.lastError;

                        return (
                            <button
                                key={hostId}
                                className={`host-picker-option ${index === selectedIndex ? 'selected' : ''}`}
                                onClick={() => handleSelect(hostId)}
                                onMouseEnter={() => setSelectedIndex(index)}
                            >
                                <span className={`host-picker-status ${isConnected ? 'connected' : hasError ? 'error' : 'unknown'}`}>
                                    {isConnected ? '●' : hasError ? '○' : '?'}
                                </span>
                                <div className="host-picker-info">
                                    <div className="host-picker-name">{hostId}</div>
                                    {hasError && (
                                        <div className="host-picker-error" title={hasError}>
                                            {hasError.substring(0, 50)}...
                                        </div>
                                    )}
                                </div>
                                <button
                                    className="host-picker-test"
                                    onClick={(e) => handleTestConnection(hostId, e)}
                                    disabled={testing === hostId}
                                    title="Test connection"
                                >
                                    {testing === hostId ? '...' : '🔌'}
                                </button>
                                <span className="host-picker-shortcut">{index + 1}</span>
                            </button>
                        );
                    })}
                </div>
                <div className="host-picker-footer">
                    <span className="host-picker-hint"><kbd>↑↓</kbd> Navigate</span>
                    <span className="host-picker-hint"><kbd>1-9</kbd> Quick select</span>
                    <span className="host-picker-hint"><kbd>Enter</kbd> Select</span>
                    <span className="host-picker-hint"><kbd>Esc</kbd> Cancel</span>
                </div>
            </div>
        </div>
    );
}
