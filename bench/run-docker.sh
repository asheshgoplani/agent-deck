#!/bin/sh
# Build dependencies with networking, execute the suite without networking or host HOME.
set -eu
cd "$(dirname "$0")/.."
# Match the checkout owner so bind-mounted reports remain writable/readable on
# Linux runners (whose account is not necessarily UID 1000).
runner_uid=$(id -u)
runner_gid=$(id -g)
if [ "$runner_uid" = 0 ]; then
 printf '%s\n' 'Run the benchmark from a non-root account.' >&2
 exit 1
fi
image=${BENCH_IMAGE:-agentdeck-bench:go1.25}
if [ -z "${BENCH_IMAGE:-}" ]; then
 docker build -t "$image" -f bench/Dockerfile .
fi
cache=${BENCH_CACHE:-agentdeck-bench-build}
# Named caches need one container-side ownership setup for non-root Go execution.
docker run --rm --network none --cap-drop ALL -v "$cache":/cache "$image" chmod 1777 /cache
revision=$(git rev-parse HEAD)
docker run --rm --init -u "$runner_uid:$runner_gid" --network none --cap-drop ALL \
 -v "$PWD":/src -w /src -v "$cache":/tmp/gocache \
 -e HOME=/tmp/h -e GOCACHE=/tmp/gocache -e GOTOOLCHAIN=local -e GOFLAGS=-p=2 -e GOPROXY=off -e GOSUMDB=off \
 -e AGENTDECK_SKIP_UPDATE_CHECK=1 -e BENCH_REVISION="$revision" \
 "$image" sh -eu -c '
  mkdir -p /tmp/h /tmp/bench-bin
  go build -o /tmp/bench-bin/agent-deck ./cmd/agent-deck
  go build -o /tmp/bench-bin/bench ./tools/bench
  go test -c -o /tmp/bench-bin/ui.test ./internal/ui
  AGENTDECK_BENCH_ISOLATION_TEST=1 go test ./tools/bench -count=1
  /tmp/bench-bin/bench -binary /tmp/bench-bin/agent-deck -ui-harness /tmp/bench-bin/ui.test -revision "$BENCH_REVISION" "$@"
 ' sh "$@"
