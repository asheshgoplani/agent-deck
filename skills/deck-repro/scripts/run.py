#!/usr/bin/env python3
"""Run a reviewed binary reproduction in Docker and retain exact evidence."""
import argparse
import hashlib
import json
import pathlib
import subprocess
import time
import uuid


def sha256(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("image", "binary", "script", "fixtures", "out"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--timeout", type=int, default=120)
    args = parser.parse_args()
    binary, script, fixtures = (pathlib.Path(value).resolve(strict=True)
                                for value in (args.binary, args.script, args.fixtures))
    out = pathlib.Path(args.out).resolve()
    out.mkdir(parents=True, exist_ok=False)
    name = "deck-repro-" + uuid.uuid4().hex
    command = ["docker", "run", "--name", name, "--network", "none",
               "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
               "--read-only", "--tmpfs", "/work:rw,exec,mode=1777", "--tmpfs", "/tmp:rw,exec,mode=1777",
               "--mount", f"type=bind,src={binary},dst=/input/agent-deck,readonly",
               "--mount", f"type=bind,src={script},dst=/input/repro.sh,readonly",
               "--mount", f"type=bind,src={fixtures},dst=/fixtures,readonly",
               "--workdir", "/work", "--env", "HOME=/work/home",
               "--env", "XDG_CONFIG_HOME=/work/config", "--env", "XDG_DATA_HOME=/work/data",
               "--env", "XDG_CACHE_HOME=/work/cache", "--env", "XDG_STATE_HOME=/work/state",
               "--env", "XDG_RUNTIME_DIR=/work/runtime", "--env", "TMUX_TMPDIR=/work/tmux",
               "--entrypoint", "/bin/sh", args.image, "-c",
               'mkdir -p "$HOME" "$XDG_RUNTIME_DIR" "$TMUX_TMPDIR"; chmod 700 "$XDG_RUNTIME_DIR" "$TMUX_TMPDIR"; exec /bin/sh /input/repro.sh /input/agent-deck']
    receipt = {"command": command, "binary_sha256": sha256(binary), "script_sha256": sha256(script),
               "fixtures": {str(p.relative_to(fixtures)): sha256(p) for p in sorted(fixtures.rglob("*")) if p.is_file()},
               "container": name, "timeout_seconds": args.timeout}
    started = time.monotonic()
    with (out / "output.log").open("wb") as output:
        try:
            result = subprocess.run(command, stdout=output, stderr=subprocess.STDOUT, timeout=args.timeout)
            receipt["exit_code"] = result.returncode
            receipt["timed_out"] = False
        except subprocess.TimeoutExpired:
            try:
                subprocess.run(["docker", "stop", "--time", "1", name], stdout=output, stderr=subprocess.STDOUT, timeout=10)
            except (OSError, subprocess.TimeoutExpired) as error:
                receipt["cleanup_error"] = str(error)
            receipt["exit_code"] = None
            receipt["timed_out"] = True
        except OSError as error:
            receipt.update(exit_code=2, timed_out=False, harness_error=str(error))
            output.write((str(error) + "\n").encode())
    receipt["output_sha256"] = sha256(out / "output.log")
    receipt["duration_seconds"] = round(time.monotonic() - started, 3)
    try:
        inspected = subprocess.run(["docker", "inspect", name], capture_output=True, text=True, timeout=10)
        (out / "container.json").write_text(inspected.stdout)
        if inspected.returncode == 0:
            receipt["experiment"] = {"image_id": json.loads(inspected.stdout)[0]["Image"], "timeout_seconds": args.timeout}
    except (OSError, subprocess.TimeoutExpired) as error:
        receipt["inspect_error"] = str(error)
    (out / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")
    print(json.dumps({"receipt": str(out / "receipt.json"), "exit_code": receipt["exit_code"], "timed_out": receipt["timed_out"]}))
    return 2 if receipt["timed_out"] else receipt["exit_code"]


if __name__ == "__main__":
    raise SystemExit(main())
