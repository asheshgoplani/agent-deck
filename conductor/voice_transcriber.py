"""
Voice message transcription for the conductor bridge.

Downloads Telegram voice messages (.ogg) and transcribes them
using the OpenAI Whisper API. Falls back gracefully when the
API key is missing or the transcription fails.

Dependencies: pip3 install openai
"""

import logging
import tempfile
from pathlib import Path

log = logging.getLogger("conductor-bridge")

try:
    from openai import OpenAI

    HAS_OPENAI = True
except ImportError:
    HAS_OPENAI = False


def is_available(config: dict) -> bool:
    """Check whether voice transcription is configured and usable."""
    voice_cfg = config.get("voice", {})
    if not voice_cfg.get("enabled", False):
        return False
    if not HAS_OPENAI:
        log.warning("Voice enabled but 'openai' package not installed. pip3 install openai")
        return False
    if not voice_cfg.get("openai_api_key"):
        log.warning("Voice enabled but conductor.telegram.voice.openai_api_key not set")
        return False
    return True


async def transcribe_voice(bot, voice_file_id: str, config: dict) -> str | None:
    """Download a Telegram voice message and transcribe it via Whisper.

    Returns the transcribed text, or None on failure.
    """
    voice_cfg = config.get("voice", {})
    api_key = voice_cfg.get("openai_api_key", "")
    model = voice_cfg.get("model", "whisper-1")

    if not api_key:
        return None

    tmp_path: Path | None = None
    try:
        file = await bot.get_file(voice_file_id)
        tmp_path = Path(tempfile.mktemp(suffix=".ogg"))
        await bot.download_file(file.file_path, destination=str(tmp_path))

        log.info("Voice file downloaded (%d bytes), transcribing with %s", tmp_path.stat().st_size, model)

        client = OpenAI(api_key=api_key)
        with open(tmp_path, "rb") as audio_file:
            transcript = client.audio.transcriptions.create(
                model=model,
                file=audio_file,
            )

        text = transcript.text.strip()
        log.info("Transcription result (%d chars): %s", len(text), text[:100])
        return text if text else None

    except Exception as e:
        log.error("Voice transcription failed: %s", e)
        return None

    finally:
        if tmp_path and tmp_path.exists():
            tmp_path.unlink()
