# fix/autoupdate-launchd-and-sweep-20260919

- [x] 1. launchd hygiene: never bootout the service the updater runs inside (defer via pending marker, drained by the next run outside it); bootstrap retries with backoff; verify running, else re-bootstrap from the plist once (WARN with the exact launchctl lines), then fail loudly; fake-launchctl tests
- [x] 2. update.log: append-only audit trail next to debug.log, every line carries trigger, pid, ppid, launchd service and binary version; test
- [x] 3. remote sweep: throttle keyed by controller version so a killed sweep is caught up by the next start; every skip/defer logged with its reason; tests
- [x] 4. pipe reconnect budget per session (backoff up to a minute, one summary line instead of one per attempt); tests
- [x] 5. TUI restart overdue: `tui_restart_overdue` + persistent banner after 2h; per-TUI heartbeat files; `update --check --json` lists running TUIs with pid/version/outdated/reason; tests
- [x] CHANGELOG + docs
- [x] go build/vet, Docker tests, code-simplifier, push, PR
