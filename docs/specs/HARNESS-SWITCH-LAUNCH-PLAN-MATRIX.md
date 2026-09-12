# Cross-harness launch-plan matrix

This is a source-only, plan-only adapter contract. `BuildFreshTargetLaunchPlan`
consumes an exact-ID `ContextExport` and describes a **fresh, distinct** target;
it does not stop the source, copy source files, write a registry, or claim target
readiness.

| Source → target | Export | Native target shape | Target account |
|---|---|---|---|
| Claude → Codex | `ExportClaudeContext` | `codex -C <cwd> <context>` | configured Codex home or default |
| Claude → Pi | `ExportClaudeContext` | `pi --session-dir ${HOME}/.pi/agent-deck/<target> ...` | Pi default only |
| Codex → Claude | `ExportCodexContext` | `claude --session-id <target-uuid> <context>` | configured Claude config dir or default |
| Codex → Pi | `ExportCodexContext` | Pi instance-scoped `--session-dir` | Pi default only |
| Pi → Claude | `ExportPiContext` | `claude --session-id <target-uuid> <context>` | configured Claude config dir or default |
| Pi → Codex | `ExportPiContext` | `codex -C <cwd> <context>` | configured Codex home or default |

Target IDs are deterministic for the source instance/session, target harness and
account, so repeated previews describe the same target without reusing the
source ID. Claude receives a deterministic native session UUID. Pi mints its
native session-header ID inside the target's instance-scoped directory, while
Codex has no native fresh-session ID flag; the distinct Agent Deck target
instance ID is the stable target identity for both.

All plans explicitly disclose bounded transfer, missing native state, source
process/files/credentials/attachments/tool state, and unverified target
readiness/authentication. `Executable=false` and `Ready=false` are invariant.
Named Pi accounts fail with `pi-account-unsupported`; Pi's default is the only
supported Pi account semantics.

## Execution and evidence matrix

The shared `ExecuteCrossHarnessSwitch` service now creates and persists the
**distinct** target through an injected store, starts it through an injected
lifecycle, and persists a source/target operation journal. It never stops or
rewrites the source instance, process, transcript, account, child links, or
routing. It only reports target-ready after an injected target-native observer
returns the matching target instance ID, a fresh native ID, and a ready event.

| Path | CLI/TUI action | Success claim | Current runtime evidence |
|---|---|---|---|
| Claude/Codex/Pi → different Claude/Codex/Pi | explicit target picker + loss confirmation creates a distinct target | only on correlated observer evidence | Claude/Codex use fresh per-instance hook evidence; Pi uses a fresh exact native session document; semantic context acceptance remains pending |
| Same-harness Claude account | existing native journal path | existing native verifier only | unresolved findings remain release-blocking |
| Same-harness Codex account | existing native journal path | existing native verifier only | unresolved findings remain release-blocking |
| Pi named account | refused | never | Pi has default-account semantics only |

`session switch` requires `--confirm-context-loss` for a cross-harness action;
`session switch-preview` discloses the bounded export and excluded context
first. The TUI reuses the target account/tool edit flow and presents the same
loss disclosure in an explicit cancel-default confirmation before creation. If a harness cannot emit the required native evidence, the operation returns
an explicit pending result with the exact missing contract; it never promotes a
launch, old artifact, or configured account to readiness. Existing native
switch/journal paths remain separate from this cross-harness lifecycle and are
not represented as fully resolved by this matrix.
