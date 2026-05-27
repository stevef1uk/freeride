#!/usr/bin/env python3
"""
Detect corrupted on-disk artifacts for *open/in_progress implement beads* and optionally delete them.

Goal: when a bead's artifact is clearly marker/conflict-corrupted (e.g. `>>>>>>> REPLACE`),
delete the file so the LLM can WRITE it afresh without fighting stale junk.

Heuristics are intentionally conservative and deterministic:
- Only deletes files for bead titles that match `Implement <path> per ...`.
- Only considers `.go` and `.py` artifacts for corruption checks.
- Go corruption checks:
  - conflict markers like `<<<<<<< SEARCH` / `>>>>>>> REPLACE` / `<<<<<<<` / `=======` / `>>>>>>>`
  - missing `package ...` near the top (first ~200 chars)
- Python corruption checks:
  - conflict markers / git-merge style fragments / `>>>>>>> REPLACE` / `<<<<<<< SEARCH`
  - ignores zero-size `.py` files (common "stub/module marker" cases)
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable


IMPLEMENT_TITLE_RE = re.compile(r"Implement\s+(.+?)\s+per\s+", re.IGNORECASE)


CONFLICT_MARKERS = (
    "<<<<<<<",
    "=======",
    ">>>>>>>",
)

# Our native-edit sentinel markers that sometimes leak into file bodies.
NATIVE_SENTINELS = (
    ">>>>>>> REPLACE",
    "<<<<<<< SEARCH",
    "---END WRITE---",
    "---END EDIT---",
)


@dataclass(frozen=True)
class BeadArtifact:
    bead_id: str
    bead_title: str
    status: str
    rel_path: str
    abs_path: Path


def run_bd_list_open_in_progress(beads_dir: Path, rig_workdir: Path) -> list[dict]:
    """
    Returns bd list rows parsed from `bd list --json`.
    """
    env = os.environ.copy()
    env["BEADS_DIR"] = str(beads_dir)

    cmd = [
        "bd",
        "list",
        "--status=open,in_progress",
        "--json",
        "--limit=0",
    ]
    # Important: run in the mayor/rig workdir so `bd` resolves paths consistently.
    proc = subprocess.run(
        cmd,
        cwd=str(rig_workdir),
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        check=False,
    )
    if proc.returncode != 0:
        # Make the failure deterministic and actionable.
        raise RuntimeError(
            f"bd list failed (exit {proc.returncode}). stderr:\n{proc.stderr.strip()}\nstdout:\n{proc.stdout.strip()}"
        )
    try:
        rows = json.loads(proc.stdout)
    except json.JSONDecodeError as e:
        raise RuntimeError(f"bd list --json produced non-JSON output: {e}\n{proc.stdout[:5000]}") from e
    if not isinstance(rows, list):
        raise RuntimeError(f"Unexpected bd list JSON type: {type(rows)}")
    return rows


def extract_implement_rel_path(title: str) -> str | None:
    m = IMPLEMENT_TITLE_RE.search(title or "")
    if not m:
        return None
    rel = m.group(1).strip().strip('"').strip("'")
    # Normalize separators; expected to be layout-root-relative (e.g. linkshelf/...).
    rel = rel.replace("\\", "/")
    return rel or None


def iter_open_implement_bead_artifacts(town_root: Path, rig: str) -> Iterable[BeadArtifact]:
    rig_workdir = town_root / rig / "mayor" / "rig"
    beads_dir = rig_workdir / ".beads"
    if not rig_workdir.exists():
        raise FileNotFoundError(f"rig workdir not found: {rig_workdir}")
    if not beads_dir.exists():
        raise FileNotFoundError(f"beads dir not found: {beads_dir}")

    rows = run_bd_list_open_in_progress(beads_dir=beads_dir, rig_workdir=rig_workdir)
    for r in rows:
        title = r.get("title", "")
        if not title or "Implement" not in title:
            continue
        rel = extract_implement_rel_path(title)
        if not rel:
            continue
        bead_id = str(r.get("id", "")).strip()
        if not bead_id:
            continue
        status = str(r.get("status", "")).strip()
        abs_path = (rig_workdir / rel).resolve()
        # Safety: ensure the path stays under rig_workdir.
        if rig_workdir.resolve() not in abs_path.parents and abs_path != rig_workdir.resolve():
            # Skip suspicious paths.
            continue
        yield BeadArtifact(
            bead_id=bead_id,
            bead_title=title,
            status=status,
            rel_path=rel,
            abs_path=abs_path,
        )


def read_text_head(path: Path, max_chars: int = 64_000) -> str:
    # Read with a forgiving decoder: corruption sentinels are ASCII.
    data = path.read_bytes()[:max_chars]
    return data.decode("utf-8", errors="replace")


def likely_go_corrupted(text: str, size: int) -> tuple[bool, str]:
    # Very small files frequently contain leaked sentinel markers rather than real code.
    if size <= 40 and any(s in text for s in NATIVE_SENTINELS):
        return True, "size tiny and contains native sentinels"

    if any(s in text for s in NATIVE_SENTINELS):
        # Covers >>>>>>> REPLACE / <<<<<<< SEARCH and other edit/write termination leaks.
        return True, "contains native edit sentinels"

    if any(m in text for m in CONFLICT_MARKERS):
        # Covers git-merge style conflict fragments.
        return True, "contains conflict markers"

    # Deterministic top-of-file heuristic:
    # Look for `package ...` within the first ~200 chars (after whitespace).
    head = text[:200]
    if not re.search(r"^\s*package\s+[A-Za-z_]\w*", head, flags=re.MULTILINE):
        return True, "missing `package ...` near top"

    # Also detect the common "broken fragment" patterns from earlier failures.
    if "}; if err" in text or "}||" in text or "Descriptionn" in text:
        return True, "contains known broken fragment patterns"

    return False, ""


def likely_py_corrupted(text: str, size: int) -> tuple[bool, str]:
    if size == 0:
        # User request: ignore zero-size Python files (often module markers/stubs).
        return False, ""

    if any(s in text for s in NATIVE_SENTINELS):
        return True, "contains native edit sentinels"

    if any(m in text for m in CONFLICT_MARKERS):
        return True, "contains conflict markers"

    # Also catch common "conflict prologue" cases near the start.
    if re.match(r"^\s*<{7,}|^\s*>{7,}", text):
        return True, "looks like conflict block start"

    return False, ""


def detect_corrupted(artifact: BeadArtifact) -> tuple[bool, str]:
    p = artifact.abs_path
    try:
        st = p.stat()
    except FileNotFoundError:
        return False, "missing file (cannot delete)"

    size = int(st.st_size)
    suffix = p.suffix.lower()

    # Read head for heuristics.
    text = read_text_head(p) if size > 0 else ""

    if suffix == ".go":
        return likely_go_corrupted(text, size)
    if suffix == ".py":
        return likely_py_corrupted(text, size)
    # Other extensions ignored.
    return False, ""


def delete_file(path: Path) -> None:
    # Deterministic safety: refuse deletion outside current rig.
    path.unlink()


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--town-root", default="/home/stevef/gt", help="GAS TOWN root (default: /home/stevef/gt)")
    ap.add_argument("--rig", default="testgt3", help="Rig name (default: testgt3)")
    ap.add_argument("--apply", action="store_true", help="Actually delete files (otherwise dry-run).")
    ap.add_argument("--limit", type=int, default=0, help="Optional max deletions (0 = no limit).")
    args = ap.parse_args()

    town_root = Path(args.town_root).resolve()
    rig = args.rig

    artifacts = list(iter_open_implement_bead_artifacts(town_root=town_root, rig=rig))
    # Deterministic order.
    artifacts.sort(key=lambda a: (a.status, a.rel_path, a.bead_id))

    deletable: list[tuple[BeadArtifact, str]] = []
    for art in artifacts:
        is_bad, reason = detect_corrupted(art)
        if is_bad:
            deletable.append((art, reason))

    if args.limit and args.limit > 0:
        deletable = deletable[: args.limit]

    print(f"Detected {len(deletable)} corrupted artifacts among open/in_progress implement beads.")
    if len(deletable) == 0:
        return 0

    for art, reason in deletable:
        try:
            sz = art.abs_path.stat().st_size
        except FileNotFoundError:
            sz = -1
        print(
            f"- {art.bead_id} [{art.status}] {art.rel_path} "
            f"(size={sz}): {reason}"
        )

    if not args.apply:
        print("\nDry-run: re-run with `--apply` to delete the files.")
        return 0

    # Delete with deterministic ordering.
    for art, _reason in deletable:
        print(f"Deleting: {art.abs_path}")
        delete_file(art.abs_path)

    print("Deletion complete.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

