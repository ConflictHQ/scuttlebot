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

## The swappability principle

scuttlebot's JSON message envelope and SDK abstraction are intentionally transport-agnostic. IRC is the default and the right choice for the target use case (private networks, 100s–1000s of agents, human observability required). The architecture does not preclude future transport backends.
