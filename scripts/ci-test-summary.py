#!/usr/bin/env python3
"""Summarise gotestsum JSON output as a GitHub job summary.

Reads the go test -json stream that gotestsum wrote via --jsonfile, finds
every test whose final outcome is a failure, labels it "known flake" when it
appears in tests/known-flaky.txt, and appends a Markdown table with an exact
reproduction command to $GITHUB_STEP_SUMMARY (stdout when unset).

This script never changes the job's pass/fail result: it always exits 0.
Standard library only.

Usage:
    ci-test-summary.py [--json test-output.json] [--known-flaky tests/known-flaky.txt]
                       [--go-mod go.mod]
"""

import argparse
import json
import os
import re
import sys

FINAL_ACTIONS = ("pass", "fail", "skip")


def read_module_path(go_mod):
    try:
        with open(go_mod, encoding="utf-8") as fh:
            for line in fh:
                parts = line.split()
                if len(parts) == 2 and parts[0] == "module":
                    return parts[1]
    except OSError:
        pass
    return ""


def relative_package(pkg, module):
    if module and pkg == module:
        return "."
    if module and pkg.startswith(module + "/"):
        return "./" + pkg[len(module) + 1:]
    return pkg


def load_known_flaky(path):
    """Return a set of ("<pkg>" or "", "<TopLevelTest>") tuples."""
    entries = set()
    try:
        with open(path, encoding="utf-8") as fh:
            for line in fh:
                line = line.split("#", 1)[0].strip()
                if not line:
                    continue
                parts = line.split()
                if len(parts) == 1:
                    entries.add(("", parts[0]))
                else:
                    pkg = parts[0]
                    if not pkg.startswith("./") and pkg != ".":
                        pkg = "./" + pkg
                    entries.add((pkg, parts[1]))
    except OSError:
        pass
    return entries


def is_known_flaky(entries, rel_pkg, test):
    top = test.split("/", 1)[0]
    return ("", top) in entries or (rel_pkg, top) in entries


def run_regex(test):
    return "/".join("^" + re.escape(seg) + "$" for seg in test.split("/"))


def repro_line(rel_pkg, test):
    if test:
        return "go test %s -run '%s' -race -count=1" % (rel_pkg, run_regex(test))
    return "go test %s -race -count=1" % rel_pkg


def collect(events):
    """Map (package, test) -> final action, plus the set that ever failed."""
    final = {}
    ever_failed = set()
    for ev in events:
        action = ev.get("Action")
        if action not in FINAL_ACTIONS:
            continue
        key = (ev.get("Package", ""), ev.get("Test", ""))
        final[key] = action
        if action == "fail":
            ever_failed.add(key)
    return final, ever_failed


def read_events(path):
    events = []
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                events.append(json.loads(line))
            except ValueError:
                continue
    return events


def render(final, ever_failed, module, known):
    failed = sorted(k for k, a in final.items() if a == "fail")
    # A package-level failure with no named failing test (build error, panic
    # outside a test, timeout) still needs a row; otherwise the package row is
    # redundant with its tests.
    named_failures = {pkg for pkg, test in failed if test}
    rows = [(pkg, test) for pkg, test in failed if test or pkg not in named_failures]
    recovered = sorted(k for k in ever_failed if final.get(k) == "pass" and k[1])

    out = ["## Go test failures", ""]
    if not rows:
        out.append("No failing tests in the final gotestsum result.")
    else:
        new = 0
        out.append("| Package | Test | Status | Reproduce |")
        out.append("|---|---|---|---|")
        for pkg, test in rows:
            rel = relative_package(pkg, module)
            flaky = bool(test) and is_known_flaky(known, rel, test)
            status = "known flake" if flaky else "new failure"
            new += 0 if flaky else 1
            out.append("| `%s` | `%s` | %s | `%s` |" % (
                rel, test or "(package)", status, repro_line(rel, test)))
        out.append("")
        out.append("%d failing, %d not in `tests/known-flaky.txt`." % (len(rows), new))
    if recovered:
        out.append("")
        out.append("Failed at least once but passed on rerun (flake candidates):")
        out.append("")
        for pkg, test in recovered:
            out.append("- `%s` `%s`" % (relative_package(pkg, module), test))
    out.append("")
    return "\n".join(out)


def write_summary(text):
    path = os.environ.get("GITHUB_STEP_SUMMARY")
    if path:
        with open(path, "a", encoding="utf-8") as fh:
            fh.write(text)
    else:
        sys.stdout.write(text)


def main(argv):
    ap = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    ap.add_argument("--json", default="test-output.json")
    ap.add_argument("--known-flaky", default="tests/known-flaky.txt")
    ap.add_argument("--go-mod", default="go.mod")
    args = ap.parse_args(argv)

    if not os.path.isfile(args.json):
        write_summary("## Go test failures\n\nNo gotestsum JSON at `%s`; "
                      "the test step did not produce output.\n" % args.json)
        return 0

    events = read_events(args.json)
    final, ever_failed = collect(events)
    text = render(final, ever_failed, read_module_path(args.go_mod),
                  load_known_flaky(args.known_flaky))
    write_summary(text)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
