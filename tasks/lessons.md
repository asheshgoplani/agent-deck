# Lessons

- Delayed subprocess regression fixtures must register gate-release cleanup before waiting for entry and explicitly join every worker after a timeout. Acquiring the worker's mutex does not prove that worker has run.
- Docker-mounted Git worktrees need accessible repository metadata or `-buildvcs=false` for nested fixture builds. Expected-red checks must match the specific behavioral assertion, not merely the test name.
- Compare total observable subprocess calls across implementations. Protocol-specific delays establish TTL stress, not a representative speedup ratio.
