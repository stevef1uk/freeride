#!/usr/bin/env python3
"""Review OpenRouter QA candidates; preview and confirm a models.yaml update.

Uses only Python's standard library. Catalog access is public. --probe makes small
billable completion requests using OPENROUTER_API_KEY (environment or repo .env).
"""

from __future__ import annotations

import argparse
import datetime as dt
import difflib
import json
import os
from pathlib import Path
import re
import shutil
import sys
import tempfile
import urllib.error
import urllib.request

CATALOG_URL = "https://openrouter.ai/api/v1/models"
CHAT_URL = "https://openrouter.ai/api/v1/chat/completions"
ROOT = Path(__file__).resolve().parent.parent
WEAK_MODEL = re.compile(
    r"nano|mini(?!max)|flash-lite|lightning|safety|guard|embed|audio|image|:batch|"
    r"(?:^|[-/])(?:[1-9]|1[0-9]|2[0-9])b(?:[-/:]|$)", re.I
)
# These namespaces are routed to NIM by Freeride, not OpenRouter, except :free.
NIM_PREFIXES = ("nvidia/", "meta/", "mistralai/", "microsoft/", "qwen/", "abacusai/", "ai21labs/", "01-ai/")
PROBE_PROMPT = """You are QA. Review this code against its requirement.
Requirement: has_duplicates(values) returns True when duplicate integers exist,
and False when all integers are unique.
Implementation:
def has_duplicates(values):
    return len(set(values)) == len(values)

Return only a JSON object with keys: verdict (PASS or FAIL), explanation (one
sentence), actual_for_1_1 (boolean result for [1, 1]), expected_for_1_1 (boolean).
"""


def fetch_json(url, *, payload=None, key=None, timeout=30):
    headers = {"Accept": "application/json", "User-Agent": "freeride-qa-model-review"}
    data = None
    if payload is not None:
        data = json.dumps(payload).encode()
        headers["Content-Type"] = "application/json"
    if key:
        headers["Authorization"] = "Bearer " + key
    request = urllib.request.Request(url, data=data, headers=headers)
    with urllib.request.urlopen(request, timeout=timeout) as response:
        return json.load(response)


def block_span(lines, header, indent):
    """Locate an existing block-style YAML section without rewriting the file."""
    marker = " " * indent + header + ":"
    start = next((i for i, line in enumerate(lines) if line.rstrip() == marker), None)
    if start is None:
        raise ValueError(f"Expected a block-style {header}: section in models.yaml")
    end = start + 1
    while end < len(lines):
        stripped = lines[end].strip()
        if stripped and not stripped.startswith("#"):
            depth = len(lines[end]) - len(lines[end].lstrip())
            if depth <= indent:
                break
        end += 1
    # Leave separators/comments before the next section in place.
    while end > start + 1 and (not lines[end - 1].strip() or lines[end - 1].lstrip().startswith("#")):
        end -= 1
    return start, end


def list_values(lines):
    values = []
    for line in lines:
        match = re.match(r"\s*-\s*(?:\"([^\"]+)\"|'([^']+)'|([^#\s]+))\s*(?:#.*)?$", line)
        if match:
            values.append(next(value for value in match.groups() if value is not None))
        elif line.strip() and not line.lstrip().startswith("#"):
            raise ValueError(f"Unsupported YAML list entry: {line.strip()}")
    return values


def read_policy(text):
    lines = text.splitlines(keepends=True)
    start, end = block_span(lines, "rolePrepend", 0)
    role_lines = lines[start + 1:end]
    roles = {}
    for i, line in enumerate(role_lines):
        match = re.match(r"^  ([\w-]+):\s*(?:#.*)?$", line.rstrip())
        if match:
            _, role_end = block_span(role_lines, match[1], 2)
            roles[match[1]] = list_values(role_lines[i + 1:role_end])
    if not roles.get("qa"):
        raise ValueError("rolePrepend.qa must be a nonempty block-style list")
    try:
        excluded_start, excluded_end = block_span(lines, "excludeModels", 0)
        excluded = set(list_values(lines[excluded_start + 1:excluded_end]))
    except ValueError as error:
        if any(line.startswith("excludeModels:") for line in lines):
            raise error
        excluded = set()
    return roles, excluded


def shortlist(catalog, roles, excluded, args):
    primaries = {values[0] for role, values in roles.items() if role != "qa" and values}
    result = []
    for model in catalog:
        model_id = model["id"]
        if model_id in excluded or model_id in primaries or WEAK_MODEL.search(model_id):
            continue
        if model_id.startswith(NIM_PREFIXES) and not model_id.endswith(":free"):
            continue
        if "tools" not in model.get("supported_parameters", []):
            continue
        architecture = model.get("architecture", {})
        if architecture.get("output_modalities", ["text"]) != ["text"]:
            continue
        # Catalog claims are a shortlist signal, not proof of review quality.
        description = model.get("description", "").lower()
        if model_id not in roles["qa"] and not any(word in description for word in ("coding", "code review", "agent", "software")):
            continue
        pricing = model.get("pricing", {})
        try:
            input_cost = float(pricing["prompt"]) * 1e6
            output_cost = float(pricing["completion"]) * 1e6
            context = int(model.get("context_length", 0))
        except (KeyError, TypeError, ValueError):
            continue
        if not (0 <= input_cost <= args.max_input and 0 <= output_cost <= args.max_output and context >= args.min_context):
            continue
        if not getattr(args, "include_free", False) and input_cost == 0 and output_cost == 0:
            continue
        result.append(dict(model, input_cost=input_cost, output_cost=output_cost,
                           review_cost=input_cost * 0.05 + output_cost * 0.002))
    return sorted(result, key=lambda m: (m["review_cost"], m["id"]))[:args.limit]


def probe_model(model, key):
    payload = {"model": model["id"], "messages": [{"role": "user", "content": PROBE_PROMPT}], "max_tokens": 1600}
    if "reasoning_effort" in model.get("supported_parameters", []):
        payload["reasoning"] = {"effort": "low"}
    try:
        response = fetch_json(CHAT_URL, payload=payload, key=key, timeout=90)
        content = response["choices"][0]["message"].get("content") or ""
        start = content.find("{")
        if start < 0:
            return False, "no JSON verdict (possibly reasoning-only/token cap)"
        answer, _ = json.JSONDecoder().raw_decode(content[start:])
        passed = (answer.get("verdict") == "FAIL" and answer.get("actual_for_1_1") is False
                  and answer.get("expected_for_1_1") is True and bool(answer.get("explanation")))
        cost = response.get("usage", {}).get("cost")
        suffix = f", billed ${cost:.6f}" if isinstance(cost, (int, float)) else ""
        return passed, ("caught planted bug" if passed else "missed bug or wrong result") + suffix
    except urllib.error.HTTPError as error:
        return False, f"HTTP {error.code}"
    except (OSError, ValueError, KeyError, IndexError, TypeError) as error:
        return False, f"probe failed: {type(error).__name__}"


def api_key(config_path):
    key = os.environ.get("OPENROUTER_API_KEY", "").strip()
    if key:
        return key
    env_path = config_path.parent / ".env"
    if env_path.exists():
        for line in env_path.read_text().splitlines():
            name, separator, value = line.strip().removeprefix("export ").partition("=")
            if separator and name.strip() == "OPENROUTER_API_KEY":
                return value.strip().strip("\"'")
    raise ValueError("--probe requires OPENROUTER_API_KEY in the environment or the config directory's .env")


def proposed_config(text, selected, roles):
    lines = text.splitlines(keepends=True)
    start, end = block_span(lines, "rolePrepend", 0)
    role_lines = lines[start + 1:end]
    qa_start, qa_end = block_span(role_lines, "qa", 2)
    shared = {values[0] for role, values in roles.items() if role != "qa" and values}
    models = list(dict.fromkeys(selected + roles["qa"]))
    models = [m for m in models if m not in shared] + [m for m in models if m in shared]
    replacement = ["  qa:\n", "    # Catalog suggestions selected by the user; existing fallbacks retained.\n"]
    replacement += [f"    - {json.dumps(model)}\n" for model in models]
    role_lines[qa_start:qa_end] = replacement
    lines[start + 1:end] = role_lines
    return "".join(lines)


def write_update(path, original, updated):
    if path.read_text() != original:
        raise ValueError("models.yaml changed during review; rerun to review the current file")
    timestamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%d-%H%M%S-%f")
    backup = path.with_name(path.name + ".bak-" + timestamp)
    shutil.copy2(path, backup)
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, prefix=".qa-models-", delete=False) as handle:
            temporary = Path(handle.name)
            handle.write(updated)
        temporary.chmod(path.stat().st_mode)
        os.replace(temporary, path)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)
    return backup


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, default=ROOT / "models.yaml")
    parser.add_argument("--max-input", type=float, default=0.50, help="Maximum USD per 1M input tokens (default .50)")
    parser.add_argument("--max-output", type=float, default=1.50, help="Maximum USD per 1M output tokens (default 1.50)")
    parser.add_argument("--min-context", type=int, default=128000)
    parser.add_argument("--limit", type=int, default=8)
    parser.add_argument("--include-free", action="store_true", help="Also consider free/experimental routes (excluded by default)")
    parser.add_argument("--probe", action="store_true", help="Make small billable bug-review requests for shortlisted models")
    parser.add_argument("--dry-run", action="store_true", help="Report only; never prompt or write")
    args = parser.parse_args(argv)
    if args.limit < 1 or args.min_context < 1 or args.max_input < 0 or args.max_output < 0:
        parser.error("limits and context must be positive; price ceilings must be nonnegative")
    try:
        path = args.config.resolve()
        original = path.read_text()
        roles, excluded = read_policy(original)
        key = api_key(path) if args.probe else None
        candidates = shortlist(fetch_json(CATALOG_URL)["data"], roles, excluded, args)
        print(f"OpenRouter catalog checked at {dt.datetime.now(dt.timezone.utc).isoformat(timespec='seconds')}")
        print("Shortlist: text output, tools, coding/agent description, context and price limits.")
        print("Ranked by estimated 50k-input + 2k-output review cost, not benchmark quality.")
        print("Prices are USD per 1M tokens; long-context/time-based overrides may apply.")
        print("Current QA chain: " + " -> ".join(roles["qa"]))
        print(" #  Model                                               Input    Output   Context   $/review")
        usable = set()
        for index, model in enumerate(candidates, 1):
            print(f"{index:2}  {model['id']:<49} {model['input_cost']:8.4f} {model['output_cost']:8.4f} {model['context_length']:9,} {model['review_cost']:9.5f}")
            overrides = model.get("pricing", {}).get("overrides")
            if overrides:
                print("    Price overrides: " + json.dumps(overrides, separators=(",", ":")))
            if args.probe:
                passed, detail = probe_model(model, key)
                print(f"    Probe {'PASS' if passed else 'FAIL'}: {detail}")
                if passed:
                    usable.add(index)
            else:
                usable.add(index)
        if not candidates:
            print("No candidates match. models.yaml unchanged.")
            return 0
        if args.dry_run or not sys.stdin.isatty():
            print("Report only; models.yaml unchanged. Run in a terminal without --dry-run to select models.")
            return 0
        if args.probe:
            print("Only models passing this small probe can be selected; it is not a full QA benchmark.")
        choice = input("Promote which models to QA? Enter numbers separated by commas, or Enter to cancel: ").strip()
        if not choice:
            print("Cancelled; models.yaml unchanged.")
            return 0
        indices = [int(value.strip()) for value in choice.split(",")]
        if not indices or any(index not in usable for index in indices):
            raise ValueError("Select only listed model numbers (and passing probes when --probe is used)")
        selected = [candidates[index - 1]["id"] for index in indices]
        updated = proposed_config(original, selected, roles)
        if updated == original:
            print("No changes needed.")
            return 0
        print("\nProposed changes (other roles are preserved; existing QA fallbacks retained):")
        print("".join(difflib.unified_diff(original.splitlines(True), updated.splitlines(True), fromfile=str(path), tofile=str(path) + " (proposed)")))
        if input("Apply these changes to models.yaml? [y/N] ").strip().lower() not in ("y", "yes"):
            print("Cancelled; models.yaml unchanged.")
            return 0
        backup = write_update(path, original, updated)
        print(f"Updated {path}\nBackup: {backup}")
        print("A running Freeride config watcher reloads models.yaml automatically.")
        return 0
    except (EOFError, KeyboardInterrupt):
        print("\nCancelled; models.yaml unchanged.")
        return 0
    except (OSError, ValueError, KeyError) as error:
        print(f"error: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
