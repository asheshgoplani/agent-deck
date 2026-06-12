"""Tests for XDG-aware path resolution in bridge.py (issue #1350).

bridge.py used to hardcode ~/.agent-deck for CONFIG_PATH / CONDUCTOR_DIR. On a
fresh XDG install the Go side writes the bridge + conductors under
$XDG_DATA_HOME/agent-deck and the config under $XDG_CONFIG_HOME/agent-deck, so
the hardcoded paths made load_config() exit and discover_conductors() find
nothing -> conductor message routing died.

The resolvers must mirror internal/agentpaths exactly:
  - per-marker existence check: use XDG dir if the marker exists there,
    else legacy ~/.agent-deck if the marker exists there, else default XDG.
  - honor $XDG_DATA_HOME / $XDG_CONFIG_HOME (absolute paths only),
    defaulting to ~/.local/share and ~/.config.

Because the path constants resolve at import time, each scenario runs bridge.py
in a fresh subprocess with a fully sandboxed HOME + XDG env.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
from pathlib import Path

import pytest

BRIDGE_DIR = Path(__file__).parent.parent


def _run_probe(env_overrides: dict[str, str]) -> dict:
    """Import bridge.py under a sandboxed env and dump its resolved paths.

    Returns a dict with conductor_dir, config_path, and the discovered
    conductor names. Runs in a subprocess so import-time resolution sees the
    sandboxed env and does not pollute the in-process module cache.
    """
    probe = (
        "import json, sys\n"
        f"sys.path.insert(0, {str(BRIDGE_DIR)!r})\n"
        "import bridge\n"
        "out = {\n"
        "    'conductor_dir': str(bridge.CONDUCTOR_DIR),\n"
        "    'config_path': str(bridge.CONFIG_PATH),\n"
        "    'conductors': bridge.get_conductor_names(),\n"
        "}\n"
        "print('PROBE_JSON:' + json.dumps(out))\n"
    )
    env = dict(os.environ)
    # Clear inherited XDG so the sandbox is deterministic.
    for key in ("XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME"):
        env.pop(key, None)
    env.update(env_overrides)
    result = subprocess.run(
        [sys.executable, "-c", probe],
        capture_output=True,
        text=True,
        env=env,
    )
    if result.returncode != 0:
        raise AssertionError(
            f"probe failed (rc={result.returncode}):\n"
            f"STDOUT:\n{result.stdout}\nSTDERR:\n{result.stderr}"
        )
    for line in result.stdout.splitlines():
        if line.startswith("PROBE_JSON:"):
            return json.loads(line[len("PROBE_JSON:"):])
    raise AssertionError(f"probe produced no PROBE_JSON line:\n{result.stdout}")


def _write_conductor(conductor_dir: Path, name: str) -> None:
    cdir = conductor_dir / name
    cdir.mkdir(parents=True, exist_ok=True)
    (cdir / "meta.json").write_text(json.dumps({"name": name, "profile": "personal"}))


def test_resolves_xdg_when_xdg_populated(tmp_path: Path) -> None:
    """XDG install: marker exists under XDG, legacy empty -> resolve XDG."""
    home = tmp_path / "home"
    xdg_data = tmp_path / "xdgdata"
    xdg_config = tmp_path / "xdgconfig"
    home.mkdir()

    xdg_conductor = xdg_data / "agent-deck" / "conductor"
    _write_conductor(xdg_conductor, "c1")
    xdg_cfg_dir = xdg_config / "agent-deck"
    xdg_cfg_dir.mkdir(parents=True)
    (xdg_cfg_dir / "config.toml").write_text("[telegram]\n")

    # Legacy dir exists but is empty (no conductor/, no config.toml).
    (home / ".agent-deck").mkdir()

    out = _run_probe(
        {
            "HOME": str(home),
            "XDG_DATA_HOME": str(xdg_data),
            "XDG_CONFIG_HOME": str(xdg_config),
        }
    )

    assert out["conductor_dir"] == str(xdg_conductor), out
    assert out["config_path"] == str(xdg_cfg_dir / "config.toml"), out
    assert out["conductors"] == ["c1"], out


def test_resolves_legacy_when_only_legacy_populated(tmp_path: Path) -> None:
    """Migration fallback: only legacy ~/.agent-deck populated -> resolve legacy."""
    home = tmp_path / "home"
    xdg_data = tmp_path / "xdgdata"
    xdg_config = tmp_path / "xdgconfig"
    home.mkdir()

    legacy = home / ".agent-deck"
    legacy_conductor = legacy / "conductor"
    _write_conductor(legacy_conductor, "legacyc")
    (legacy / "config.toml").write_text("[telegram]\n")

    # XDG dirs exist but have no agent-deck markers.
    xdg_data.mkdir()
    xdg_config.mkdir()

    out = _run_probe(
        {
            "HOME": str(home),
            "XDG_DATA_HOME": str(xdg_data),
            "XDG_CONFIG_HOME": str(xdg_config),
        }
    )

    assert out["conductor_dir"] == str(legacy_conductor), out
    assert out["config_path"] == str(legacy / "config.toml"), out
    assert out["conductors"] == ["legacyc"], out


def test_defaults_to_xdg_when_nothing_populated(tmp_path: Path) -> None:
    """Fresh machine, no markers anywhere -> default to XDG location."""
    home = tmp_path / "home"
    xdg_data = tmp_path / "xdgdata"
    xdg_config = tmp_path / "xdgconfig"
    home.mkdir()

    out = _run_probe(
        {
            "HOME": str(home),
            "XDG_DATA_HOME": str(xdg_data),
            "XDG_CONFIG_HOME": str(xdg_config),
        }
    )

    assert out["conductor_dir"] == str(xdg_data / "agent-deck" / "conductor"), out
    assert out["config_path"] == str(xdg_config / "agent-deck" / "config.toml"), out
    assert out["conductors"] == [], out


def test_unset_xdg_defaults_to_local_share_and_config(tmp_path: Path) -> None:
    """Unset XDG_* -> default to ~/.local/share and ~/.config per spec."""
    home = tmp_path / "home"
    home.mkdir()

    xdg_conductor = home / ".local" / "share" / "agent-deck" / "conductor"
    _write_conductor(xdg_conductor, "defc")
    cfg_dir = home / ".config" / "agent-deck"
    cfg_dir.mkdir(parents=True)
    (cfg_dir / "config.toml").write_text("[telegram]\n")

    out = _run_probe({"HOME": str(home)})

    assert out["conductor_dir"] == str(xdg_conductor), out
    assert out["config_path"] == str(cfg_dir / "config.toml"), out
    assert out["conductors"] == ["defc"], out
