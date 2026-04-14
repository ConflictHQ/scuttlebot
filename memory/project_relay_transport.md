---
name: Relay transport must be HTTP not IRC
description: SCUTTLEBOT_TRANSPORT must be set to http for relays to receive web-UI messages; irc transport silently drops them due to Ergo RELAYMSG delivery bug
type: project
---

`~/.config/scuttlebot-relay.env` must have `SCUTTLEBOT_TRANSPORT=http`.

**Why:** The bridge uses `draft/relaymsg` RELAYMSG for web-UI messages. Ergo v2.18.0 does not deliver RELAYMSG to clients even when they have the cap ACKed — confirmed by exhaustive debugging (ALL_EVENTS showed zero events, relaymsg=true confirmed from CAP ACK). HTTP transport bypasses this entirely: the relay polls `/v1/channels/X/messages` which reads from the bridge's ring buffer, which the bridge populates via `dispatch()` before/after sending to IRC.

**How to apply:** If relays stop receiving web-UI messages, check `SCUTTLEBOT_TRANSPORT` first. It must be `http`. Also: `pkg/sessionrelay/irc.go` has a RELAYMSG handler (alongside PRIVMSG) for when IRC transport is used with working RELAYMSG delivery — this is correct code and should be kept.
