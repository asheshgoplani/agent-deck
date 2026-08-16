package session

// This file is documentation, not code: it is the enumeration the "a remote
// session never resolves its conversation against local disk" rule (#1851) is
// only as good as.
//
// A rule described as unconditional has to be enforced at EVERY entry point,
// and the previous attempt at this epic was faulted for closing two of three
// doors while the commit message called the rule unconditional. So the doors
// are listed here, each one gated on Instance.TranscriptIsResolvableLocally(),
// and TestRemoteTranscriptBoundary_EveryEntryPointRefuses in
// issue1851_remote_transcript_boundary_test.go exercises the list.
//
// Why a hit is worse than a miss: an --ssh session stores a LOCAL placeholder
// in ProjectPath (defaulting to the directory `add --ssh` ran in). Every lookup
// below is keyed on that placeholder or scans all local project dirs, so for a
// remote session it does not fail to find a transcript — it finds a LOCAL
// session's transcript.
//
// The doors, in package session:
//
//  1. ensureClaudeSessionIDFromDisk        (instance.go) — Start-path adoption
//  2. ensureClaudeSessionIDFromDiskForRestart (instance.go) — the #1851 report
//  3. findLatestClaudeTranscriptOnDisk     (instance.go) — writes the adopted id
//     back onto the instance AND into the pane's tmux env (GetLastResponseBestEffort)
//  4. GetJSONLPath                         (instance.go) — analytics, transition
//     notifier, TUI, and GetJSONLPathChecked all funnel through it
//  5. getClaudeLastResponse                (instance.go) — `session output`
//  6. sessionHasConversationData           (instance.go) — resume/bind decisions
//  7. sessionConversationByteSize          (instance.go) — bind tiebreaker
//  8. sessionConversationMtime             (instance.go) — bind tiebreaker
//  9. findSessionFileInAllProjects         (instance.go) — scans every project dir
// 10. claudeTranscriptPathIn               (handoff.go) — CONSTRUCTS a path when
//     nothing is found, so it never returns "not here" on its own
// 11. BuildClaudeToCodexHandoffPrompt      (handoff.go) — hands a whole transcript
//     to another tool
// 12. LocateConversationConfigDir          (migrate_locate.go) — its caller
//     (switch-account) MIGRATES the file it names
// 13. MigrateConversation                  (migrate.go) — same, directly
// 14. RestoreOrphanedConversationBackup    (migrate.go) — restores a .bak over a
//     local project dir
//
// Outside package session:
//
// 15. internal/ctxinspect/sessionhost.BuildRequest — its retry ladder re-resolves
//     against per-instance config dirs
// 16. cmd/agent-deck streamSessionSend — polls for a local transcript that can
//     never appear
//
// claudeTranscriptDir (instance.go) is deliberately NOT gated: it is a collision
// KEY, never opened as a path, and it is keyed on the remote location precisely
// so two remote sessions sharing a placeholder stop being judged co-located. See
// its doc comment for why changing that key is only safe alongside gates 3-5.
