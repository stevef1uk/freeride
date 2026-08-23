#!/usr/bin/env python3
"""Benchmark an inference engine against gastown's agent-format workloads.

Usage:
  python3 scripts/llm_bench.py --engine freetoken [--out results_freetoken.json]
  python3 scripts/llm_bench.py --engine llama     [--out results_llama.json]
  python3 scripts/llm_bench.py --compare results_freetoken.json results_llama.json

Run one engine at a time (only one fits the GPU). Each run: health check,
2 discarded warm-ups, then five identical format/comprehension tests.
"""

import argparse
import json
import sys
import time
import urllib.request
import urllib.error

ENGINES = {
    "freetoken": {
        "base": "http://127.0.0.1:1919",
        "model": "Qwen3.6-35B-A3B-NVFP4",
        "extra_body": {"chat_template_kwargs": {"enable_thinking": False}},
        "suffix": "",
    },
    "llama": {
        "base": "http://127.0.0.1:8090",
        "model": "byteshape/Qwen3-Coder-30B-A3B-Instruct-GGUF",
        "extra_body": {},
        "suffix": " /no_think",
    },
}

FAKE_SPEC = """# LinkShelf Service Specification

## Overview
LinkShelf catalogs web bookmarks. The backend exposes a REST API and a
background maintenance loop handles deduplication.

## Runtime
- The service binds to port 7412 on all interfaces.
- Readiness is reported at /readyz once the schema migration completes.
- The metrics scraper authenticates with bearer token MT-9917.

## Maintenance
- Deduplication scans run every fifteen minutes.
- Database backups trigger during the backup window at 03:40 UTC.
- Orphaned link rows older than ninety days are purged.

## API Surface
GET /links            list links (paginated)
POST /links           create link
DELETE /links/{id}    remove link
GET /readyz           readiness probe
"""


def chat(base, model, extra_body, content, max_tokens, timeout):
    body = {
        "model": model,
        "max_tokens": max_tokens,
        "messages": [{"role": "user", "content": content}],
    }
    body.update(extra_body)
    req = urllib.request.Request(
        base + "/v1/chat/completions",
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json"},
    )
    t0 = time.time()
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        data = json.loads(resp.read())
    dt = time.time() - t0
    msg = data["choices"][0]["message"]
    return {
        "content": msg.get("content") or "",
        "reasoning_len": len(msg.get("reasoning_content") or ""),
        "completion_tokens": data["usage"]["completion_tokens"],
        "finish": data["choices"][0]["finish_reason"],
        "latency_s": round(dt, 2),
    }


def health_ok(base, model):
    try:
        with urllib.request.urlopen(base + "/v1/models", timeout=8) as r:
            return model in r.read().decode()
    except Exception as e:
        print(f"HEALTH FAIL ({base}): {e}")
        return False


def run_tests(call):
    results = {}

    def record(name, c, checks):
        passed = all(checks.values())
        results[name] = {
            "pass": passed,
            "checks": checks,
            "latency_s": c["latency_s"],
            "completion_tokens": c["completion_tokens"],
            "tok_per_s": round(c["completion_tokens"] / c["latency_s"], 1) if c["latency_s"] > 0 else 0,
            "reasoning_len": c["reasoning_len"],
            "finish": c["finish"],
            "content_head": c["content"][:400],
        }
        status = "PASS" if passed else "FAIL"
        failed = [k for k, v in checks.items() if not v]
        detail = "" if passed else "  failed: " + ", ".join(failed)
        print(f"  [{status}] {name}  {c['latency_s']}s  {results[name]['tok_per_s']} tok/s{detail}")

    c = chat_call("You are finalizing a design step. Reply with EXACTLY one shell command line "
                  "(prefixed CMD:) that writes a file architecture.md using a single-quoted heredoc "
                  "containing two sections titled ### go-module and ### core, terminated by a line "
                  "containing only EOF. No markdown fences. No explanation.", 800)
    record("t1_heredoc", c, {
        "cmd_prefix": c["content"].strip().startswith("CMD:"),
        "quoted_heredoc": "<<'EOF'" in c["content"],
        "eof_terminator_line": any(x.strip() == "EOF" for x in c["content"].splitlines()),
        "both_headings": ("### go-module" in c["content"] and "### core" in c["content"]),
        "no_fences": "```" not in c["content"],
    })

    c = chat_call("Step complete. Reply with ONLY this JSON, nothing else: "
                  '{"outcome":"success","summary":"wrote main.py"}', 200)
    s = c["content"].strip()
    record("t2_json_purity", c, {
        "pure_json": s.startswith("{") and s.rstrip().endswith("}"),
        "no_fences": "```" not in s,
        "no_cmd_lines": "CMD:" not in s,
    })

    c = chat_call("Implementation turn. Reply with exactly three consecutive parts and nothing else.\n"
                  "Part 1: the exact line WRITE: pingapp/main.py\n"
                  "Part 2: a complete runnable Python file that defines a FastAPI app with one "
                  'GET /ping route whose JSON response body is {"message":"pong"}\n'
                  "Part 3: the exact line ---END WRITE---\n"
                  "No markdown fences. No commentary.", 700)
    record("t3_write_marker", c, {
        "write_path_prefix": "WRITE: pingapp/main.py" in c["content"],
        "end_marker": "---END WRITE---" in c["content"],
        "fastapi_route": 'app.get("/ping")' in c["content"],
        "pong_body": '"pong"' in c["content"],
        "no_fences": "```" not in c["content"],
    })

    original = ('from fastapi import FastAPI\napp = FastAPI()\n\n'
                '@app.get("/ping")\ndef ping():\n    return {"message": "pong"}')
    c = chat_call('pingapp/main.py currently contains exactly:\n\n' + original +
                  '\n\nReply in EDIT format to change the response message to "pong-ok":\n'
                  "EDIT: pingapp/main.py\n<<<<<<< SEARCH\n<exact existing lines being replaced>\n"
                  "=======\n<replacement lines>\n>>>>>>> REPLACE\nNo other text.", 500)
    search_ok = replace_ok = False
    if "<<<<<<< SEARCH" in c["content"] and "=======" in c["content"]:
        try:
            search_section = c["content"].split("<<<<<<< SEARCH")[1].split("=======")[0]
            replace_section = c["content"].split("=======")[1]
            search_ok = 'return {"message": "pong"}' in search_section
            replace_ok = 'return {"message": "pong-ok"}' in replace_section.split(">>>>>>> REPLACE")[0]
        except IndexError:
            pass
    record("t4_edit_search", c, {
        "search_marker": "<<<<<<< SEARCH" in c["content"],
        "divider": "=======" in c["content"],
        "replace_marker": ">>>>>>> REPLACE" in c["content"],
        "exact_old_line_in_search": search_ok,
        "new_line_in_replace": replace_ok,
    })

    c = chat_call("Read this specification:\n\n" + FAKE_SPEC +
                  "\n\nReply with ONLY the three values the spec names, in this order, "
                  "comma separated: bind port, metrics token, backup window time.", 120)
    record("t5_long_context", c, {
        "port_7412": "7412" in c["content"],
        "token_MT-9917": "MT-9917" in c["content"],
        "backup_0340": "03:40" in c["content"],
    })

    return results


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--engine", choices=sorted(ENGINES))
    ap.add_argument("--out")
    ap.add_argument("--compare", nargs=2, metavar=("A.json", "B.json"))
    ap.add_argument("--warmup", type=int, default=2)
    ap.add_argument("--timeout", type=int, default=180)
    args = ap.parse_args()

    if args.compare:
        a_name, b_name = args.compare
        a = json.load(open(a_name))
        b = json.load(open(b_name))
        tests = sorted(set(a["tests"]) | set(b["tests"]))
        print(f"\n{'test':22} {a['engine']:>10} {b['engine']:>10}")
        print("-" * 46)
        pa = pb = 0
        for t in tests:
            ra = a["tests"].get(t, {}).get("pass", False)
            rb = b["tests"].get(t, {}).get("pass", False)
            pa += ra
            pb += rb
            sa = f"{'PASS' if ra else 'FAIL':>5} {a['tests'].get(t, {}).get('latency_s', '-'):>6}s"
            sb = f"{'PASS' if rb else 'FAIL':>5} {b['tests'].get(t, {}).get('latency_s', '-'):>6}s"
            print(f"{t:22} {sa:>14} {sb:>14}")
        print("-" * 46)
        print(f"{'TOTAL PASS':22} {pa:>10}/{len(tests)} {pb:>10}/{len(tests)}")
        ta = sum(t["latency_s"] for t in a["tests"].values())
        tb = sum(t["latency_s"] for t in b["tests"].values())
        print(f"{'total latency':22} {ta:>9.1f}s {tb:>9.1f}s")
        if pa != pb:
            print(f"\nVERDICT: {a['engine'] if pa > pb else b['engine']} wins on correctness "
                  f"({max(pa, pb)}/{len(tests)} formats correct)")
        elif abs(ta - tb) > 1:
            print(f"\nVERDICT: tie on correctness; {a['engine'] if ta < tb else b['engine']} faster overall")
        else:
            print("\nVERDICT: statistical tie")
        sys.exit(0)

    if not args.engine:
        ap.error("either --engine or --compare is required")
    cfg = ENGINES[args.engine]

    if not health_ok(cfg["base"], cfg["model"]):
        print(f"Engine '{args.engine}' does not appear to be serving {cfg['model']} at {cfg['base']}.")
        sys.exit(1)
    print(f"Engine '{args.engine}' healthy at {cfg['base']} ({cfg['model']})")

    def call_fn(content, max_tokens):
        return chat(cfg["base"], cfg["model"], cfg["extra_body"],
                    content + cfg["suffix"], max_tokens, args.timeout)

    globals()["chat_call"] = call_fn

    print(f"Warm-up x{args.warmup} (discarded)...")
    for i in range(args.warmup):
        try:
            r = call_fn("hi", 24)
            print(f"  warmup {i + 1}: {r['latency_s']}s")
        except Exception as e:
            print(f"  warmup {i + 1} FAILED: {e}")

    print("Battery:")
    tests = run_tests(call_fn)

    result = {
        "engine": args.engine,
        "model": cfg["model"],
        "finished_at": time.strftime("%Y-%m-%dT%H:%M:%S"),
        "tests": tests,
    }
    out = args.out or f"bench_{args.engine}.json"
    with open(out, "w") as f:
        json.dump(result, f, indent=2)
    npass = sum(1 for t in tests.values() if t["pass"])
    print(f"\n{npass}/{len(tests)} tests passed. Results saved to {out}")
