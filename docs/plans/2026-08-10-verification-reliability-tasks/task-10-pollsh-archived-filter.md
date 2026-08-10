# Task 10 — Defensive archived filter in `poll.sh`

**tier: cheap**  
**Parallelism:** independent and disjoint.

## Approved design extract (verbatim)

> - The orchestration heartbeat filters archived children defensively, protecting users of older binaries as well as the current CLI.

## Change

Edit `skills/orchestrate/references/poll.sh`. In each of the three jq child-object pipelines—context extraction around lines 47–54, ID extraction around 63–65, and main processing around 72–79—insert exactly:

```jq
select((.archived // false) | not)
```

Place it before reading fields or aggregating. Keep quoting/error behavior unchanged. Add or extend shell fixture tests with mixed active/archived JSON and missing `archived` fields; active and missing-field rows remain, archived rows disappear in all three paths.

Limitation: older binaries emit no `archived` field, so `// false` is inert for those rows. It avoids jq failure but cannot identify/filter an archived child that the binary does not label; do not describe this as full backward protection.

## Acceptance criteria

- All three pipelines use the predicate and exclude labeled archived rows.
- Missing field is accepted as active; script syntax remains valid.

## Verification

```sh
bash -n skills/orchestrate/references/poll.sh
rg -n -F 'select((.archived // false) | not)' skills/orchestrate/references/poll.sh
```

Expected: syntax exits 0; `rg` prints exactly three matches.

## Interfaces

consumes:
- child JSON optional field `archived: boolean`
- `skills/orchestrate/references/poll.sh` three jq pipelines

produces:
- identical defensive jq filter in all three heartbeat paths

## Record (append-only)

