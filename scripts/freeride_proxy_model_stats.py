#!/usr/bin/env python3
"""
Parse a freeride proxy log (e.g. from `./freeride --debug > freeride_live.log 2>&1`)
and print a table of upstream model usage.

Counts:
  completions — cloud "Model <id> succeeded ..." or local "[LOCAL] <id> succeeded ..."
  attempts    — "Attempting request with model: <id>" or "[LOCAL] ... attempting <id>"
"""

from __future__ import annotations

import argparse
import re
import sys
import time
from collections import Counter
from pathlib import Path

# Go log package prefixes each line with "YYYY/MM/DD hh:mm:ss ".
_ATTEMPT = re.compile(r"Attempting request with model: (.+?) \(via ")
_ATTEMPT_LOCAL = re.compile(r"\[LOCAL\] (?:role=(\w+) )?attempting (.+?) \(timeout ")
# Trailing text varies: ". Returning ..." (cloud) or " in 52s — returning ..." ([LOCAL])
_SUCCESS = re.compile(
    r"(?:Model|\[LOCAL\]) (.+?) succeeded \(status \d+\)(?: for role=(\w+))?"
)


def iter_lines(path: str | None) -> list[str]:
    if path is None or path == "-":
        return sys.stdin.read().splitlines()
    p = Path(path)
    if not p.is_file():
        print(f"error: not a file: {p}", file=sys.stderr)
        sys.exit(1)
    return p.read_text(errors="replace").splitlines()


def parse_log(lines: list[str]) -> tuple[Counter, Counter, Counter]:
    attempts = Counter()
    completions = Counter()
    completions_by_role = Counter()

    for line in lines:
        m = _ATTEMPT.search(line)
        if m:
            attempts[m.group(1)] += 1
            continue
        m = _ATTEMPT_LOCAL.search(line)
        if m:
            attempts[m.group(2)] += 1
            continue
        m = _SUCCESS.search(line)
        if m:
            model, role = m.group(1), m.group(2)
            completions[model] += 1
            if role:
                completions_by_role[f"{model} (role={role})"] += 1
    return attempts, completions, completions_by_role


def print_table(
    attempts: Counter,
    completions: Counter,
    completions_by_role: Counter,
    *,
    show_roles: bool,
) -> None:
    models = sorted(
        set(attempts) | set(completions),
        key=lambda k: (-completions[k], -attempts[k], k),
    )

    if not models:
        print("No matching log lines found.")
        print("Expected freeride debug lines such as:")
        print('  Attempting request with model: <id> (via ...)')
        print('  [LOCAL] role=polecat attempting <id> (timeout ...) via ...')
        print('  Model <id> succeeded (status 200) for role=polecat. Returning ...')
        print('  [LOCAL] <id> succeeded (status 200) for role=polecat in 52s — returning ...')
        return

    w_model = max(len("model"), *(len(m) for m in models))
    w_comp = len("completions")
    w_att = len("attempts")
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

    if show_roles and completions_by_role:
        print()
        print("Completions by role:")
        for key in sorted(completions_by_role, key=lambda k: (-completions_by_role[k], k)):
            print(f"  {completions_by_role[key]:4d}  {key}")


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
    ap.add_argument(
        "--roles",
        action="store_true",
        help="Also print completion counts grouped by Gas Town role (when logged).",
    )
    ap.add_argument(
        "--watch",
        type=float,
        metavar="SECONDS",
        help="Re-read the log every SECONDS and refresh the table (Ctrl-C to stop).",
    )
    args = ap.parse_args()

    def run_once() -> bool:
        lines = iter_lines(args.logfile)
        attempts, completions, by_role = parse_log(lines)
        if args.watch:
            print(f"\n--- {time.strftime('%H:%M:%S')} ({args.logfile}) ---")
        print_table(attempts, completions, by_role, show_roles=args.roles)
        return bool(attempts or completions)

    if args.watch:
        try:
            while True:
                run_once()
                time.sleep(args.watch)
        except KeyboardInterrupt:
            print("\n(stopped)")
    else:
        if not run_once():
            sys.exit(0)


if __name__ == "__main__":
    main()
