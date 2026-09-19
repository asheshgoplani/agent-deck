#!/usr/bin/env python3
"""Shared config loader for the gh-watcher scripts.

Resolution order for the config file:
  1. $AD_GH_WATCHER_CONFIG
  2. $AD_GH_WATCHER_HOME/watcher.toml
  3. <this file>/../watcher.toml

Run as a script with --shell to print KEY=value lines for `eval` in bash.
"""
from __future__ import annotations

import os
import shlex
import sys
from pathlib import Path

try:  # Python 3.11+
    import tomllib as _toml

    def _load(path: Path) -> dict:
        with path.open("rb") as fh:
            return _toml.load(fh)
except ModuleNotFoundError:  # pragma: no cover
    import toml as _toml  # type: ignore

    def _load(path: Path) -> dict:
        return _toml.load(str(path))


def config_path() -> Path:
    if os.environ.get("AD_GH_WATCHER_CONFIG"):
        return Path(os.environ["AD_GH_WATCHER_CONFIG"]).expanduser()
    home = os.environ.get("AD_GH_WATCHER_HOME")
    if home:
        return Path(home).expanduser() / "watcher.toml"
    return Path(__file__).resolve().parent.parent / "watcher.toml"


class Config:
    def __init__(self, path: Path | None = None) -> None:
        self.path = path or config_path()
        if not self.path.is_file():
            raise SystemExit(f"gh-watcher: config not found: {self.path}")
        raw = _load(self.path)
        self.home = self.path.parent
        repo = raw.get("repo", {})
        self.owner = repo.get("owner", "")
        self.name = repo.get("name", "")
        if not self.owner or not self.name:
            raise SystemExit("gh-watcher: [repo] owner and name are required")
        routing = raw.get("routing", {})
        self.profile = routing.get("profile", "default")
        self.conductor = routing.get("conductor", f"conductor-{self.profile}")
        watcher = raw.get("watcher", {})
        self.mode = watcher.get("mode", "log")
        if self.mode not in ("log", "dispatch"):
            raise SystemExit(f"gh-watcher: mode must be log or dispatch, got {self.mode!r}")
        self.db = self._resolve(watcher.get("db_path", "watcher.db"))
        self.log = self._resolve(watcher.get("log_path", "task-log.md"))
        self.liveness = self._resolve(watcher.get("liveness_path", "last-poll"))
        cadence = raw.get("cadence", {})
        self.events_seconds = int(cadence.get("events_seconds", 30))
        self.stale_after_seconds = int(cadence.get("stale_after_seconds", 3 * self.events_seconds))
        disp = raw.get("dispatcher", {})
        self.min_interval_seconds = int(disp.get("min_interval_seconds", 30))
        self.high_water = int(disp.get("high_water", 25))
        self.digest_flush_local_time = str(disp.get("digest_flush_local_time", "18:00"))

    @property
    def repo(self) -> str:
        return f"{self.owner}/{self.name}"

    def _resolve(self, value: str) -> Path:
        p = Path(os.path.expanduser(os.path.expandvars(value)))
        return p if p.is_absolute() else self.home / p

    def shell_exports(self) -> str:
        items = {
            "GHW_HOME": str(self.home),
            "GHW_REPO": self.repo,
            "GHW_PROFILE": self.profile,
            "GHW_CONDUCTOR": self.conductor,
            "GHW_MODE": self.mode,
            "GHW_DB": str(self.db),
            "GHW_LOG": str(self.log),
            "GHW_LIVENESS": str(self.liveness),
            "GHW_EVENTS_SECONDS": str(self.events_seconds),
            "GHW_STALE_AFTER": str(self.stale_after_seconds),
            "GHW_MIN_INTERVAL": str(self.min_interval_seconds),
            "GHW_HIGH_WATER": str(self.high_water),
            "GHW_DIGEST_FLUSH_TIME": self.digest_flush_local_time,
        }
        return "\n".join(f"{k}={shlex.quote(v)}" for k, v in items.items())


if __name__ == "__main__":
    cfg = Config()
    if "--shell" in sys.argv:
        print(cfg.shell_exports())
    else:
        print(cfg.path)
