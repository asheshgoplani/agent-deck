#!/usr/bin/env python3
"""
STT worker: transcribes an audio file using parakeet-mlx.
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

MODEL_ID = "mlx-community/parakeet-tdt-0.6b-v3"
# Local cache (downloaded via curl to avoid Python 3.14 SSL issues)
MODEL_LOCAL = Path.home() / ".cache" / "parakeet-mlx" / "mlx-community--parakeet-tdt-0.6b-v3"


def normalize_audio(input_path: str) -> str:
    """Convert audio to mono 16kHz WAV using ffmpeg."""
    fd, wav_path = tempfile.mkstemp(suffix=".wav", prefix="stt_")
    os.close(fd)
    result = subprocess.run(
        [
            "ffmpeg", "-y", "-i", input_path,
            "-ac", "1", "-ar", "16000",
            "-f", "wav", wav_path,
        ],
        capture_output=True, text=True, timeout=30,
    )
    if result.returncode != 0:
        print(f"ffmpeg error: {result.stderr}", file=sys.stderr)
        sys.exit(1)
    return wav_path


def get_model_path() -> str:
    """Return model path: prefer local cache, fall back to HuggingFace ID."""
    if MODEL_LOCAL.exists() and (MODEL_LOCAL / "config.json").exists():
        return str(MODEL_LOCAL)
    return MODEL_ID


def transcribe(audio_path: str) -> str:
    """Transcribe audio file using parakeet-mlx."""
    import parakeet_mlx as pmx

    wav_path = normalize_audio(audio_path)
    try:
        model = pmx.from_pretrained(get_model_path())
        result = pmx.transcribe(model, wav_path)
        return result["text"]
    finally:
        Path(wav_path).unlink(missing_ok=True)


def main():
    if len(sys.argv) < 2:
        print("Usage: stt_worker.py <audio_file> | --warmup", file=sys.stderr)
        sys.exit(1)

    if sys.argv[1] == "--warmup":
        # Load model to verify weights are cached and working
        print("Warming up parakeet-mlx model...", file=sys.stderr)
        import parakeet_mlx as pmx
        pmx.from_pretrained(get_model_path())
        print("Model cached.", file=sys.stderr)
        print("")  # empty transcript on stdout
        return

    audio_path = sys.argv[1]
    if not Path(audio_path).exists():
        print(f"File not found: {audio_path}", file=sys.stderr)
        sys.exit(1)

    text = transcribe(audio_path)
    print(text)


if __name__ == "__main__":
    main()
