# Group activity aggregation

The candidate patch aggregates activity flags once per group before propagating
flags to ancestors. This preserves archive filtering, empty-group omission and
active-status rules. It affects list rebuilding in active-on-top and
populated-on-top views. It does not change the normal view or persisted state.

## Results

Linux arm64 Docker on Apple Silicon, Go 1.25, `GOMAXPROCS=2`, warm build cache.
Each value below is the median of five Go benchmark samples. The deterministic
fixture distributes sessions over nested `work/team-{i%3}/project-{i%10}` paths,
with one running session per three sessions. Setup is excluded from timing.

| Sessions | Before ns/op | After ns/op | Runtime change | Before B/op | After B/op | Before allocations/op | After allocations/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 10 | 3,691 | 4,182 | +13.3% | 1,592 | 1,592 | 35 | 35 |
| 100 | 52,068 | 15,069 | -71.1% | 12,264 | 6,104 | 309 | 99 |
| 500 | 218,711 | 22,769 | -89.6% | 47,464 | 6,104 | 1,509 | 99 |

At 500 sessions, allocated bytes decrease 87.1% and allocation count decreases
93.4%. Allocation counts repeated exactly in an earlier five-sample run.
The 10-session case has one session per group and shows no allocation benefit;
its measured runtime increased 13.3%.

The host had concurrent builds and tests. Runtime samples are noisy and the
before/after runs were sequential, so these percentages do not establish a
precise speedup. This is a focused method benchmark, not an end-to-end frame
latency result. The full-fleet CI comparison below independently measures the
same method inside the suite.

Raw warm samples: [before](evidence/group-before.txt),
[after](evidence/group-after.txt).

## Tested source identity

Both runs used base `7d2302fb8a41c5fcab7a441d56729b4b6547885a`, plus the same
new `internal/session/group_activity_bench_test.go`. The after run additionally
used the uncommitted production patch in a separate detached worktree. These
results are not stamped to a later suite or improvement commit.

SHA-256 receipts:

| Artifact | SHA-256 |
| --- | --- |
| Benchmark/test source | `fea310b09a649be25eea04e30bce2261b774c59539ad2d158c8962e734a0d836` |
| Before `internal/session/group_view_mode.go` | `f4579040eaea7144d0ad7c1f787d4424e5526a46a9ca762bfee9946fce616b85` |
| After `internal/session/group_view_mode.go` | `47115654a5a162e6942a45f84c74a2d70dc720ea53d23977e1bd9746d999e53f` |
| Production-only patch | `7feb9146ed79500419cd14bd0e6b66a513796a0681b34c7353ee50eca60f60da` |

## Commands and checks

The exact Docker options and benchmark command follow. `BENCH_SOURCE` denotes
the unchanged or patched checkout; it replaces the original temporary host
path. Run once for each checkout. The named caches were prepared writable for
UID 1000. No host Go tests or host tmux access were used.

```sh
docker run --rm --init -u 1000:1000 --network none --cap-drop ALL \
  -v "$BENCH_SOURCE":/src -w /src \
  -v agentdeck-gomod:/tmp/gomod \
  -v agentdeck-perf-improvement-cache:/tmp/gocache \
  -e HOME=/tmp/h -e GOMODCACHE=/tmp/gomod -e GOCACHE=/tmp/gocache \
  -e GOMAXPROCS=2 -e GOFLAGS=-mod=mod \
  agentdeck-2284-test:latest sh -c 'mkdir -p "$HOME"; go test ./internal/session -run "^$" -bench "^BenchmarkGroupActivityMap$" -benchmem -count=5'
```

The local test image included Go 1.25 and tmux. Its observed image ID was
`sha256:899e272b82d1e0b1a5d4a11fa89ff803e8d52e9b25605aa3fe2b635b129ea6a6`.
The local image tag is an evidence identifier, not a published portable image.

The new archive/ancestor regression passed before and after. Existing session
partition tests and UI group view wiring, navigation, tick handling and
`NewSessionFlow_Golden` passed after the patch. The following command ran inside
the same isolated Docker setup, with default `GOMAXPROCS`:

```sh
go test ./internal/session ./internal/ui \
  -run 'Test(GroupActivityMap|Partition|ActiveTopWiring|PopulatedTopWiring|CycleGroupView|SkipDivider|MouseWheelSkipsDivider|TickRepartitions|ViewModePartition|NewSessionFlow_Golden)' \
  -count=1
```

Observed results: session package `ok` in 0.077s; UI package `ok` in 0.595s.
This focused validation does not claim the full repository test suite passed.

## Full fleet CI before/after

Both isolated N=3 Linux amd64 jobs completed the 10/100/500 matrix successfully:
[before run 34982145641](https://github.com/asheshgoplani/agent-deck/actions/runs/34982145641)
and [after run 34982235286](https://github.com/asheshgoplani/agent-deck/actions/runs/34982235286).
The baseline is [github-ubuntu-latest.json](baseline/github-ubuntu-latest.json);
the candidate is [group-fleet-after.json](evidence/group-fleet-after.json).

| Sessions | Group map before p50 / p95, ms | After p50 / p95, ms | p50 change |
|---:|---:|---:|---:|
| 10 | 0.003874 / 0.003999 | 0.003806 / 0.004222 | -1.74% |
| 100 | 0.037209 / 0.038333 | 0.012386 / 0.019158 | -66.71% |
| 500 | 0.151713 / 0.250197 | 0.014473 / 0.021947 | -90.46% |

Across all 72 matching metrics, no candidate p95 exceeds the baseline by 25%.
At 500 sessions the group-map p50 decreases 90.46%, while key-repeat frame p95
changes only +0.37%. This is evidence for the focused method improvement, not
a claim of a comparable end-to-end rendering speedup.

These first CI jobs were capture-only. The 25% result comes from comparing
their downloaded raw artifacts with matching platform, machine class, seed,
run count and metric sets. The subsequently committed runner baseline enables
the actual advisory comparator in future CI. N=3 hosted-runner timings remain
exploratory.

GitHub tested merge revisions `d7407a82e2190f814c2710097a50c209116f1a8e`
(before, suite head `f5ebb6d350c17b5c634b77ee7084c1767d3e9eac`) and
`a3b68f9a3380cc031de032c674fe48b61bddc676`
(after, improvement head `efe2807a29db4492156748377fa22f62ce2aed9c`).
The later rebase carries the fixture path-helper lint fix and committed Linux
baseline; it leaves the measured group implementation unchanged. Final-head
CI is checked separately from these source-stamped before/after artifacts.

The two later active comparisons on the unchanged rebased head completed
measurement but flagged different non-group rows. They are retained in
[run 34983096493, attempts 1 and 2](https://github.com/asheshgoplani/agent-deck/actions/runs/34983096493).
The earlier zero-regression artifact comparison above remains a result of those
specific runs, not a promise that every repeat passes. See REPORT.md for the
calibration finding. CI makes this an explicit advisory warning while keeping
measurement and correctness failures blocking; the threshold is unchanged.
