#!/usr/bin/env python3
"""Validate a deck-repro report's evidence structure, never its scientific truth."""
import argparse
import hashlib
import json
import pathlib
import re


def require(condition, message):
    if not condition:
        raise ValueError(message)


def load_receipt(root, relative, expected):
    require(isinstance(relative, str) and relative, "missing receipt path")
    path = (root / relative).resolve()
    require(path.is_relative_to(root), "receipt escapes report directory")
    value = json.loads(path.read_text())
    require(value.get("exit_code") == expected and not value.get("timed_out", True), f"unexpected outcome: {relative}")
    require(value.get("command"), f"missing command: {relative}")
    require(not value.get("harness_error"), f"harness error: {relative}")
    log = (path.parent / "output.log").resolve()
    require(log.is_relative_to(root), "output escapes report directory")
    require(log.is_file() and log.stat().st_size > 0, f"missing output: {relative}")
    require(value.get("output_sha256") == hashlib.sha256(log.read_bytes()).hexdigest(), f"output hash mismatch: {relative}")
    require(value.get("oracle_matched") is True and value.get("oracle_evidence"), f"missing reviewed oracle evidence: {relative}")
    return value


def validate(path):
    root = path.parent.resolve()
    report = json.loads(path.read_text())
    require(report.get("schema_version") == 1, "unsupported schema")
    status = report.get("status")
    require(status in {"reproduced", "not reproduced", "blocked", "fix unverified", "fixed"}, "invalid status")
    require(report.get("candidate_id") and report.get("oracle"), "missing candidate or oracle")
    if status not in {"reproduced", "fixed", "fix unverified"}:
        attempts = report.get("attempts")
        require(isinstance(attempts, list) and attempts, "missing attempts and limitations")
        for attempt in attempts:
            require(isinstance(attempt, dict) and attempt.get("reason"), "missing attempt reason")
            if attempt.get("receipt"):
                receipt_path = (root / attempt["receipt"]).resolve()
                require(receipt_path.is_relative_to(root), "receipt escapes report directory")
                receipt = json.loads(receipt_path.read_text())
                require(receipt.get("command"), "missing attempted command")
                log = (receipt_path.parent / "output.log").resolve()
                require(log.is_relative_to(root) and log.is_file(), "missing attempted output")
                require(receipt.get("output_sha256") == hashlib.sha256(log.read_bytes()).hexdigest(), "attempt output hash mismatch")
        return status
    require(re.fullmatch(r"[0-9a-f]{40}", report.get("affected_revision", "")), "missing affected SHA")
    repro = report["reproduction"]
    old = load_receipt(root, repro.get("old_receipt"), 1)
    if status != "fixed":
        return status
    require(re.fullmatch(r"[0-9a-f]{40}", report.get("fixed_revision", "")), "missing fixed SHA")
    good = load_receipt(root, repro.get("fixed_receipt"), 0)
    require(isinstance(old.get("experiment"), dict) and old["experiment"].get("image_id") and old["experiment"].get("timeout_seconds"), "missing experiment image or budget")
    require(report["affected_revision"] != report["fixed_revision"], "fixed revision equals affected revision")
    for key in ("script_sha256", "fixtures", "experiment"):
        require(key in old and old[key] == good.get(key), f"reproduction drift: {key}")
    for receipt in (old, good):
        for key in ("binary_sha256", "script_sha256"):
            require(re.fullmatch(r"[0-9a-f]{64}", receipt.get(key, "")), f"invalid digest: {key}")
        require(isinstance(receipt.get("fixtures"), dict), "invalid fixture hashes")
        for digest in receipt["fixtures"].values():
            require(isinstance(digest, str) and re.fullmatch(r"[0-9a-f]{64}", digest), "invalid fixture digest")
    regression = report["regression"]
    require(regression.get("test"), "missing regression test name")
    red = load_receipt(root, regression.get("old_receipt"), 1)
    green = load_receipt(root, regression.get("fixed_receipt"), 0)
    for key in ("script_sha256", "fixtures", "experiment"):
        require(key in red and red[key] == green.get(key), f"regression drift: {key}")
    require(re.fullmatch(r"[0-9a-f]{64}", red.get("script_sha256", "")), "invalid regression script digest")
    require(isinstance(red.get("fixtures"), dict), "invalid regression fixture hashes")
    for digest in red["fixtures"].values():
        require(isinstance(digest, str) and re.fullmatch(r"[0-9a-f]{64}", digest), "invalid regression fixture digest")
    return status


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("report", type=pathlib.Path)
    parser.add_argument("--require-status", choices=["fixed", "reproduced"])
    args = parser.parse_args()
    try:
        status = validate(args.report.resolve())
        if args.require_status:
            require(status == args.require_status, f"required {args.require_status}, got {status}")
    except (ValueError, KeyError, TypeError, OSError) as error:
        parser.exit(1, f"INVALID: {error}\n")
    print(f"VALID evidence structure: {status}; independent oracle review still required")


if __name__ == "__main__":
    main()
