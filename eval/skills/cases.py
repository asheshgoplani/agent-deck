"""Published machine-readable skill-eval case catalogue."""

CASES = [
    # Core realistic tasks required by the standing programme.
    ("create-session", "Create a Claude session named Eval Project in /work.", "agent-deck add -t 'Eval Project' -c claude /work", "add"),
    ("find-session", "Find the session named Eval Project without scanning transcript files.", "agent-deck session search 'Eval Project'", "session search"),
    ("read-output", "Read the latest output from session Eval Project.", "agent-deck session output 'Eval Project'", "session output"),
    ("attach-detach", "Attach to Eval Project and identify the documented detach key.", "agent-deck session attach 'Eval Project'", "session attach / Ctrl+Q"),
    ("switch-model", "Switch Eval Project to the opus model.", "agent-deck session set 'Eval Project' model opus", "session set"),
    ("drain-inbox", "Drain pending inbox messages until processing is complete.", "agent-deck inbox drain --until-done", "inbox drain --until-done"),
    ("add-remote", "Register SSH host build@example as remote buildbox.", "agent-deck remote add buildbox build@example", "remote add"),
    ("recover-errored-session", "Recover the errored session Eval Project.", "agent-deck fleet recover 'Eval Project'", "fleet recover"),
    # One case for every top-level command documented by cli-reference.md.
    ("top-add", "Create a session named Demo in /work.", "agent-deck add -t Demo /work", "add"),
    ("top-launch", "Create and start a Claude session in /work.", "agent-deck launch /work -c claude", "launch"),
    ("top-list", "List all sessions as JSON.", "agent-deck list --json", "list --json"),
    ("top-remove", "Remove the session Demo.", "agent-deck remove Demo", "remove"),
    ("top-status", "Get the cheap machine-readable fleet status.", "agent-deck status --json", "status --json"),
    ("top-migrate-paths", "Preview migration to XDG paths without changing data.", "agent-deck migrate-paths --dry-run", "migrate-paths --dry-run"),
    ("top-web", "Inspect how to start the browser UI.", "agent-deck web --help", "web"),
    ("top-session", "Show session Demo as JSON.", "agent-deck session show Demo --json", "session show"),
    ("top-fleet", "Show fleet status.", "agent-deck fleet status", "fleet status"),
    ("top-worktree", "List managed worktrees.", "agent-deck worktree list", "worktree list"),
    ("top-mcp", "List available MCP servers.", "agent-deck mcp list", "mcp list"),
    ("top-skill", "List available skills.", "agent-deck skill list", "skill list"),
    ("top-group", "List groups.", "agent-deck group list", "group list"),
    ("top-profile", "List profiles.", "agent-deck profile list", "profile list"),
    ("top-conductor", "Show conductor status.", "agent-deck conductor status", "conductor status"),
    ("top-remote", "List registered remotes.", "agent-deck remote list", "remote list"),
    ("top-codex-hooks", "Inspect Codex hook setup options.", "agent-deck codex-hooks --help", "codex-hooks"),
    ("top-deepseek", "Inspect DeepSeek setup options.", "agent-deck deepseek --help", "deepseek"),
]
