#!/usr/bin/env python3
"""Patch-bump the staged marketplace version when a published skill changes."""

import json
import re
import subprocess
import sys
from pathlib import Path


MANIFEST = ".claude-plugin/marketplace.json"
VERSION = re.compile(r"^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$")


def git(*args: str) -> str:
    result = subprocess.run(
        ["git", *args], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE
    )
    if result.returncode:
        raise RuntimeError(result.stderr.strip() or "git command failed")
    return result.stdout


def manifest_from(revision: str) -> tuple[dict, str]:
    raw = git("show", f"{revision}:{MANIFEST}") if revision == "HEAD" else git("show", f":{MANIFEST}")
    try:
        return json.loads(raw), raw
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"{MANIFEST} is not valid JSON: {exc.msg}") from exc


def version_parts(value: object) -> tuple[int, int, int]:
    if not isinstance(value, str) or not (match := VERSION.fullmatch(value)):
        raise RuntimeError(
            f"{MANIFEST} metadata.version must be a numeric major.minor.patch version"
        )
    return tuple(int(part) for part in match.groups())


def published_skill_dirs(manifest: dict) -> set[str]:
    paths: set[str] = set()
    for plugin in manifest.get("plugins", []):
        for skill in plugin.get("skills", []):
            if isinstance(skill, str):
                paths.add(skill.removeprefix("./").rstrip("/"))
    return paths


def main() -> int:
    try:
        staged_manifest, staged_raw = manifest_from(":")
        staged_version = version_parts(staged_manifest.get("metadata", {}).get("version"))
        changed_paths = set(git("diff", "--cached", "--name-only").splitlines())
        skills = published_skill_dirs(staged_manifest)
        if not any(path == skill or path.startswith(f"{skill}/") for path in changed_paths for skill in skills):
            return 0

        try:
            head_manifest, _ = manifest_from("HEAD")
        except RuntimeError as exc:
            if "invalid object name 'HEAD'" in str(exc):
                return 0
            raise
        head_version = version_parts(head_manifest.get("metadata", {}).get("version"))
        if staged_version > head_version:
            return 0
        if staged_version < head_version:
            raise RuntimeError(
                f"{MANIFEST} version {'.'.join(map(str, staged_version))} is lower than HEAD's "
                f"{'.'.join(map(str, head_version))}"
            )

        if Path(MANIFEST).read_text() != staged_raw:
            raise RuntimeError(
                f"{MANIFEST} has unstaged changes; stage or stash them before committing a skill update"
            )

        old = ".".join(map(str, staged_version))
        new = f"{staged_version[0]}.{staged_version[1]}.{staged_version[2] + 1}"
        updated, replacements = re.subn(
            rf'("version"\s*:\s*"){re.escape(old)}(")',
            rf"\g<1>{new}\g<2>",
            staged_raw,
            count=1,
        )
        if replacements != 1:
            raise RuntimeError(f"could not update {MANIFEST} metadata.version")
        Path(MANIFEST).write_text(updated)
        git("add", MANIFEST)
        print(f"Bumped marketplace plugin version {old} -> {new} for staged skill update.")
        return 0
    except RuntimeError as exc:
        print(f"plugin skill version hook: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
