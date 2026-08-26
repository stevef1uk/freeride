#!/usr/bin/env python3
"""
Parse a freeride proxy log and print LLM performance (attempts, completions, success rates)
over the last minute, hour, and day.
"""

from __future__ import annotations

import argparse
import re
import sys
from collections import Counter
from datetime import datetime, timedelta
from pathlib import Path

_ATTEMPT = re.compile(r"Attempting request with model: (.+?) \(via ")
_ATTEMPT_LOCAL = re.compile(r"\[LOCAL\] (?:role=(\w+) )?attempting (.+?) \(timeout ")
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

def parse_log(lines: list[str]) -> tuple[dict, dict, dict]:
    # First pass: find the last valid timestamp to use as "now"
    now = None
    for line in reversed(lines):
        try:
            if len(line) >= 19 and line[4] == '/' and line[16] == ':':
                now = datetime.strptime(line[:19], "%Y/%m/%d %H:%M:%S")
                break
        except ValueError:
            continue
            
    if not now:
        now = datetime.now()
        
    thresholds = {
        "Last Minute": now - timedelta(minutes=1),
        "Last Hour": now - timedelta(hours=1),
        "Last Day (24h)": now - timedelta(days=1),
        "All Time": datetime.min
    }
    
    attempts = {k: Counter() for k in thresholds}
    completions = {k: Counter() for k in thresholds}
    
    for line in lines:
        try:
            if len(line) >= 19 and line[4] == '/' and line[16] == ':':
                dt = datetime.strptime(line[:19], "%Y/%m/%d %H:%M:%S")
            else:
                continue
        except ValueError:
            continue
            
        m_att = _ATTEMPT.search(line)
        m_att_local = _ATTEMPT_LOCAL.search(line)
        m_succ = _SUCCESS.search(line)
        
        model = None
        is_attempt = False
        is_success = False
        
        if m_att:
            model = m_att.group(1)
            is_attempt = True
        elif m_att_local:
            model = m_att_local.group(2)
            is_attempt = True
        elif m_succ:
            model = m_succ.group(1)
            is_success = True
            
        if model:
            for interval, threshold in thresholds.items():
                if dt >= threshold:
                    if is_attempt:
                        attempts[interval][model] += 1
                    if is_success:
                        completions[interval][model] += 1

    return attempts, completions, thresholds.keys()

def print_table(interval_name: str, attempts: Counter, completions: Counter) -> None:
    models = sorted(
        set(attempts) | set(completions),
        key=lambda k: (-completions[k], -attempts[k], k),
    )

    if not models:
        print(f"=== {interval_name} ===")
        print("No activity recorded.")
        print()
        return

    print(f"=== {interval_name} ===")
    
    w_model = max(len("model"), *(len(m) for m in models))
    w_comp = len("completions")
    w_att = len("attempts")
    w_rate = len("success %")
    
    for m in models:
        w_comp = max(w_comp, len(str(completions[m])))
        w_att = max(w_att, len(str(attempts[m])))

    sep = f"+-{'-' * w_model}-+-{'-' * w_comp}-+-{'-' * w_att}-+-{'-' * w_rate}-+"
    row = "| {:<{}} | {:>{}} | {:>{}} | {:>{}} |"

    print(sep)
    print(row.format("model", w_model, "completions", w_comp, "attempts", w_att, "success %", w_rate))
    print(sep)
    
    tc, ta = 0, 0
    for m in models:
        c, a = completions[m], attempts[m]
        tc += c
        ta += a
        rate = f"{(c / a * 100):.1f}%" if a > 0 else "0.0%"
        print(row.format(m, w_model, c, w_comp, a, w_att, rate, w_rate))
    print(sep)
    
    trate = f"{(tc / ta * 100):.1f}%" if ta > 0 else "0.0%"
    print(row.format("TOTAL", w_model, tc, w_comp, ta, w_att, trate, w_rate))
    print(sep)
    print()

# --- Throughput section ---

_THROUGHPUT = re.compile(
    r"\[llm\] response received: status=(\d+) "
    r"duration=((?:\d+m)?[\d.]+)s bytes=(\d+) model=(\S+)"
)

def _parse_duration(s: str) -> float:
    """Parse '4.094s' or '2m23.467s' into seconds."""
    if "m" in s:
        mins, _, secs = s.partition("m")
        return float(mins) * 60 + float(secs.rstrip("s"))
    return float(s.rstrip("s"))

def parse_throughput(lines: list[str]) -> dict[str, dict]:
    """Parse agent session log lines with duration+bytes to estimate tok/s."""
    results = {}  # model -> {count, total_tokens, total_seconds}
    for line in lines:
        m = _THROUGHPUT.search(line)
        if not m or m.group(1) != "200":
            continue
        dur_s = _parse_duration(m.group(2))
        nbytes = int(m.group(3))
        model = m.group(4)
        # Estimate tokens: ~3.5 bytes per token for JSON/English text
        est_tokens = max(1, int(nbytes / 3.5))
        tps = est_tokens / dur_s if dur_s > 0 else 0
        if model not in results:
            results[model] = {"count": 0, "total_tokens": 0, "total_seconds": 0.0, "tps_samples": []}
        r = results[model]
        r["count"] += 1
        r["total_tokens"] += est_tokens
        r["total_seconds"] += dur_s
        r["tps_samples"].append(tps)
    return results

def print_throughput(results: dict) -> None:
    if not results:
        return
    print("=== Average Tokens/Second by Model ===")
    models = sorted(results, key=lambda k: -results[k]["total_seconds"])
    w_model = max(len("model"), *(len(m) for m in models))
    w_count = len("requests")
    w_tps = len("avg tok/s")
    w_time = len("total time")
    sep = f"+-{'-' * w_model}-+-{'-' * w_count}-+-{'-' * w_tps}-+-{'-' * w_time}-+"
    row = "| {:<{}} | {:>{}} | {:>{}} | {:>{}} |"
    print(sep)
    print(row.format("model", w_model, "requests", w_count, "avg tok/s", w_tps, "total time", w_time))
    print(sep)
    for m in models:
        r = results[m]
        avg_tps = r["total_tokens"] / r["total_seconds"] if r["total_seconds"] > 0 else 0
        print(row.format(m[:w_model], w_model, r["count"], w_count,
                         f"{avg_tps:.1f}", w_tps, f"{r['total_seconds']:.0f}s", w_time))
    print(sep)
    print()


def main() -> None:
    ap = argparse.ArgumentParser(
        description="Print freeride proxy upstream model performance over time."
    )
    ap.add_argument(
        "logfile",
        nargs="?",
        default="freeride_live.log",
        help="Path to log file (default: freeride_live.log in cwd). Use '-' for stdin.",
    )
    ap.add_argument(
        "--sessions",
        default=None,
        help="Glob pattern for gt-agent session logs (e.g. '/home/stevef/gt/logs/sessions/*.log') "
             "to report estimated tokens/second per model.",
    )
    args = ap.parse_args()

    lines = iter_lines(args.logfile)
    attempts, completions, intervals = parse_log(lines)
    
    for interval in reversed(list(intervals)):
        print_table(interval, attempts[interval], completions[interval])

    if args.sessions:
        import glob as globmod
        session_lines = []
        for f in sorted(globmod.glob(args.sessions)):
            session_lines.extend(iter_lines(f))
        tp_results = parse_throughput(session_lines)
        print_throughput(tp_results)

if __name__ == "__main__":
    main()
