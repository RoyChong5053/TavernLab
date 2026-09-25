#!/usr/bin/env python3
"""Read-only truncation probe for an OpenAI-compatible chat upstream (one-api).

It only issues chat completions and reports finish_reason / char count / abnormal
stream termination. It never writes config or touches the data directory (env
vars only). Safe to run repeatedly.

Usage:
    scripts/repro_truncation.py
    UPSTREAM=http://127.0.0.1:3000 KEY=sk-xxx MODEL=auto-gemini scripts/repro_truncation.py

Env:
    UPSTREAM  default http://127.0.0.1:3000
    KEY       bearer token; if empty, read data/settings.json -> api_key
    MODEL     default auto-gemini
    N         trials per mode, default 5
    MAXTOK    max_tokens per trial, default 2048
    TIMEOUT   per-request seconds, default 180
    MODE      stream | nonstream | both, default both
    PROMPT    prompt text (defaults to a long-form request)
"""

import json
import os
import sys
import time
import urllib.error
import urllib.request

UPSTREAM = os.environ.get("UPSTREAM", "http://127.0.0.1:3000").rstrip("/")
MODEL = os.environ.get("MODEL", "auto-gemini")
N = int(os.environ.get("N", "5"))
MAXTOK = int(os.environ.get("MAXTOK", "2048"))
TIMEOUT = int(os.environ.get("TIMEOUT", "180"))
MODE = os.environ.get("MODE", "both")
PROMPT = os.environ.get(
    "PROMPT",
    "用中文写一段约800字的散文，主题是深夜的城市。不要省略，写完整。",
)


def load_key() -> str:
    key = os.environ.get("KEY", "")
    if key:
        return key
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    path = os.path.join(root, "data", "settings.json")
    if os.path.exists(path):
        try:
            return json.load(open(path)).get("api_key", "")
        except Exception:
            return ""
    return ""


def post(stream: bool) -> dict:
    body = json.dumps(
        {
            "model": MODEL,
            "stream": stream,
            "max_tokens": MAXTOK,
            "messages": [{"role": "user", "content": PROMPT}],
        }
    ).encode()
    req = urllib.request.Request(
        UPSTREAM + "/v1/chat/completions",
        data=body,
        headers={
            "Content-Type": "application/json",
            "Authorization": "Bearer " + KEY,
            "Accept": "text/event-stream" if stream else "application/json",
        },
    )
    t0 = time.time()
    try:
        resp = urllib.request.urlopen(req, timeout=TIMEOUT)
    except urllib.error.HTTPError as e:
        return {"http": e.code, "error": e.read()[:200].decode("utf-8", "replace"),
                "secs": round(time.time() - t0, 1)}
    except Exception as e:  # noqa: BLE001
        return {"http": 0, "error": str(e), "secs": round(time.time() - t0, 1)}

    status = resp.status
    if stream:
        finish = None
        chars = 0
        done = False
        err = None
        chunks = 0
        try:
            for raw in resp:
                line = raw.decode("utf-8", "replace").strip()
                if not line.startswith("data:"):
                    continue
                payload = line[5:].strip()
                if payload == "[DONE]":
                    done = True
                    continue
                if not payload:
                    continue
                try:
                    j = json.loads(payload)
                except Exception:
                    continue
                chunks += 1
                if isinstance(j, dict) and j.get("error"):
                    err = str(j["error"])[:200]
                ch = (j.get("choices") or [{}])[0]
                fr = ch.get("finish_reason")
                if fr:
                    finish = fr
                c = (ch.get("delta") or {}).get("content")
                if isinstance(c, str):
                    chars += len(c)
        except Exception as e:  # noqa: BLE001
            err = (err or "") + "|read:" + str(e)
        return {"http": status, "finish": finish, "chars": chars, "done": done,
                "error": err, "chunks": chunks, "secs": round(time.time() - t0, 1)}

    data = resp.read()
    try:
        j = json.loads(data)
    except Exception:
        return {"http": status, "error": "bad json", "secs": round(time.time() - t0, 1)}
    ch = (j.get("choices") or [{}])[0]
    content = (ch.get("message") or {}).get("content") or ""
    return {"http": status, "finish": ch.get("finish_reason"), "chars": len(content),
            "secs": round(time.time() - t0, 1)}


def run(mode: str) -> None:
    stream = mode == "stream"
    print(f"== {mode}  upstream={UPSTREAM}  model={MODEL}  max_tokens={MAXTOK}  n={N} ==")
    length_cut = abnormal = http_err = 0
    for i in range(1, N + 1):
        r = post(stream)
        flag = ""
        if r.get("http") != 200:
            http_err += 1
            flag = "HTTP_ERR"
        elif stream:
            if r.get("error"):
                abnormal += 1
                flag = "ABNORMAL"
            elif r.get("finish") == "length":
                length_cut += 1
                flag = "LENGTH"
            elif not r.get("done") and not r.get("finish"):
                abnormal += 1
                flag = "NO_TERMINAL"
        else:
            if r.get("finish") == "length":
                length_cut += 1
                flag = "LENGTH"
            elif not r.get("finish"):
                flag = "NO_FINISH"
        print(f"  #{i:<2} {flag:<12} http={r.get('http')} finish={r.get('finish')} "
              f"chars={r.get('chars')} chunks={r.get('chunks')} secs={r.get('secs')} "
              f"err={r.get('error')}")
    print(f"  -> length-cut={length_cut}  abnormal={abnormal}  http_errors={http_err}\n")


if __name__ == "__main__":
    KEY = load_key()
    if not KEY:
        print("KEY required (env KEY= or data/settings.json api_key)", file=sys.stderr)
        sys.exit(2)
    if MODE in ("stream", "both"):
        run("stream")
    if MODE in ("nonstream", "both"):
        run("nonstream")
