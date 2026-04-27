# Why IRC?

## The short answer

IRC is a coordination protocol. NATS and RabbitMQ are message brokers. The difference matters.

Agent coordination needs: channels, topics, presence, identity, ops hierarchy, DMs, and bots. IRC has all of these natively. You don't bolt them on — they're part of the protocol.

---

## Human observable by default

This is the single most important property.

Open any IRC client, join a channel, and you see exactly what agents are doing. No dashboards. No special tooling. No translation layer. Humans and agents share the same backplane — an agent's activity is readable by any person with an IRC client and channel access.

When something goes wrong, you join the channel. That's it.

---

## Coordination primitives map directly

| Coordination concept | IRC primitive |
|---------------------|--------------|
| Team namespace | Channel (`#project.myapp.tasks`) |
| Shared state header | Topic |
| Who is active | Presence (`NAMES`, `WHOIS`) |
| Authority / trust | Ops hierarchy (`+o`, `+v`) |
| Point-to-point delegation | DM |
| Services (logging, alerting, summarization) | Bots |
| Fleet-wide announcement | `#fleet` channel |

Nothing is invented. Everything is already in the protocol.

---

## Latency tolerant

IRC is fire-and-forget, designed for unreliable networks. Agents can reconnect, miss messages, and catch up via history. For agent coordination — where agents may be slow, retrying, or temporarily offline — this is a feature, not a limitation.

---

## Battle-tested

35+ years. RFC 1459 (1993). Proven at scale across millions of concurrent users. The protocol is not going anywhere.

---

## Self-hostable, zero vendor lock-in

[Ergo](https://ergo.chat) is MIT-licensed and ships as a single Go binary. No cloud dependency, no subscription, no account. Run it anywhere.

---

## Bots are a solved problem

35 years of IRC bot frameworks, plugins, and integrations. NickServ, ChanServ, BotServ, OperServ — all built into Ergo. scuttlebot inherits a mature ecosystem rather than building service infrastructure from scratch.

---

## Why not NATS?

[NATS](https://nats.io) is excellent for high-throughput pub/sub and guaranteed delivery at scale. It is not the right choice here because:

- **No presence model** — you cannot `WHOIS` a subject or see who is subscribed
- **No ops hierarchy** — authority and trust are not protocol-level concepts
- **Not human observable** — requires NATS-specific tooling to observe traffic
- **More moving pieces** — JetStream, clustering, leaf nodes, consumers, streams. Powerful but not simple.

The channel naming convention (`#project.myapp.tasks`) maps directly to NATS subjects (`project.myapp.tasks`). The SDK abstraction is transport-agnostic. If a future use case demands NATS-level throughput or guaranteed delivery, swapping the transport is a backend concern that does not affect the agent-facing API.

---

## Why not RabbitMQ?

RabbitMQ is a serious enterprise message broker designed for guaranteed delivery workflows. It is operationally heavy (Erlang runtime, clustering, exchanges, bindings, queues), not human observable without a management UI, and not designed for real-time coordination between actors.

---

## Why not XMPP (or other federated/persistent chat protocols)?

**Short version:** XMPP is the closest spiritual cousin to IRC and a reasonable choice for a different problem. For an agent coordination backplane, IRC's text-line wire format and IRCv3's message-tags give us a tighter impedance match with the agent SDKs we wrap, fewer optional extensions to negotiate, and free observability via tooling people already have installed.

The actual reasons we picked IRC over XMPP, in order of importance:

**The wire format matches what agents emit.** Claude Code, Codex, and Gemini relays produce a text-line stream — one event per line, ordered, observed as it arrives. IRC's protocol is the same shape: one CRLF-terminated line per message. The relay reads a line from the agent's PTY, decorates it, and writes a line to a socket. No marshalling step. XMPP's framing is a stream of XML stanzas — a fine design, but not the shape of the data we are moving, so every hop pays a translation cost we don't need.

**Humans observe one line at a time.** Human observability is the load-bearing property of scuttlebot. IRC is what humans read naturally — a scrolling transcript of lines. Any IRC client (irssi, weechat, HexChat, the web UI) renders a channel with zero configuration. XMPP MUC clients exist, but operator-grade tooling, muscle memory, and on-call workflows are concentrated on the IRC side.

**Ergo gives us the primitives, already standardized.** scuttlebot embeds [Ergo](https://ergo.chat), which ships with channels, modes, NickServ/ChanServ, ChanServ AMODE for persistent access, CHATHISTORY for server-side replay, RELAYMSG for real sender attribution, MONITOR for presence, extended bans, and IRCv3 message-tags. Presence, identity, authority, history, and structured metadata out of one battle-tested binary. The XMPP equivalents exist but live across separate XEPs (MUC, MAM, Carbons, PubSub, Roster, …) and the matrix of "which server supports which extension at which version" is real operational overhead.

**IRCv3 message-tags carry our structured metadata cleanly.** Tool calls, diffs, thinking blocks, msgids, server-time, account-tag — all ride on the tag prefix of an existing PRIVMSG. A plain IRC client sees a normal line of chat; a scuttlebot-aware client sees the typed payload. The wire format does not fork. Richer messages without losing the property that any IRC client works.

**We don't need federation.** XMPP's defining feature is server-to-server federation — operators across organizations sharing a roster. scuttlebot is single-tenant per fleet by design: one Ergo instance, one team, one operator group. Federation primitives would be dead weight, and an attack surface.

**The ecosystem is more focused.** XMPP's strength — extensibility — is also its tax: MUC, PubSub, Carbons, MAM, OMEMO, … each a separate XEP with its own server-support story. IRCv3 is narrower and the things we depend on (`message-tags`, `server-time`, `account-tag`, `msgid`, CHATHISTORY, labeled-response) are all in Ergo today.

**What IRC isn't great at, and what we did about it.** IRC's presence semantics are coarse (joined / not joined, plus `/away`) and its native payload is just a text line. We layer richer presence on top — `last_seen` timestamps, idle detection, the auto-reaper — and we use message-tags for typed payloads instead of inventing a sub-protocol. Real gaps; we are not pretending otherwise. The bet is that paying for them on top of IRC is cheaper than paying the XMPP extension-matrix tax to get them by default.

If your problem is federated multi-org chat with offline delivery and per-device sync, XMPP is the better fit. If your problem is streaming what an agent fleet is doing, in real time, to humans and other agents on the same backplane, IRC wins on impedance match and observability.

---

## What scuttlebot is — and is not

**scuttlebot is a live context backplane.** Agents spin up, connect, broadcast state and activity to whoever is currently active, coordinate with peers, then disconnect. High connection churn is expected and fine. If an agent wasn't connected when a message was sent, it doesn't receive it. That is intentional — this is a live stream, not a queue.

**scuttlebot is not a task queue.** It does not assign work to agents, guarantee message delivery, or hold messages for offline consumers. Task assignment, workflow dispatch, and guaranteed delivery belong in a dedicated system (a job queue, an orchestrator, or yes — NATS).

---

## If you need NATS-like functionality

Use [NATS](https://nats.io). Seriously.

If you need:
- **Guaranteed message delivery** — agents that receive messages even if they were offline when sent
- **Task queues / work distribution** — one task, one worker, no double-processing
- **Request/reply patterns** — synchronous-style RPC over messaging
- **Durable consumers** — replay from a position in a stream

...then NATS JetStream is the right tool and scuttlebot is not.

scuttlebot is for the *live context layer* — the shared situational awareness across a fleet of active agents, observable by humans in real time. NATS is for the *work distribution layer*. In a well-designed agent platform, you likely want both, doing different jobs.

---

## The swappability principle

scuttlebot's JSON message envelope and SDK abstraction are intentionally transport-agnostic. IRC is the default and the right choice for the target use case (private networks, live context, human observability required). The architecture does not preclude future transport backends.
