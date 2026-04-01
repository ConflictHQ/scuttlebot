#!/usr/bin/env node
// Minimal OpenAI + scuttlebot relay example (Node 18+).
// Requires env: SCUTTLEBOT_URL, SCUTTLEBOT_TOKEN, SCUTTLEBOT_CHANNEL.
// Optional: SCUTTLEBOT_NICK, SCUTTLEBOT_SESSION_ID, OPENAI_API_KEY.

import OpenAI from "openai";
import path from "node:path";

const prompt = process.argv[2] || "Hello from openai-relay";
const sanitize = (value) => value.replace(/[^A-Za-z0-9_-]+/g, "-").replace(/^-+|-+$/g, "");
const baseName = sanitize(path.basename(process.cwd()) || "repo");
const sessionSuffix = sanitize(
  process.env.SCUTTLEBOT_SESSION_ID || process.env.CODEX_SESSION_ID || String(process.ppid || process.pid)
) || "session";

const cfg = {
  url: process.env.SCUTTLEBOT_URL,
  token: process.env.SCUTTLEBOT_TOKEN,
  channel: (process.env.SCUTTLEBOT_CHANNEL || "general").replace(/^#/, ""),
  nick: process.env.SCUTTLEBOT_NICK || `codex-${baseName}-${sessionSuffix}`,
  model: process.env.OPENAI_MODEL || "gpt-4.1-mini",
  backend: process.env.SCUTTLEBOT_LLM_BACKEND || "openai", // default to daemon-stored openai
};

for (const [k, v] of Object.entries(cfg)) {
  if (["backend", "model"].includes(k)) continue;
  if (!v) {
    console.error(`missing env: ${k.toUpperCase()}`);
    process.exit(1);
  }
}
const useBackend = !!cfg.backend;
if (!useBackend && !process.env.OPENAI_API_KEY) {
  console.error("missing env: OPENAI_API_KEY (or set SCUTTLEBOT_LLM_BACKEND to use server-side key)");
  process.exit(1);
}

const openai = useBackend ? null : new OpenAI({ apiKey: process.env.OPENAI_API_KEY });
let lastCheck = 0;

function mentionsNick(text) {
  const escaped = cfg.nick.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`(^|[^A-Za-z0-9_./\\\\-])${escaped}($|[^A-Za-z0-9_./\\\\-])`, "i").test(text);
}

async function relayPost(text) {
  const res = await fetch(`${cfg.url}/v1/channels/${cfg.channel}/messages`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${cfg.token}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ text, nick: cfg.nick }),
  });
  if (!res.ok) {
    throw new Error(`relay post failed: ${res.status} ${res.statusText}`);
  }
}

async function relayPoll() {
  const res = await fetch(`${cfg.url}/v1/channels/${cfg.channel}/messages`, {
    headers: { Authorization: `Bearer ${cfg.token}` },
  });
  if (!res.ok) {
    throw new Error(`relay poll failed: ${res.status} ${res.statusText}`);
  }
  const data = await res.json();
  const now = Date.now() / 1000;
  const bots = new Set([
    cfg.nick,
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
  ]);
  const msgs =
    data.messages?.filter(
      (m) =>
        !bots.has(m.nick) &&
        !m.nick.startsWith("claude-") &&
        !m.nick.startsWith("codex-") &&
        !m.nick.startsWith("gemini-") &&
        Date.parse(m.at) / 1000 > lastCheck &&
        mentionsNick(m.text)
    ) || [];
  lastCheck = now;
  return msgs;
}

async function main() {
  await relayPost(`starting: ${prompt}`);

  let reply;
  if (useBackend) {
    const res = await fetch(`${cfg.url}/v1/llm/complete`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${cfg.token}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ backend: cfg.backend, prompt }),
    });
    if (!res.ok) throw new Error(`llm complete failed: ${res.status} ${res.statusText}`);
    const body = await res.json();
    reply = body.text;
  } else {
    const completion = await openai.chat.completions.create({
      model: cfg.model,
      messages: [{ role: "user", content: prompt }],
    });
    reply = completion.choices[0].message.content;
  }
  console.log(`OpenAI: ${reply}`);

  await relayPost(`OpenAI reply: ${reply}`);

  const instructions = await relayPoll();
  instructions.forEach((m) => console.log(`[IRC] ${m.nick}: ${m.text}`));
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
