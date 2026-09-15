# Remote creation field parity

PR 3 for #2170. The controller obtains `add --capabilities --json` protocol
version 1 from the owning host before creating a session. The response derives
`add` and `launch` fields from their registered flag parsers, and includes that
host's tool kinds, model suggestions, configured Claude account names, MCP
names, conductor identities, and option defaults. It contains no credentials
or configuration directories.

Catalog and capacity checks use a WAL-aware read-only registry snapshot, so
uncheckpointed conductor and group changes are visible. Empty registries are
accepted without initializing tables; partial schemas fail visibly. Existing
immutable evidence readers keep their separate filesystem-preservation contract.

Both the public remote CLI and New Session dialog validate requested fields
against this response. An unknown catalog version, unknown option, or malformed
field returns a visible error before creation. If the remote exits with status 2
because it does not recognize `--capabilities`, creation falls back to the legacy
contract: title, path, command, group, worktree/branch, sandbox, additional
repositories, and launch messages. The dialog shows one older-release notice
and hides extra controls. The CLI refuses catalog-only options with the option
name and an instruction to update the remote. Other catalog failures remain
refusals with a short reason; full diagnostics go to the application log.

Project and additional-repository paths belong to the remote. A CLI message file is read
on the controller and forwarded through stdin; its filename is never sent.

| Option | Before | After |
| --- | --- | --- |
| Title, path, tool, group | Forwarded without field negotiation | Forwarded and negotiated; remote custom tools retain host compatibility |
| Model ID | Forwarded with controller suggestions/defaults | Host model suggestions/defaults and explicit override |
| Codex reasoning effort | Refused | `--effort` forwarded, including compatible aliases |
| Claude effort and switches | Extra arguments; unchecked could inherit enabled defaults | Explicit persisted options, including false overrides |
| Claude startup query | Refused | One `launch --startup-query` call, no separate send or mutation replay |
| Resume and continue | Partially forwarded | Forwarded; query combined with resume/continue refused before mutation |
| Multi-repo paths | Refused | Host validation and host-owned combined workspace/context |
| Worktree branch | Forwarded | Host git setup and validation; requested multi-repo checkout failures refuse creation and clean newly owned checkout paths |
| Docker sandbox | Forwarded; expander showed controller config | Host sandbox setup; controller settings suppressed |
| Claude account | Separate remote fetch | One authoritative owner catalog, names only |
| MCP picks | Separate fetch could reset picks | One owner catalog; host tool support/name validation before create |
| Conducting parent | Hidden and omitted | Host conductor catalog and explicit owner session ID |
| Codex/Gemini YOLO | Enabled only | Explicit true and false overrides |
| Hermes YOLO | Refused | Owner-supported true and false overrides |
| Missing directory | Confirmation and retry | Preserved; `--create-dir` negotiated on retry |
| Queue result | Existing add/start queue outcome | Preserved; never attach a queued row |
| Public add/launch flags | Broad forwarding without field catalog | Actual parser catalog and strict field validation |
| Public `add --attach` | Noninteractive SSH caused late attach failure | Shared remote PTY; JSON/nonterminal combinations refused before create |
| CLI help and catalog JSON | No creation catalog | Read-only help and versioned catalog JSON |

A startup query is transient by design. It requires a new session and available
group capacity; queued startup queries are refused with a retry message rather
than saved without their query. Other queued creation retains its queued ID and
status. Unsupported multi-repository option combinations are refused locally
and remotely using the same validation.

Successful creation is separate from attach success. This change does not
claim a release, installed fleet update, live remote acceptance, or parity for
other dialogs and command families. Remote path completion uses observed remote
paths; it never inspects the controller filesystem. Docker's inherited settings
expander remains hidden remotely rather than displaying controller values.

Verification: focused fake-runner, parser/catalog, option validation, dialog,
and ownership regression tests run in isolated Docker. A fixture-backed actual
New Session dialog was captured in a private tmux socket with throwaway HOME.
The fixture frame proves rendering, not a live remote connection.

Independent review R1 added regressions for pending/failed/empty catalog arrow
navigation and custom command typing using the producer's empty-string shell
entry. An empty catalog refuses submission visibly.

Catalog validation is a preflight contract, not a general transaction across
all creation side effects. The one-call startup-query launch route compensates
failed insertion, MCP configuration, and start by stopping its attempted child,
removing its session row, and removing newly owned checkout/workspace paths.
Reused worktrees and original repositories are preserved. If compensation
fails, the error includes the session ID and failing cleanup step for recovery.
Branch references, newly reconciled groups, user-requested directory creation,
and changes made by repository setup scripts are not reverted. Existing MCP
files in original repositories are not restored. Successful creation and later
attach or message delivery remain separate outcomes.

Requested multi-repository Git worktree creation and setup failures now stop
creation rather than warning and symlinking the original checkout. Non-Git
additional directories intentionally remain symlinks. This strict behavior is
used by owner add/launch; existing local TUI fallback policy is unchanged.
