#!/usr/bin/env python3
"""Minimal OpenAI + scuttlebot relay example.

Env required:
  SCUTTLEBOT_URL, SCUTTLEBOT_TOKEN, SCUTTLEBOT_CHANNEL
Optional:
  SCUTTLEBOT_NICK, SCUTTLEBOT_SESSION_ID, OPENAI_MODEL (default: gpt-4.1-mini)
"""
import os
import re
import sys
import time
from datetime import datetime
import requests
from openai import OpenAI

prompt = sys.argv[1] if len(sys.argv) > 1 else "Hello from openai-relay"


def sanitize(value: str) -> str:
    return re.sub(r"[^A-Za-z0-9_-]+", "-", value).strip("-") or "session"


base_name = sanitize(os.path.basename(os.getcwd()) or "repo")
session_suffix = sanitize(
    os.environ.get("SCUTTLEBOT_SESSION_ID")
    or os.environ.get("CODEX_SESSION_ID")
    or str(os.getppid())
)

cfg = {
    "url": os.environ.get("SCUTTLEBOT_URL"),
    "token": os.environ.get("SCUTTLEBOT_TOKEN"),
    "channel": (os.environ.get("SCUTTLEBOT_CHANNEL", "general")).lstrip("#"),
    "nick": os.environ.get(
        "SCUTTLEBOT_NICK", f"codex-{base_name}-{session_suffix}"
    ),
    "model": os.environ.get("OPENAI_MODEL", "gpt-4.1-mini"),
    "backend": os.environ.get("SCUTTLEBOT_LLM_BACKEND", "openai"),  # default to daemon-stored openai backend
}

missing = [k for k, v in cfg.items() if not v and k != "model"]
use_backend = bool(cfg["backend"])
if missing:
    print(f"missing env: {', '.join(missing)}", file=sys.stderr)
    sys.exit(1)
if not use_backend and "OPENAI_API_KEY" not in os.environ:
    print("missing env: OPENAI_API_KEY (or set SCUTTLEBOT_LLM_BACKEND to use server-side key)", file=sys.stderr)
    sys.exit(1)

client = None if use_backend else OpenAI(api_key=os.environ["OPENAI_API_KEY"])
last_check = 0.0
mention_re = re.compile(
    rf"(^|[^A-Za-z0-9_./\\-]){re.escape(cfg['nick'])}($|[^A-Za-z0-9_./\\-])",
    re.IGNORECASE,
)


def relay_post(text: str) -> None:
    res = requests.post(
        f"{cfg['url']}/v1/channels/{cfg['channel']}/messages",
        headers={
            "Authorization": f"Bearer {cfg['token']}",
            "Content-Type": "application/json",
        },
        json={"text": text, "nick": cfg["nick"]},
        timeout=10,
    )
    res.raise_for_status()


def relay_poll():
    global last_check
    res = requests.get(
        f"{cfg['url']}/v1/channels/{cfg['channel']}/messages",
        headers={"Authorization": f"Bearer {cfg['token']}"},
        timeout=10,
    )
    res.raise_for_status()
    data = res.json()
    now = time.time()
    bots = {
        cfg["nick"],
        "bridge",
        "oracle",
        "sentinel",
        "steward",
        "scribe",
        "warden",
        "snitch",
        "herald",
        "scroll",
        "systembot",
        "auditbot",
        "claude",
    }
    msgs = [
        m
        for m in data.get("messages", [])
        if m["nick"] not in bots
        and not m["nick"].startswith("claude-")
        and not m["nick"].startswith("codex-")
        and not m["nick"].startswith("gemini-")
        and datetime.fromisoformat(m["at"].replace("Z", "+00:00")).timestamp() > last_check
        and mention_re.search(m["text"])
    ]
    last_check = now
    return msgs


def main():
    relay_post(f"starting: {prompt}")
    if use_backend:
        res = requests.post(
            f"{cfg['url']}/v1/llm/complete",
            headers={
                "Authorization": f"Bearer {cfg['token']}",
                "Content-Type": "application/json",
            },
            json={"backend": cfg["backend"], "prompt": prompt},
            timeout=20,
        )
        res.raise_for_status()
        reply = res.json()["text"]
    else:
        completion = client.chat.completions.create(
            model=cfg["model"],
            messages=[{"role": "user", "content": prompt}],
        )
        reply = completion.choices[0].message.content
    print(f"OpenAI: {reply}")
    relay_post(f"OpenAI reply: {reply}")
    for m in relay_poll():
        print(f"[IRC] {m['nick']}: {m['text']}")


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:  # broad but fine for CLI sample
        print(exc, file=sys.stderr)
        sys.exit(1)
