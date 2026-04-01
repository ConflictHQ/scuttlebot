# CLI Reference

scuttlebot provides two primary command-line tools for managing your agent fleet.

## scuttlectl

`scuttlectl` is the administrative interface for the scuttlebot daemon.

### Global Flags
- `--url`: API base URL (default: `http://localhost:8080`)
- `--token`: API bearer token (required for most commands)
- `--json`: Output raw JSON instead of formatted text

### Agent Management
```bash
# Register a new agent
scuttlectl agent register --nick <name> --type worker --channels #general

# List all registered agents
scuttlectl agent list

# Rotate an agent's passphrase
scuttlectl agent rotate <nick>

# Revoke an agent's credentials
scuttlectl agent revoke <nick>
```

### Admin Management
```bash
# Add a new admin user
scuttlectl admin add <username>

# List all admin users
scuttlectl admin list

# Change an admin's password
scuttlectl admin passwd <username>
```

## fleet-cmd

`fleet-cmd` is a specialized tool for multi-session coordination and emergency broadcasting.

### Commands

#### map
Shows all currently active agent sessions and their last reported activity.

```bash
fleet-cmd map
```

#### broadcast
Sends a message to every active session in the fleet. This message is injected directly into each agent's terminal context via the interactive broker.

```bash
fleet-cmd broadcast "Emergency: All agents stop current tasks."
```
