#!/usr/bin/env python3
"""
Parse a freeride proxy log (e.g. from `./freeride --debug > freeride_live.log 2>&1`)
and print a table of upstream model usage.

Counts:
  completions — lines matching "Model <id> succeeded (status ...)"
  attempts    — lines matching "Attempting request with model: <id> (via ...)"
"""

from __future__ import annotations

import argparse
import re
import sys
from collections import Counter
from pathlib import Path

# Go log package prefixes each line with "YYYY/MM/DD hh:mm:ss ".
_ATTEMPT = re.compile(r"Attempting request with model: (.+) \(via ")
_SUCCESS = re.compile(r"Model (.+) succeeded \(status \d+\)\. Returning response to client\.")


def iter_lines(path: str | None) -> list[str]:
    if path is None or path == "-":
        return sys.stdin.read().splitlines()
    p = Path(path)
    if not p.is_file():
        print(f"error: not a file: {p}", file=sys.stderr)
        sys.exit(1)
    return p.read_text(errors="replace").splitlines()


def main() -> None:
    ap = argparse.ArgumentParser(
        description="Count freeride proxy upstream model calls from a debug log."
    )
    ap.add_argument(
        "logfile",
        nargs="?",
        default="freeride_live.log",
        help="Path to log file (default: freeride_live.log in cwd). Use '-' for stdin.",
    )
    args = ap.parse_args()

    lines = iter_lines(args.logfile)
    attempts = Counter()
    completions = Counter()

    for line in lines:
        m = _ATTEMPT.search(line)
        if m:
            attempts[m.group(1)] += 1
            continue
        m = _SUCCESS.search(line)
        if m:
            completions[m.group(1)] += 1

    models = sorted(
        set(attempts) | set(completions),
        key=lambda k: (-completions[k], -attempts[k], k),
    )

    if not models:
        print("No matching log lines found (need Attempting / succeeded messages from freeride).")
        sys.exit(0)

    w_model = max(len("model"), *(len(m) for m in models))
    w_comp = len("completions")
    w_att = len("attempts")
    # widen numeric columns if needed
    for m in models:
        w_comp = max(w_comp, len(str(completions[m])))
        w_att = max(w_att, len(str(attempts[m])))

    sep = f"+-{'-' * w_model}-+-{'-' * w_comp}-+-{'-' * w_att}-+"
    row = "| {:<{}} | {:>{}} | {:>{}} |"

    print(sep)
    print(row.format("model", w_model, "completions", w_comp, "attempts", w_att))
    print(sep)
    tc, ta = 0, 0
    for m in models:
        c, a = completions[m], attempts[m]
        tc += c
        ta += a
        print(row.format(m, w_model, c, w_comp, a, w_att))
    print(sep)
    print(row.format("TOTAL", w_model, tc, w_comp, ta, w_att))
    print(sep)


if __name__ == "__main__":
    main()
