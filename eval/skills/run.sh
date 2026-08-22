#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
IMAGE="${SKILL_EVAL_IMAGE:-agent-deck-go-test:latest}"
JOBS=4
REMOTE=""
while (($#)); do case "$1" in --jobs) JOBS="$2"; shift 2;; --remote) REMOTE="$2"; shift 2;; *) echo "unknown argument: $1" >&2; exit 2;; esac; done
if [[ "$REMOTE" == g14 ]]; then
  GATE=/home/ashesh/projects/agent-deck-control/overnight/gate-runner.sh
  if [[ -x "$GATE" ]] && "$GATE" skill-evals "$ROOT" container-targeted ./eval/skills/run.sh; then exit 0; fi
  echo "g14 offload unavailable; falling back locally" >&2
fi
BUILD_DIR="$(mktemp -d /tmp/agent-deck-skill-eval.XXXXXX)"
BIN="$BUILD_DIR/agent-deck"
trap 'rm -rf -- "$BUILD_DIR"' EXIT
chmod 0777 "$BUILD_DIR"
docker run --rm -v "$ROOT:/src:ro" -v "$BUILD_DIR:/out" -w /src "$IMAGE" go build -o /out/agent-deck ./cmd/agent-deck
SKILL_EVAL_HOST_LAUNCHER=1 python3 "$ROOT/eval/skills/run.py" --jobs "$JOBS" --image "$IMAGE" --binary "$BIN" --skill "$ROOT/skills/agent-deck"
