# Standing orders

One `## name` section per order. `agent-deck orders tick` runs each due `check:`
(shell, 60 s timeout, no AI). Same output as last time: nothing is sent.
Changed: the conductor gets one message with the change, `do:` and `ask_first:`.

Copy this file to your conductor's directory (`agent-deck conductor status --json`
shows it) or let the conductor write it with `agent-deck orders add`.

## watch-my-prs
every: 30m
active: 08:00-22:00 Europe/Berlin
check: gh pr list --author @me --json number,statusCheckRollup -q '.[] | [.number, ([.statusCheckRollup[].conclusion] | unique)] | @json'
do: for every PR whose checks are all SUCCESS, land it; for a red one, start one worker to fix it fail-first. Tell me in one line what you did.
ask_first: merge, force-push, closing a PR, posting comments
max_wakes_per_day: 6
expires: 2026-12-31

## watch-main-ci
every: 15m
check: gh run list -b main -L 1 --json conclusion,headSha -q '.[0] | [.conclusion, .headSha[0:7]] | @tsv'
do: if main is red, start one worker to find the failing test and fix it fail-first; tell me in one line
ask_first: merge, push
max_wakes_per_day: 4
