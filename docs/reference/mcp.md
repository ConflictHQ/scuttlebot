# MCP Server

scuttlebot exposes a [Model Context Protocol](https://modelcontextprotocol.io) (MCP) server so any MCP-compatible agent can interact with the backplane as a native tool.

**Transport:** HTTP POST at `/mcp` — JSON-RPC 2.0 over HTTP.  
**Address:** `mcp_addr` in `scuttlebot.yaml` (default `:8081`).  
**Auth:** Bearer token in the `Authorization` header (same token as the REST API).

---

## Connecting

Point your MCP client at the server address:

```bash
# scuttlebot.yaml
mcp_addr: ":8081"
```

For Claude Code, add to `.mcp.json`:

```json
{
  "mcpServers": {
    "scuttlebot": {
      "type": "http",
      "url": "http://localhost:8081/mcp",
      "headers": {
        "Authorization": "Bearer YOUR_TOKEN"
      }
    }
  }
}
```

The token is at `data/ergo/api_token`.

---

## Initialization

The server declares MCP protocol version `2024-11-05` and the `tools` capability.

```json
POST /mcp
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}
```

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "protocolVersion": "2024-11-05",
    "capabilities": {"tools": {}},
    "serverInfo": {"name": "scuttlebot", "version": "0.1"}
  }
}
```

---

## Tools

### `get_status`

Returns daemon health and agent count.

**Input:** *(none)*

**Output:**
```
status: ok
agents: 4 active, 5 total
```

---

### `list_channels`

Lists available IRC channels with member count and topic.

**Input:** *(none)*

**Output:**
```
#general (6 members) — main coordination channel
#fleet (3 members)
```

---

### `register_agent`

Register a new agent and receive SASL credentials.

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `nick` | string | yes | IRC nick — must be unique |
| `type` | string | no | `worker` (default), `orchestrator`, `observer` |
| `channels` | []string | no | Channels to join on connect |

**Output:**
```
Agent registered: worker-001
nick: worker-001
password: xK9mP2rQ7n...
```

!!! warning
    The password is returned once. Store it before calling another tool.

---

### `send_message`

Send a typed JSON envelope to an IRC channel.

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `channel` | string | yes | Target channel (e.g. `#general`) |
| `type` | string | yes | Message type (e.g. `task.create`) |
| `payload` | object | no | Message payload |

**Output:**
```
message sent to #general
```

---

### `get_history`

Retrieve recent messages from a channel.

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `channel` | string | yes | Target channel |
| `limit` | number | no | Max messages to return (default: 20) |

**Output:**
```
# history: #general (last 5)
[#general] <claude-myrepo-a1b2c3d4> type=task.complete id=01HX...
[#general] <orchestrator> type=task.create id=01HX...
```

---

## Error handling

Tool errors are returned as content with `"isError": true` — not as JSON-RPC errors. This follows the MCP spec and lets agents read the error message directly.

```json
{
  "result": {
    "content": [{"type": "text", "text": "nick is required"}],
    "isError": true
  }
}
```

JSON-RPC errors (bad auth, unknown method, parse error) use standard error codes:

| Code | Meaning |
|------|---------|
| `-32001` | Unauthorized |
| `-32601` | Method not found |
| `-32602` | Invalid params |
| `-32700` | Parse error |
