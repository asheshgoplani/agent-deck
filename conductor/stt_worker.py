#!/usr/bin/env python3
"""
STT worker: transcribes an audio file using parakeet-mlx CLI.
Runs as a subprocess to isolate inference from the bridge event loop.

Usage:
    python stt_worker.py /path/to/audio.ogg
    python stt_worker.py --warmup
"""

import os
import subprocess
import sys
import tempfile
from pathlib import Path

VENV_BIN = Path.home() / ".agent-deck" / "conductor" / ".venv" / "bin"
PARAKEET_CLI = str(VENV_BIN / "parakeet-mlx")


def transcribe(audio_path: str) -> str:
    """Transcribe audio file using parakeet-mlx CLI."""
    with tempfile.TemporaryDirectory(prefix="stt_") as tmp_dir:
        result = subprocess.run(
            [
                PARAKEET_CLI, audio_path,
                "--output-format", "txt",
                "--output-dir", tmp_dir,
            ],
            capture_output=True, text=True, timeout=60,
        )
        if result.returncode != 0:
            print(f"parakeet-mlx error: {result.stderr}", file=sys.stderr)
            sys.exit(1)

        # Read the output txt file
        txt_files = list(Path(tmp_dir).glob("*.txt"))
        if not txt_files:
            print("No transcription output file found", file=sys.stderr)
            sys.exit(1)
        return txt_files[0].read_text().strip()


def main():
    if len(sys.argv) < 2:
        print("Usage: stt_worker.py <audio_file> | --warmup", file=sys.stderr)
        sys.exit(1)

    if sys.argv[1] == "--warmup":
        print("Warming up parakeet-mlx...", file=sys.stderr)
        # Just check the CLI is accessible
        result = subprocess.run(
            [PARAKEET_CLI, "--help"],
            capture_output=True, text=True, timeout=10,
        )
        if result.returncode == 0:
            print("CLI accessible.", file=sys.stderr)
        print("")
        return

    audio_path = sys.argv[1]
    if not Path(audio_path).exists():
        print(f"File not found: {audio_path}", file=sys.stderr)
        sys.exit(1)

    text = transcribe(audio_path)
    print(text)


if __name__ == "__main__":
    main()
