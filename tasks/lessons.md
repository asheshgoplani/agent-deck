# Lessons

- Delayed subprocess regression fixtures must register gate-release cleanup before waiting for entry and explicitly join every worker after a timeout. Acquiring the worker's mutex does not prove that worker has run.
- Docker-mounted Git worktrees need accessible repository metadata or `-buildvcs=false` for nested fixture builds. Expected-red checks must match the specific behavioral assertion, not merely the test name.
- Compare total observable subprocess calls across implementations. Protocol-specific delays establish TTL stress, not a representative speedup ratio.
- A tmux fake must distinguish `pane_pid` from `pane_dead` queries. Returning PID 1 for a liveness query reports the pane as dead and prevents the intended polling path.
- Construct tmux fixtures with the real tool command so status detection loads the correct busy patterns. Assert exact baseline call counts to detect accidental bypass of the metadata path.
- Hosted-runner checkout ownership may differ from container UID 1000. Make only required module metadata writable when using `-mod=mod`.
- Do not infer "hook X never fires" from the absence of a transcript record type; check the artifact the hook itself writes (the hook status file's ts against the turn's end time). Rebuild a commit rather than following it with a "remove binary" commit when a build artifact lands in it: history, not the tip, is what gets pushed.
