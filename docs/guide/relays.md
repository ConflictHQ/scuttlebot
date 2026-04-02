# Relay Brokers

A relay broker wraps a local LLM CLI session — Claude Code, Codex, or Gemini — on a pseudo-terminal (PTY) and bridges it into the scuttlebot IRC backplane. Every tool call the agent makes is mirrored to the channel in real time, and operators can address the session by nick to inject instructions directly into the running terminal.

---

## Why relay brokers exist

Hook-only telemetry posts what happened after the fact. It cannot:

- interrupt a running agent mid-task
- inject operator guidance before the next tool call
- establish real IRC presence for the session nick

The relay broker solves all three. It owns the entire session lifecycle:

1. starts the agent CLI on a PTY
2. registers a fleet-style IRC nick and posts `online`
3. tails the session JSONL and mirrors output to IRC as it arrives
4. polls IRC every 2 seconds for messages that mention the session nick
5. injects addressed operator messages into the live PTY (with Ctrl+C if needed)
6. posts `offline (exit N)` and deregisters the nick on exit

When the relay is active it also sets `SCUTTLEBOT_ACTIVITY_VIA_BROKER=1` in the child environment, which tells the hook scripts to stay quiet and avoid double-posting.

---

## How it works end-to-end

```
operator in IRC channel
        │  mentions claude-myrepo-a1b2c3d4
        ▼
  relay input loop (polls every 2s)
        │  filterMessages: must mention nick, not from bots/service accounts
        ▼
  PTY write (Ctrl+C if agent is busy, then inject text)
        │
        ▼
  Claude / Codex / Gemini CLI on PTY
        │  writes JSONL session file
        ▼
  mirrorSessionLoop (tails session JSONL, 250ms scan)
        │  sessionMessages: assistant text + tool_use blocks
        │  skips: thinking blocks, non-assistant entries
        ▼
  relay.Post → IRC channel
```

### Session nick generation

The nick is auto-generated from the project directory base name and a CRC32 of the process IDs and timestamp:

```
claude-{repo-basename}-{8-char-hex}
codex-{repo-basename}-{8-char-hex}
gemini-{repo-basename}-{8-char-hex}
```

Examples:

```
claude-scuttlebot-a1b2c3d4
codex-api-9c0d1e2f
gemini-myapp-e5f6a7b8
```

Override with `SCUTTLEBOT_NICK` in `~/.config/scuttlebot-relay.env`.

### Online / offline presence

On successful IRC or HTTP connect the broker posts:

```
online in scuttlebot; mention claude-scuttlebot-a1b2c3d4 to interrupt before the next action
```

On process exit (any exit code):

```
offline (exit 0)
offline (exit 1)
```

If the relay cannot connect (no token, IRC unreachable), the agent runs normally with no IRC presence. The session is not aborted.

---

## The three runtimes

=== "Claude"

    **Binary:** `cmd/claude-relay`
    **Default transport:** IRC
    **Session file:** `~/.claude/projects/<sanitized-cwd>/<session>.jsonl`

    Claude Code writes a JSONL file for each session under `~/.claude/projects/`. The relay discovers the matching file by scanning for `.jsonl` files modified after session start, verifying the `cwd` field in the first few entries. It then tails from the current end of file so only new output is mirrored.

    Mirrored entry types:

    | JSONL block type | What gets posted |
    |---|---|
    | `text` | assistant text, split at 360-char line limit |
    | `tool_use` | compact summary: `› bash cmd`, `edit path/to/file`, `grep pattern`, etc. |
    | `thinking` | skipped — too verbose for IRC |

    Busy detection: the relay looks for the string `esc to interrupt` in PTY output. If seen within the last 1.5 seconds, Ctrl+C is sent before injecting the operator message.

=== "Codex"

    **Binary:** `cmd/codex-relay`
    **Default transport:** HTTP
    **Session file:** Codex session JSONL (format differs from Claude)

    The Codex relay reads `response_item` entries from the session JSONL. Tool activity is published as:

    | Entry type | What gets posted |
    |---|---|
    | `function_call: exec_command` | `› <command>` (truncated to 140 chars) |
    | `function_call: parallel` | `parallel N tools` |
    | `function_call: spawn_agent` | `spawn agent` |
    | `custom_tool_call: apply_patch` | `patch path/to/file` or `patch N files: ...` |
    | `message (role: assistant)` | assistant text, split at 360-char limit |

    Gemini uses bracketed paste sequences (`\x1b[200~` / `\x1b[201~`) when injecting operator messages to preserve multi-line input correctly.

=== "Gemini"

    **Binary:** `cmd/gemini-relay`
    **Default transport:** HTTP
    **Session file:** Gemini session JSONL

    The Gemini relay uses bracketed paste mode when injecting operator messages — Gemini CLI requires this for multi-line injection. Otherwise the architecture is identical to the Codex relay.

---

## Session mirroring in detail

The broker finds the session file by:

1. computing `~/.claude/projects/<sanitized-cwd>/` (Claude) or the runtime equivalent
2. scanning for `.jsonl` files modified after `startedAt - 2s`
3. peeking at the first five lines of each candidate to match `cwd` against the working directory
4. selecting the newest match
5. seeking to the end of the file and entering a tail loop (250ms poll interval)

Each line from the tail loop is passed through `sessionMessages`, which:

- ignores non-assistant entries
- extracts `text` blocks (splits on newlines, wraps at 360 chars)
- summarizes `tool_use` blocks into one-line descriptions
- redacts secrets: bearer tokens, `sk-` prefixed API keys, 32+ char hex strings, `TOKEN=`, `KEY=`, `SECRET=` assignments

Lines are posted to the relay channel one at a time. Empty lines are skipped.

---

## Operator inject in detail

The relay input loop runs on a `SCUTTLEBOT_POLL_INTERVAL` (default 2s) ticker. On each tick it calls `relay.MessagesSince(ctx, lastSeen)` and applies `filterMessages`:

**A message is injected only if:**

- its timestamp is strictly after `lastSeen`
- its nick is not the session nick itself
- its nick is not in the service bot list (`bridge`, `oracle`, `sentinel`, `steward`, `scribe`, `warden`, `snitch`, `herald`, `scroll`, `systembot`, `auditbot`)
- its nick does not start with a known activity prefix (`claude-`, `codex-`, `gemini-`)
- the message text contains the session nick (word-boundary match)

Accepted messages are formatted as:

```
[IRC operator messages]
operatornick: the message text
```

and written to the PTY. If `SCUTTLEBOT_INTERRUPT_ON_MESSAGE=1` and the agent was seen as busy within the last 1.5 seconds, Ctrl+C is sent 150ms before the text inject.

---

## Installing each relay

=== "Claude"

    Run from the repo checkout:

    ```bash
    bash skills/scuttlebot-relay/scripts/install-claude-relay.sh \
      --url http://localhost:8080 \
      --token "$(./run.sh token)" \
      --channel general
    ```

    Or via Make:

    ```bash
    SCUTTLEBOT_URL=http://localhost:8080 \
    SCUTTLEBOT_TOKEN="$(./run.sh token)" \
    SCUTTLEBOT_CHANNEL=general \
    make install-claude-relay
    ```

    After install, use the wrapper instead of the bare `claude` command:

    ```bash
    ~/.local/bin/claude-relay
    ```

=== "Codex"

    ```bash
    bash skills/scuttlebot-relay/scripts/install-claude-relay.sh \
      --url http://localhost:8080 \
      --token "$(./run.sh token)" \
      --channel general
    ```

    After install:

    ```bash
    ~/.local/bin/claude-relay  # same wrapper pattern
    ```

=== "Gemini"

    ```bash
    bash skills/gemini-relay/scripts/install-gemini-relay.sh \
      --url http://localhost:8080 \
      --token "$(./run.sh token)" \
      --channel general
    ```

    After install:

    ```bash
    ~/.local/bin/gemini-relay
    ```

For a remote scuttlebot instance, pass the full URL and optionally select IRC transport:

```bash
bash skills/gemini-relay/scripts/install-gemini-relay.sh \
  --url http://scuttlebot.example.com:8080 \
  --token "$SCUTTLEBOT_TOKEN" \
  --channel fleet \
  --transport irc \
  --irc-addr scuttlebot.example.com:6667
```

Install in disabled mode (hooks present but silent):

```bash
bash skills/gemini-relay/scripts/install-gemini-relay.sh --disabled
```

Re-enable later:

```bash
bash skills/gemini-relay/scripts/install-gemini-relay.sh --enabled
```

---

## Environment variable reference

All variables are read from the environment first, then from `~/.config/scuttlebot-relay.env`, then fall back to compiled defaults. The config file format is `KEY=value` (one per line, `#` comments, optional `export ` prefix, optional quotes stripped).

| Variable | Default | Description |
|---|---|---|
| `SCUTTLEBOT_URL` | `http://localhost:8080` | Daemon HTTP API base URL |
| `SCUTTLEBOT_TOKEN` | — | Bearer token for the HTTP API. Relay disabled if unset (HTTP transport) |
| `SCUTTLEBOT_CHANNEL` | `general` | Channel name without `#` |
| `SCUTTLEBOT_TRANSPORT` | `irc` (Claude), `http` (Codex, Gemini) | `irc` or `http` |
| `SCUTTLEBOT_IRC_ADDR` | `127.0.0.1:6667` | Ergo IRC address (IRC transport only) |
| `SCUTTLEBOT_IRC_PASS` | — | Fixed NickServ password (IRC transport). If unset, the broker auto-registers a session nick via the API |
| `SCUTTLEBOT_IRC_AGENT_TYPE` | `worker` | Agent type registered with scuttlebot (IRC transport) |
| `SCUTTLEBOT_IRC_DELETE_ON_CLOSE` | `true` | Delete the auto-registered nick on clean exit |
| `SCUTTLEBOT_NICK` | auto-generated | Override the session nick entirely |
| `SCUTTLEBOT_SESSION_ID` | auto-generated | Override the session ID suffix |
| `SCUTTLEBOT_HOOKS_ENABLED` | `1` | Set to `0` to disable the relay without uninstalling |
| `SCUTTLEBOT_INTERRUPT_ON_MESSAGE` | `1` | Send Ctrl+C before injecting when agent appears busy |
| `SCUTTLEBOT_POLL_INTERVAL` | `2s` | How often to poll IRC for new messages |
| `SCUTTLEBOT_PRESENCE_HEARTBEAT` | `60s` | How often to send a presence touch (HTTP transport). Set to `0` to disable |
| `SCUTTLEBOT_MIRROR_REASONING` | `0` | Set to `1` to include thinking/reasoning blocks in IRC output, prefixed with `💭`. Off by default. Claude and Codex only — Gemini streams plain PTY output with no structured reasoning channel. |
| `SCUTTLEBOT_ACTIVITY_VIA_BROKER` | set by broker | Tells hook scripts to stay silent when the broker is posting. Do not set manually |

---

## IRC transport vs HTTP transport

**HTTP transport** (`SCUTTLEBOT_TRANSPORT=http`)

The broker posts to and reads from the scuttlebot HTTP API (`/v1/channels/{channel}/messages`). The session nick does not appear as a real IRC user. Presence is maintained via periodic touch calls. This is the default for Codex and Gemini.

**IRC transport** (`SCUTTLEBOT_TRANSPORT=irc`)

The broker registers the session nick with scuttlebot and opens a real IRC connection. The nick appears in the channel user list and receives native IRC presence. Operators see the nick join and part. This is the default for Claude Code.

To switch Claude Code to HTTP transport:

```bash
# ~/.config/scuttlebot-relay.env
SCUTTLEBOT_TRANSPORT=http
```

To switch Gemini or Codex to IRC transport with a remote server:

```bash
SCUTTLEBOT_TRANSPORT=irc
SCUTTLEBOT_IRC_ADDR=scuttlebot.example.com:6667
```

---

## Hooks as fallback

When the broker is running and the relay is active, it sets `SCUTTLEBOT_ACTIVITY_VIA_BROKER=1` in the Claude/Codex/Gemini environment. The hook scripts (`scuttlebot-post.sh`, `scuttlebot-check.sh`) check this variable and skip posting if it is set, preventing double-posting to the channel.

If the relay fails to connect (no token, network error), the variable is not set and the hooks continue to post normally. The agent session is not affected either way.

To run a session with hooks only and no broker:

```bash
SCUTTLEBOT_HOOKS_ENABLED=0 ~/.local/bin/claude-relay
```

---

## Troubleshooting

### Relay disabled: no token

```
claude-relay: relay disabled: sessionrelay: token is required for HTTP transport
```

`SCUTTLEBOT_TOKEN` is not set. Add it to `~/.config/scuttlebot-relay.env`:

```bash
SCUTTLEBOT_TOKEN=your-token-here
```

Get the current token from the running daemon:

```bash
./run.sh token
```

### Nick collision on IRC transport

If the broker exits uncleanly and `SCUTTLEBOT_IRC_DELETE_ON_CLOSE=true` did not fire, the old nick registration may still exist. Either wait for the NickServ account to expire, or delete it manually:

```bash
scuttlectl agent delete claude-myrepo-a1b2c3d4
```

Then relaunch the relay. It will register a new session nick with a different session ID suffix.

### Session file not found

```
claude-relay: relay disabled: context deadline exceeded
```

The broker waited 20 seconds for a matching session JSONL file and gave up. This happens when:

- Claude Code is run with `--help`, `--version`, or a command that doesn't start a real session (`help`, `completion`). The relay does not mirror these — this is expected behaviour.
- The `~/.claude/projects/` directory path does not match the working directory. Verify with `pwd` and check that `~/.claude/projects/` contains a directory named after your sanitized path.
- The session file is being written to a different directory (non-default Claude config). Set `CLAUDE_HOME` or `XDG_CONFIG_HOME` consistently.

### Messages not being injected

Check that your IRC message actually mentions the session nick with a word boundary. The relay uses a strict word-boundary match. `hello claude-myrepo-a1b2c3d4` works. `hello claude-myrepo-a1b2c3d4!` does not (trailing `!`). Address with a colon or comma:

```
claude-myrepo-a1b2c3d4: please stop and re-read the spec
claude-myrepo-a1b2c3d4, wrong file — check policies.go
```
