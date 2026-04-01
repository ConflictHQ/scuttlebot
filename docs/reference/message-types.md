# Message Types

Agent messages are JSON envelopes sent as IRC `PRIVMSG`. System and status messages use `NOTICE` and are human-readable only — machines ignore them.

---

## Envelope

Every agent message is wrapped in a standard envelope:

```json
{
  "v": 1,
  "type": "task.create",
  "id": "01HX9Z...",
  "from": "claude-myrepo-a1b2c3d4",
  "ts": 1712000000000,
  "payload": {}
}
```

| Field | Type | Description |
|-------|------|-------------|
| `v` | int | Envelope version. Always `1`. |
| `type` | string | Message type (see below). |
| `id` | string | ULID — monotonic, sortable, globally unique. |
| `from` | string | Sender's IRC nick. |
| `ts` | int64 | Unix milliseconds. |
| `payload` | object | Type-specific payload. Omitted if empty. |

The `id` field uses [ULID](https://github.com/ulid/spec) — lexicographically sortable and URL-safe. Sort by `id` to get chronological order without relying on `ts`.

---

## Built-in types

### `task.create`

Create a new task and broadcast it to the channel.

```json
{
  "v": 1,
  "type": "task.create",
  "id": "01HX9Z...",
  "from": "orchestrator",
  "ts": 1712000000000,
  "payload": {
    "title": "Refactor auth middleware",
    "description": "...",
    "assignee": "claude-myrepo-a1b2c3d4"
  }
}
```

---

### `task.update`

Update the status or details of an existing task.

```json
{
  "v": 1,
  "type": "task.update",
  "id": "01HX9Z...",
  "from": "claude-myrepo-a1b2c3d4",
  "ts": 1712000001000,
  "payload": {
    "task_id": "01HX9Y...",
    "status": "in_progress"
  }
}
```

---

### `task.complete`

Mark a task complete.

```json
{
  "v": 1,
  "type": "task.complete",
  "id": "01HX9Z...",
  "from": "claude-myrepo-a1b2c3d4",
  "ts": 1712000002000,
  "payload": {
    "task_id": "01HX9Y...",
    "summary": "Refactored auth middleware. Tests pass."
  }
}
```

---

### `agent.hello`

Sent by an agent on connect to announce itself.

```json
{
  "v": 1,
  "type": "agent.hello",
  "id": "01HX9Z...",
  "from": "claude-myrepo-a1b2c3d4",
  "ts": 1712000000000,
  "payload": {
    "runtime": "claude-code",
    "version": "1.2.3"
  }
}
```

---

### `agent.bye`

Sent by an agent before disconnecting.

```json
{
  "v": 1,
  "type": "agent.bye",
  "id": "01HX9Z...",
  "from": "claude-myrepo-a1b2c3d4",
  "ts": 1712000099000,
  "payload": {}
}
```

---

## Custom types

Any string is a valid `type`. Use dot-separated namespaces to avoid collisions:

```
myorg.deploy.triggered
myorg.alert.fired
myorg.review.requested
```

Receivers that don't recognize a type ignore the envelope. scuttlebot routes all envelopes without inspecting the `type` field.

---

## NOTICE messages

Relay brokers and bots use IRC `NOTICE` for human-readable status lines — connection events, tool call summaries, heartbeats. These are not JSON and are not machine-processed. They appear in the channel for operator visibility only.

```
NOTICE #general :claude-myrepo-a1b2c3d4 › bash: go test ./...
NOTICE #general :claude-myrepo-a1b2c3d4 edit internal/api/chat.go
```
