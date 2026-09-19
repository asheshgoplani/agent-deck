# Stage events

One JSON object per line, appended to a JSONL file as a PR moves through the
pipeline. This replaces per-merge receipts, hash readbacks and metrics prose:
the conductor records one line per transition and `stage-report.py` turns the
file into cycle time, ready-to-merge wait and blocked time after the fact.

## Schema

| Field   | Type   | Required | Meaning |
|---------|--------|----------|---------|
| `pr`    | int    | yes      | Pull request number |
| `stage` | string | yes      | One of the stages below |
| `ts`    | string | yes      | RFC3339 timestamp in UTC, e.g. `2026-09-06T14:03:11Z` |
| `actor` | string | yes      | GitHub login of whoever caused the transition (or the conductor's name) |
| `repo`  | string | yes      | `owner/name` |
| `note`  | string | no       | Free text, kept short |

Stages, in the order a PR normally passes through them:

| Stage            | When to record it |
|------------------|-------------------|
| `arrived`        | The PR was opened (use the GitHub `created_at` time when backfilling) |
| `discovered`     | The conductor first saw the PR |
| `triaged`        | Intake label applied or a human decided what to do with it |
| `review_started` | Review work actually began |
| `verdict`        | A review verdict was posted (approve, changes requested, close) |
| `ci_green`       | Required checks passed on the head that will merge |
| `merged`         | The PR was merged |
| `closed`         | The PR was closed without merging |

Unknown stage names are rejected by `stage-event.sh`. A PR can carry several
events with the same stage (for example two `verdict` lines after a re-review);
the report uses the first occurrence of each stage per PR.

## Example

```jsonl
{"pr": 2135, "stage": "arrived", "ts": "2026-09-06T09:12:40Z", "actor": "contributor", "repo": "asheshgoplani/agent-deck"}
{"pr": 2135, "stage": "discovered", "ts": "2026-09-06T09:20:02Z", "actor": "conductor", "repo": "asheshgoplani/agent-deck"}
{"pr": 2135, "stage": "merged", "ts": "2026-09-06T11:41:19Z", "actor": "asheshgoplani", "repo": "asheshgoplani/agent-deck", "note": "squash"}
```

## Recording an event

```bash
conductor/scripts/stage-event.sh <pr> <stage> [note]
```

The script validates the stage, stamps `ts` with the current UTC time and
appends one line. Configuration comes from the environment:

| Variable             | Default                                       |
|----------------------|-----------------------------------------------|
| `STAGE_EVENTS_FILE`  | `./stage-events.jsonl` in the current directory |
| `STAGE_EVENTS_ACTOR` | `$GITHUB_ACTOR`, then `$USER`, then `conductor` |
| `STAGE_EVENTS_REPO`  | `$GITHUB_REPOSITORY`, then `asheshgoplani/agent-deck` |

## Reporting

```bash
python3 conductor/scripts/stage-report.py stage-events.jsonl \
    --maintainer asheshgoplani --since 2026-09-01 --until 2026-09-08
```

The report prints, for each consecutive stage transition, the median and p90
duration in hours, split into rows where the later event's `actor` is the
maintainer and rows where it is anyone else. It also prints open-to-merge
(`arrived` to `merged`) and discovered-to-merge (`discovered` to `merged`).
Only stdlib is used, so it runs anywhere `python3` does.
