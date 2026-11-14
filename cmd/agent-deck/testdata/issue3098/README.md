# issue3098: custom pi footer and `launch -m`

Offline reproduction for the built-in detection-pattern overrides fix (follow-up to #3098).
It runs the real agent-deck binary against a fake pi that draws a custom footer.
`fixtures/pi.toml` adds one `[tools.pi].prompt_patterns_extra` regex that matches that footer.
No real pi, no model and no credentials are involved.

- `repro.sh <agent-deck>` launches `agent-deck launch -c pi -m READY_PROBE` and captures the pane.
- `fixtures/bin/pi` prints the footer and `ACCEPTED_READY_PROBE` for each line it reads.

Exit codes:

| Exit | Meaning |
|---|---|
| 0 | Healthy: launch succeeded and the task was delivered exactly once |
| 1 | Defect: launch timed out (124) with the footer on screen and no task delivered |
| 2 | Harness or setup error |

## Run

Build each binary in a container, then run it through the repro runner (network disabled, fresh HOME, private tmux socket):

```sh
dir=cmd/agent-deck/testdata/issue3098
out=$(mktemp -d)
docker build -t deck-repro-3098 "$dir"
docker run --rm -v "$PWD:/src:ro" -v "$out:/out" -w /src -e GOFLAGS=-buildvcs=false \
  golang:1.26.10-bookworm go build -o /out/agent-deck ./cmd/agent-deck
python3 skills/agent-deck/scripts/repro/run.py --image deck-repro-3098 \
  --binary "$out/agent-deck" --script "$dir/repro.sh" --fixtures "$dir/fixtures" \
  --out "$out/run"
```

On `f7017710` (before the fix) it exits 1. With the fix it exits 0.
