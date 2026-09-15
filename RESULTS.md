# PR #2290: status pass ownership refresh

PR: https://github.com/asheshgoplani/agent-deck/pull/2290

Base: `7d2302fb8a41c5fcab7a441d56729b4b6547885a`.
Reviewed head: `49d69b468a63529ca69611e3d8fc5ee6a5e3dde5`.

## Root cause

The original quadratic tmux environment scan is confirmed. Round 1 shared the scan but held the process-wide ownership mutex during subprocess reads. A bootstrap refresh could therefore block authoritative hook publication under another instance's lock.

Round 2 moves refresh I/O outside cache, pass, bootstrap-selection, and status-instance locks. Refreshes coalesce per socket. Publications made during refresh override older enumeration results. Failed enumeration remains unknown. The status path rechecks binding, lifecycle, and status after reacquiring its instance lock.

## Reproduction

From the PR checkout, on a machine with Docker:

```sh
scripts/ci/status-pass-regression.sh /tmp/status-pass-receipts
```

The script records immutable base/reviewed/head revisions and Docker image digest. It copies identical fixtures into detached worktrees and executes tests in the same image as UID/GID 1000, with no network and all capabilities dropped. Only dependency preparation has network access. The baseline's test-only pass adapter delegates to unchanged `Instance.UpdateStatus`; it does not add caching or change production source.

Expected red assertions: baseline production sweep and CLI subprocess bounds; reviewed-head delayed refresh blocks readers/hook rotation. Fixed-head checks include the actual background worker sweep beyond the two-second TTL, cached environment rotation after the existing 30-second TTL, claim merging, failure handling, golden rows, and race detection.

## Verification status

- Initial isolated host build and targeted vet passed. No host tests ran.
- Local Docker test attempt could not create a container: engine HTTP 500. Engine `/_ping` also timed out. No restart or other shared-engine mutation was performed.
- Docker receipts and exact-head CI are pending the PR workflow. No test-pass claim is made before these complete.

## Limits

Synthetic tmux fixtures establish call bounds and synchronization behavior. They do not measure the user's live profile or prove native Codex process rotation on macOS. The environment rotation fixture exercises actual cache expiry; the delayed hook fixture proves authoritative publication remains responsive during a peer refresh.

CLI live status validation remains in place because storage has no per-row freshness timestamp. No new CLI schema, remote-execution surface, merge, deployment, or release. The PR remains a draft, parked for September 25, 2026.
