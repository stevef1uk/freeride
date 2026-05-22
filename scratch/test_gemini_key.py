#!/usr/bin/env python3
"""Test GEMINI_API_KEY from .env (no secrets printed)."""
import json
import os
import urllib.error
import urllib.request

def load_gemini_key():
    path = os.path.join(os.path.dirname(__file__), "..", ".env")
    for line in open(path):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if line.split("=", 1)[0].strip() == "GEMINI_API_KEY":
            return line.split("=", 1)[1].strip()
    return ""


def post(url, headers, body):
    req = urllib.request.Request(
        url,
        data=json.dumps(body).encode(),
        headers=headers,
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return resp.status, json.loads(resp.read().decode())
    except urllib.error.HTTPError as e:
        raw = e.read().decode()
        try:
            data = json.loads(raw)
        except json.JSONDecodeError:
            data = {"raw": raw[:500]}
        return e.code, data


def main():
    key = load_gemini_key()
    if not key:
        print("FAIL: GEMINI_API_KEY not found in .env")
        return 1
    print(f"OK: loaded key ({len(key)} chars)")

    tests = [
        (
            "openai chat gemini-2.5-flash",
            "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions",
            {"Authorization": f"Bearer {key}", "Content-Type": "application/json"},
            {
                "model": "gemini-2.5-flash",
                "messages": [{"role": "user", "content": "Reply exactly: GEMINI_OK"}],
                "max_tokens": 16,
            },
        ),
        (
            "openai chat gemini-3.5-flash",
            "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions",
            {"Authorization": f"Bearer {key}", "Content-Type": "application/json"},
            {
                "model": "gemini-3.5-flash",
                "messages": [{"role": "user", "content": "Reply exactly: GEMINI_OK"}],
                "max_tokens": 16,
            },
        ),
        (
            "native generateContent",
            "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent",
            {"x-goog-api-key": key, "Content-Type": "application/json"},
            {"contents": [{"parts": [{"text": "Reply exactly: GEMINI_OK"}]}]},
        ),
    ]

    any_ok = False
    for name, url, headers, body in tests:
        status, data = post(url, headers, body)
        if status == 200:
            any_ok = True
            if "choices" in data:
                msg = data["choices"][0].get("message", {})
                text = msg.get("content") or msg.get("refusal") or str(msg)
            else:
                text = data["candidates"][0]["content"]["parts"][0]["text"]
            print(f"PASS [{name}] HTTP {status}: {text!r}")
        else:
            if isinstance(data, list) and data and isinstance(data[0], dict):
                err = data[0].get("error", data[0])
            elif isinstance(data, dict):
                err = data.get("error", data)
            else:
                err = data
            if isinstance(err, dict):
                msg = err.get("message", str(err))
            else:
                msg = str(err)
            print(f"FAIL [{name}] HTTP {status}: {msg}")

    return 0 if any_ok else 2


if __name__ == "__main__":
    raise SystemExit(main())
