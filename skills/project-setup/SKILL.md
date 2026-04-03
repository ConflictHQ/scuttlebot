---
name: project-setup
description: Wire any project repo for scuttlebot IRC coordination — creates .scuttlebot.yaml, adds gitignore entry, and documents the issue channel workflow in the project's bootstrap file. Use when onboarding a new project to the scuttlebot coordination backplane.
---

# Project Setup for Scuttlebot Coordination

Use this skill to wire a project repo into the scuttlebot coordination backplane.
This sets up per-project IRC channels and the issue-based workflow so agents
working in the repo automatically coordinate through scuttlebot.

## What this skill does

1. Creates `.scuttlebot.yaml` in the project root (gitignored)
2. Adds `.scuttlebot.yaml` to `.gitignore`
3. Adds an IRC coordination section to the project's bootstrap doc

## Channel hierarchy

Every project uses three channel tiers:

| Tier | Channel | Purpose | Lifecycle |
|------|---------|---------|-----------|
| General | `#general` | Cross-project coordination, operator chatter | Always joined |
| Project | `#<project-name>` | Project-specific coordination, status, discussion | Joined at relay startup via `.scuttlebot.yaml` |
| Issue | `#issue-<N>` | Solo work channel for a specific GitHub issue | `/join` when starting, `/part` when done |

## Per-repo config: `.scuttlebot.yaml`

Created in the project root, gitignored. The relay reads this at startup and
merges its channels into the session channel set.

```yaml
# .scuttlebot.yaml — per-repo scuttlebot relay config (gitignored)
channel: <project-name>
```

That's it. One field. The relay handles the rest.

Optional additional channels:

```yaml
channel: <project-name>
channels:
  - ops
  - deployments
```

## Issue channel workflow

When an agent picks up a GitHub issue, it should:

1. `/join #issue-<N>` — join the issue channel (auto-created if it doesn't exist)
2. Work in that channel — all activity mirrors there
3. `/part #issue-<N>` — leave when the issue is closed or work is complete

This gives operators per-issue observability. Multiple agents on different issues
work in isolation. An operator can watch `#kohakku` for project-level activity or
drill into `#issue-42` for a specific task.

## Bootstrap doc section

Add this to the project's `bootstrap.md` (or equivalent conventions doc):

```markdown
## IRC Coordination

This project uses scuttlebot for agent coordination via IRC.

### Channels

- `#general` — cross-project coordination (always joined)
- `#<project-name>` — project coordination (auto-joined via `.scuttlebot.yaml`)
- `#issue-<N>` — per-issue work channel (join/part dynamically)

### Issue workflow

When you start working on a GitHub issue:

1. Join the issue channel: send `/join #issue-<N>` where N is the issue number
2. Do your work — activity is mirrored to both the project channel and the issue channel
3. When done, part the issue channel: send `/part #issue-<N>`

### Setup

The `.scuttlebot.yaml` file in the project root configures the relay to auto-join
the project channel. This file is gitignored — each developer/agent creates their
own. The relay config at `~/.config/scuttlebot-relay.env` provides the server
URL, token, and transport settings.
```

## Step-by-step setup

### For a new project

Given a project named `myproject` in a repo at `/path/to/myproject`:

1. Create `.scuttlebot.yaml`:
   ```yaml
   channel: myproject
   ```

2. Add to `.gitignore`:
   ```
   .scuttlebot.yaml
   ```

3. Add the IRC Coordination section to the project's bootstrap doc.

4. Start a relay from the project directory — it will auto-join `#general` and `#myproject`.

### For an existing project

Same steps. The relay picks up `.scuttlebot.yaml` on next startup. No server-side
config needed — channels are created on demand by Ergo when the first user joins.

## What NOT to put in `.scuttlebot.yaml`

- Tokens or credentials (use `~/.config/scuttlebot-relay.env`)
- Server URL (use the global relay config)
- Transport settings (use the global relay config)

The per-repo file is only for channel routing. Everything else is global.
