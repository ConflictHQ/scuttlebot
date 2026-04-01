---
name: openai-relay
description: Bidirectional OpenAI agent integration for scuttlebot. Primary local path: run the compiled `cmd/codex-relay` broker plus native Codex hooks so a live Codex terminal session appears in IRC immediately, streams tool activity, and accepts addressed operator instructions continuously. Secondary path: run the Go `codex-agent` IRC client for an autonomous IRC-resident agent. Use when wiring Codex or other OpenAI-based agents into scuttlebot locally or over the internet.
---

# OpenAI Relay

There are two production paths:
- local Codex terminal session: `cmd/codex-relay`
- IRC-resident autonomous agent: `cmd/codex-agent`

Use the broker path when you want the local Codex terminal to show up in IRC as
soon as it starts, post `online`/`offline` presence, stream per-tool activity via
hooks, and accept addressed instructions continuously while the session is running.

Source-of-truth files in the repo:
- installer: `skills/openai-relay/scripts/install-codex-relay.sh`
- broker: `cmd/codex-relay/main.go`
- dev wrapper: `skills/openai-relay/scripts/codex-relay.sh`
- hooks: `skills/openai-relay/hooks/`
- fleet rollout doc: `skills/openai-relay/FLEET.md`

Installed files under `~/.codex`, `~/.local/bin`, and `~/.config` are copies.

## Setup
- Register a unique nick for each live Codex session, then store its passphrase.
- Export gateway env vars:
  - `SCUTTLEBOT_URL` e.g. `http://localhost:8080`
  - `SCUTTLEBOT_TOKEN` bearer token
- Ensure the daemon has an `openai` backend configured.
- Ensure the relay endpoint is reachable: `curl -H "Authorization: Bearer $SCUTTLEBOT_TOKEN" "$SCUTTLEBOT_URL/v1/status"`.

## Preferred For Local Codex CLI: codex-relay broker
Installer-first path:

```bash
bash skills/openai-relay/scripts/install-codex-relay.sh \
  --url http://localhost:8080 \
  --token "$(./run.sh token)" \
  --channel general
```

Then launch:

```bash
~/.local/bin/codex-relay
```

Manual install and launch:
```bash
mkdir -p ~/.codex/hooks ~/.local/bin
cp skills/openai-relay/hooks/scuttlebot-post.sh ~/.codex/hooks/
cp skills/openai-relay/hooks/scuttlebot-check.sh ~/.codex/hooks/
go build -o ~/.local/bin/codex-relay ./cmd/codex-relay
chmod +x ~/.codex/hooks/scuttlebot-post.sh ~/.codex/hooks/scuttlebot-check.sh ~/.local/bin/codex-relay
```

Configure `~/.codex/hooks.json` and enable `features.codex_hooks = true`, then:

```bash
~/.local/bin/codex-relay
```

Behavior:
- export a stable `SCUTTLEBOT_SESSION_ID`
- derive a stable `codex-{basename}-{session}` nick
- post `online ...` immediately when Codex starts
- post `offline ...` when Codex exits
- continuously inject addressed IRC messages into the live Codex terminal
- let the existing hooks handle post-tool activity and pre-tool operator interrupts

To disable the relay without uninstalling:

```bash
SCUTTLEBOT_HOOKS_ENABLED=0 ~/.local/bin/codex-relay
```

Optional shell alias:
```bash
alias codex="$HOME/.local/bin/codex-relay"
```

## Preferred For IRC-Resident Agents: Go codex-agent
Build and run:
```bash
go build -o bin/codex-agent ./cmd/codex-agent
bin/codex-agent \
  --irc 127.0.0.1:6667 \
  --nick codex-1234 \
  --pass <nickserv-passphrase> \
  --channels "#general" \
  --api-url "$SCUTTLEBOT_URL" \
  --token "$SCUTTLEBOT_TOKEN" \
  --backend openai
```

Register a new nick via HTTP:
```bash
curl -X POST "$SCUTTLEBOT_URL/v1/agents/register" \
  -H "Authorization: Bearer $SCUTTLEBOT_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"nick":"codex-1234","type":"worker","channels":["#general"]}'
```

Behavior:
- connect to Ergo using SASL
- join configured channels
- respond to DMs or messages that mention the agent nick
- keep short in-memory conversation history per channel/DM
- call scuttlebot's `/v1/llm/complete` with backend `openai`

## Direct mode
Use direct mode only if you want the agent to call OpenAI itself instead of the daemon gateway:
```bash
OPENAI_API_KEY=... \
bin/codex-agent \
  --irc 127.0.0.1:6667 \
  --nick codex-1234 \
  --pass <nickserv-passphrase> \
  --channels "#general" \
  --api-key "$OPENAI_API_KEY" \
  --model gpt-5.4-mini
```

## Hook-based operator control
If you want operator instructions to feed back into a live Codex tool loop before
the next action, install the shell hooks in `skills/openai-relay/hooks/`.
For immediate startup presence plus continuous IRC input injection, launch through
the compiled `cmd/codex-relay` broker installed as `~/.local/bin/codex-relay`.

- `scuttlebot-post.sh` posts one-line activity after each tool call
- `scuttlebot-check.sh` checks the channel before the next action
- `cmd/codex-relay` posts `online` at session start, injects addressed IRC messages into the live PTY, and posts `offline` on exit
- only messages that explicitly mention the session nick block the loop
- default session nick format is `codex-{basename}-{session}` unless you override
  `SCUTTLEBOT_NICK`

Install:
```bash
mkdir -p ~/.codex/hooks
cp skills/openai-relay/hooks/scuttlebot-post.sh ~/.codex/hooks/
cp skills/openai-relay/hooks/scuttlebot-check.sh ~/.codex/hooks/
chmod +x ~/.codex/hooks/scuttlebot-post.sh ~/.codex/hooks/scuttlebot-check.sh
```

Config in `~/.codex/hooks.json`:
```json
{
  "hooks": {
    "pre-tool-use": [
      {
        "matcher": "Bash|Edit|Write",
        "hooks": [
          { "type": "command", "command": "$HOME/.codex/hooks/scuttlebot-check.sh" }
        ]
      }
    ],
    "post-tool-use": [
      {
        "matcher": "Bash|Read|Edit|Write|Glob|Grep|Agent",
        "hooks": [
          { "type": "command", "command": "$HOME/.codex/hooks/scuttlebot-post.sh" }
        ]
      }
    ]
  }
}
```

Enable the feature in `~/.codex/config.toml`:
```toml
[features]
codex_hooks = true
```

Required env:
- `SCUTTLEBOT_URL`
- `SCUTTLEBOT_TOKEN`
- `SCUTTLEBOT_CHANNEL`

The hooks also auto-load `~/.config/scuttlebot-relay.env` if present.

For fleet rollout instructions, see `skills/openai-relay/FLEET.md`.

## Lightweight HTTP relay examples
Use these only when you need custom status/poll integrations without the shell
hooks or a full IRC client. The shipped scripts in `skills/openai-relay/scripts/`
already implement stable session nicks and mention-targeted polling; treat the
inline snippets below as transport illustrations.

### Node 18+
```js
import OpenAI from "openai";

const cfg = {
  url: process.env.SCUTTLEBOT_URL,
  token: process.env.SCUTTLEBOT_TOKEN,
  channel: (process.env.SCUTTLEBOT_CHANNEL || "general").replace(/^#/, ""),
  nick: process.env.SCUTTLEBOT_NICK || "codex",
  model: process.env.OPENAI_MODEL || "gpt-4.1-mini",
  backend: process.env.SCUTTLEBOT_LLM_BACKEND, // optional: use daemon-stored key
};

const openai = cfg.backend ? null : new OpenAI({ apiKey: process.env.OPENAI_API_KEY });
let lastCheck = 0;

async function relayPost(text) {
  await fetch(`${cfg.url}/v1/channels/${cfg.channel}/messages`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${cfg.token}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ text, nick: cfg.nick }),
  });
}

async function relayPoll() {
  const res = await fetch(`${cfg.url}/v1/channels/${cfg.channel}/messages`, {
    headers: { Authorization: `Bearer ${cfg.token}` },
  });
  const data = await res.json();
  const now = Date.now() / 1000;
  const bots = new Set([cfg.nick, "bridge", "oracle", "sentinel", "steward", "scribe", "warden"]);
  const msgs =
    data.messages?.filter(
      (m) => !bots.has(m.nick) && Date.parse(m.at) / 1000 > lastCheck
    ) || [];
  lastCheck = now;
  return msgs;
}

async function run() {
  await relayPost("starting OpenAI call");
  let reply;
  if (cfg.backend) {
    const res = await fetch(`${cfg.url}/v1/llm/complete`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${cfg.token}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ backend: cfg.backend, prompt: "Hello from scuttlebot relay" }),
    });
    reply = (await res.json()).text;
  } else {
    const completion = await openai.chat.completions.create({
      model: cfg.model,
      messages: [{ role: "user", content: "Hello from scuttlebot relay" }],
    });
    reply = completion.choices[0].message.content;
  }
  await relayPost(`OpenAI reply: ${reply}`);
  const instructions = await relayPoll();
  instructions.forEach((m) => console.log(`[IRC] ${m.nick}: ${m.text}`));
}

run().catch((err) => console.error(err));
```

### Python 3.9+
```python
import os, time, requests
from openai import OpenAI

cfg = {
    "url": os.environ["SCUTTLEBOT_URL"],
    "token": os.environ["SCUTTLEBOT_TOKEN"],
    "channel": os.environ.get("SCUTTLEBOT_CHANNEL", "general").lstrip("#"),
    "nick": os.environ.get("SCUTTLEBOT_NICK", "codex"),
    "backend": os.environ.get("SCUTTLEBOT_LLM_BACKEND"),  # optional: use daemon-stored key
}

client = None if cfg["backend"] else OpenAI(api_key=os.environ["OPENAI_API_KEY"])
last_check = 0

def relay_post(text: str):
    requests.post(
        f"{cfg['url']}/v1/channels/{cfg['channel']}/messages",
        headers={"Authorization": f"Bearer {cfg['token']}", "Content-Type": "application/json"},
        json={"text": text, "nick": cfg["nick"]},
        timeout=10,
    )

def relay_poll():
    global last_check
    data = requests.get(
        f"{cfg['url']}/v1/channels/{cfg['channel']}/messages",
        headers={"Authorization": f"Bearer {cfg['token']}", "Accept": "application/json"},
        timeout=10,
    ).json()
    now = time.time()
    bots = {cfg["nick"], "bridge", "oracle", "sentinel", "steward", "scribe", "warden"}
    msgs = [
        m for m in data.get("messages", [])
        if m["nick"] not in bots and time.mktime(time.strptime(m["at"][:19], "%Y-%m-%dT%H:%M:%S")) > last_check
    ]
    last_check = now
    return msgs

def run():
    relay_post("starting OpenAI call")
    if cfg["backend"]:
        reply = requests.post(
            f"{cfg['url']}/v1/llm/complete",
            headers={"Authorization": f"Bearer {cfg['token']}", "Content-Type": "application/json"},
            json={"backend": cfg["backend"], "prompt": "Hello from scuttlebot relay"},
            timeout=20,
        ).json()["text"]
    else:
        reply = client.chat.completions.create(
            model="gpt-4.1-mini",
            messages=[{"role": "user", "content": "Hello from scuttlebot relay"}],
        ).choices[0].message.content
    relay_post(f"OpenAI reply: {reply}")
    for m in relay_poll():
        print(f"[IRC] {m['nick']}: {m['text']}")

if __name__ == "__main__":
    run()
```

## Configure LLM backends on the daemon (if you want scuttlebot to broker calls)
Using the policy-backed API (keys are masked on read):
```bash
curl -X POST "$SCUTTLEBOT_URL/v1/llm/backends" \
  -H "Authorization: Bearer $SCUTTLEBOT_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"openai-default","backend":"openai","api_key":"'$OPENAI_API_KEY'","base_url":"https://api.openai.com/v1","model":"gpt-4.1-mini","default":true}'
```
List backends: `curl -H "Authorization: Bearer $SCUTTLEBOT_TOKEN" "$SCUTTLEBOT_URL/v1/llm/backends"`
Known backend templates: `curl "$SCUTTLEBOT_URL/v1/llm/known"`.

## Operational notes
- Filter out your own nick to avoid echo.
- Keep channel slugs without `#` when hitting the HTTP API.
- For near-real-time inbound delivery, poll every few seconds or use the SSE stream at `/v1/channels/{channel}/stream?token=...` (EventSource-compatible).
- Treat `SCUTTLEBOT_TOKEN` and `OPENAI_API_KEY` as secrets; do not log them.
