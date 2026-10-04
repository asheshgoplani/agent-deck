# Isolation and evidence contract

## Docker runner

Prepare a Linux executable binary and a POSIX shell reproduction script. The script receives the absolute binary location as `$1`. Define its exit codes: 0 means the expected behavior holds, 1 means the **specific** defect was observed, 2 or greater means setup/harness error. Check every setup operation. Do not convert arbitrary nonzero exits into 1. Use explicit output assertions and save the observed output.

```
python3 "$SKILL_DIR/scripts/run.py" --image IMAGE_DIGEST --binary BUILD/agent-deck \
  --script CASE/repro.sh --fixtures CASE/fixtures --out RECEIPT --timeout 120
```

The image must already contain required dependencies such as tmux and a shell. No host credentials or Docker socket are mounted. Input mounts are read-only. `/work` is ephemeral; HOME, XDG directories and tmux temporary directory are under it. Networking is disabled. The runner retains a uniquely named stopped container for inspection on timeout, never stops another container, and reports timeout as a harness error. Inspect and remove only your own retained containers through the normal Docker cleanup command when finished.

Build binaries separately in an isolated build container. Record its image digest, source SHA and complete build command. Dependencies may require networking at build time; reproduction runs should use offline fixtures. A developer test binary is supplementary evidence, not a substitute for a claimed CLI reproduction. For internal races not externally observable, explicitly state that binary evidence is unavailable and report source-level proof separately.

## result.json

Use this schema (paths are relative to the report directory):

```json
{
  "schema_version": 1,
  "candidate_id": "issue-or-local-id",
  "status": "reproduced",
  "issue": "public issue URL or description",
  "oracle": "Precise defect signature and healthy behavior",
  "affected_revision": "full commit SHA",
  "fixed_revision": null,
  "reproduction": {"script": "repro.sh", "fixtures": "fixtures/", "old_receipt": "old/receipt.json", "fixed_receipt": null},
  "regression": {"test": null, "old_receipt": null, "fixed_receipt": null},
  "limits": []
}
```

Allowed status: `reproduced`, `not reproduced`, `blocked`, `fix unverified`, `fixed`. Do not infer the status from exit code alone. The investigator must match the actual log against the oracle. A `fixed` record needs non-null fixed revision and both reproduction receipts plus a named regression test and both regression receipts. Empty, absent, timed out, or unrelated-failure receipts do not qualify.

`RESULTS.md` includes the input, environment, exact steps, expected/actual behavior, revision and binary hashes, old and fixed outcomes, regression red/green outcomes, required-gate outcomes, and limitations. For race defects state the iteration count and that a finite green run cannot establish mathematical absence.

## Disposable HOME alternative

Use only for a reviewed, trusted repro when Docker is unavailable. Create a new task-owned directory, clear inherited environment with `env -i`, set HOME and all XDG roots there, use a task-owned cwd and restrictive permissions, and route every tmux invocation through an explicit private socket under that directory. Unset agent/session/profile/credential variables by constructing an allowlisted environment. Capture the same metadata as the Docker runner. Do not use this alternative for Go tests when project policy requires containers or a named test host.

## Validation

After reviewing each receipt log against the declared oracle, add `oracle_matched: true` and `oracle_evidence` containing the exact relevant output excerpt. The runner records `output_sha256`; retain it. Run `python3 "$SKILL_DIR/scripts/validate.py" PATH/result.json`. The validator rejects incomplete fixed claims and changed reproduction inputs. It validates structure and hashes, not whether an investigator's interpretation is correct; independent review remains necessary. Contributor self-check invokes it with `--require-status fixed` through `REPRO_REPORT=PATH/result.json`; blocked observations cannot satisfy a bug-fix gate.

For `blocked` or `not reproduced`, include nonempty `attempts`: each entry has a concrete `reason` describing the attempted investigation and its limit, and a `receipt` path whenever a command was run. A missing execution receipt is not a completed reproduction attempt. The caller must label preparatory investigation separately.
