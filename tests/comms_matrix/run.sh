#!/usr/bin/env bash
# Own only this invocation's compose project, keys, containers and tagged images.
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
export COMMS_GROUP=${1:-all}
case "$COMMS_GROUP" in all|claude|codex|others|failures) ;; *) echo "Unknown group: $COMMS_GROUP" >&2; exit 2 ;; esac
output=${2:-"$root/artifacts/comms-matrix/$COMMS_GROUP"}
mkdir -p "$output"
export COMMS_OUTPUT=$(cd "$output" && pwd)
project="comms-$(date +%s)-$$-${RANDOM}"
export COMMS_IMAGE="agent-deck-comms:$project"
base_image="agent-deck-comms-base:$project"
compose=(docker compose -p "$project" -f "$root/tests/comms_matrix/compose.yaml")
cleanup() {
  status=$?
  trap - EXIT
  "${compose[@]}" logs --no-color > "$COMMS_OUTPUT/compose.log" 2>&1 || true
  "${compose[@]}" down --volumes --remove-orphans > "$COMMS_OUTPUT/cleanup.log" 2>&1 || status=1
  docker image remove "$COMMS_IMAGE" "$base_image" >> "$COMMS_OUTPUT/cleanup.log" 2>&1 || status=1
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
git -C "$root" rev-parse HEAD > "$COMMS_OUTPUT/source-sha.txt"
docker build --target comms-base -t "$base_image" -f "$root/sandbox/Dockerfile" "$root/sandbox"
docker build --build-arg "COMMS_BASE_IMAGE=$base_image" -t "$COMMS_IMAGE" -f "$root/tests/comms_matrix/Dockerfile" "$root"
docker image inspect --format '{{.Id}}' "$COMMS_IMAGE" > "$COMMS_OUTPUT/image-id.txt"
docker run --rm --network none "$COMMS_IMAGE" python3 -m unittest discover -s /workspace/tests/comms_matrix -p test_contracts.py
"${compose[@]}" up -d --wait r1
"${compose[@]}" run --rm -T local
