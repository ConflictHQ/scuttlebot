import { test, expect } from '@playwright/test';

const BASE = process.env.SB_BASE_URL || 'http://localhost:8080';
const TOKEN = process.env.SB_TOKEN || '';

function headers() {
  return { Authorization: `Bearer ${TOKEN}`, 'Content-Type': 'application/json' };
}

test.beforeEach(() => {
  test.skip(!TOKEN, 'SB_TOKEN not set');
});

// --- Status ---

test('GET /v1/status returns ok', async ({ request }) => {
  const res = await request.get(`${BASE}/v1/status`, { headers: headers() });
  expect(res.ok()).toBe(true);
  const data = await res.json();
  expect(data.status).toBe('ok');
  expect(data.uptime).toBeTruthy();
});

test('GET /v1/metrics returns runtime stats', async ({ request }) => {
  const res = await request.get(`${BASE}/v1/metrics`, { headers: headers() });
  expect(res.ok()).toBe(true);
  const data = await res.json();
  expect(data.runtime).toBeTruthy();
  expect(data.runtime.goroutines).toBeGreaterThan(0);
});

// --- Agents ---

test('GET /v1/agents returns agent list', async ({ request }) => {
  const res = await request.get(`${BASE}/v1/agents`, { headers: headers() });
  expect(res.ok()).toBe(true);
  const data = await res.json();
  expect(Array.isArray(data.agents)).toBe(true);
});

test('POST /v1/agents/register creates agent and returns credentials', async ({ request }) => {
  const nick = `e2e-api-${Date.now()}`;
  const res = await request.post(`${BASE}/v1/agents/register`, {
    headers: headers(),
    data: { nick, type: 'worker', channels: ['#e2e-test'] },
  });
  expect(res.status()).toBe(201);
  const data = await res.json();
  expect(data.credentials.nick).toBe(nick);
  expect(data.credentials.passphrase).toBeTruthy();

  // Clean up.
  await request.delete(`${BASE}/v1/agents/${nick}`, { headers: headers() });
});

test('POST /v1/agents/register with skills stores them', async ({ request }) => {
  const nick = `e2e-skills-${Date.now()}`;
  const res = await request.post(`${BASE}/v1/agents/register`, {
    headers: headers(),
    data: { nick, type: 'worker', channels: ['#e2e-test'], skills: ['go', 'python'] },
  });
  expect(res.status()).toBe(201);

  const agent = await (await request.get(`${BASE}/v1/agents/${nick}`, { headers: headers() })).json();
  expect(agent.skills).toContain('go');
  expect(agent.skills).toContain('python');

  await request.delete(`${BASE}/v1/agents/${nick}`, { headers: headers() });
});

test('GET /v1/agents?skill= filters by capability', async ({ request }) => {
  const nick = `e2e-skill-${Date.now()}`;
  await request.post(`${BASE}/v1/agents/register`, {
    headers: headers(),
    data: { nick, type: 'worker', channels: ['#e2e-test'], skills: ['rare-skill-xyz'] },
  });

  const res = await request.get(`${BASE}/v1/agents?skill=rare-skill-xyz`, { headers: headers() });
  const data = await res.json();
  expect(data.agents.some((a: any) => a.nick === nick)).toBe(true);

  await request.delete(`${BASE}/v1/agents/${nick}`, { headers: headers() });
});

test('POST /v1/agents/bulk-delete removes multiple agents', async ({ request }) => {
  const nicks = [`e2e-bulk-${Date.now()}-a`, `e2e-bulk-${Date.now()}-b`];
  for (const nick of nicks) {
    await request.post(`${BASE}/v1/agents/register`, {
      headers: headers(),
      data: { nick, type: 'worker', channels: ['#e2e-test'] },
    });
  }

  const res = await request.post(`${BASE}/v1/agents/bulk-delete`, {
    headers: headers(),
    data: { nicks },
  });
  expect(res.ok()).toBe(true);
  const data = await res.json();
  expect(data.deleted).toBe(2);
});

// --- Channels ---

test('GET /v1/channels returns channel list', async ({ request }) => {
  const res = await request.get(`${BASE}/v1/channels`, { headers: headers() });
  expect(res.ok()).toBe(true);
  const data = await res.json();
  expect(Array.isArray(data.channels)).toBe(true);
});

test('POST /v1/channels/{ch}/messages sends message', async ({ request }) => {
  const res = await request.post(`${BASE}/v1/channels/general/messages`, {
    headers: headers(),
    data: { text: `e2e test message ${Date.now()}`, nick: 'e2e-test' },
  });
  expect(res.status()).toBeLessThan(300);
});

test('GET /v1/channels/{ch}/users returns user info with modes', async ({ request }) => {
  const res = await request.get(`${BASE}/v1/channels/general/users`, { headers: headers() });
  expect(res.ok()).toBe(true);
  const data = await res.json();
  expect(Array.isArray(data.users)).toBe(true);
  expect(typeof data.channel_modes).toBe('string');
});

// --- Settings ---

test('GET /v1/settings returns policies and bot_commands', async ({ request }) => {
  const res = await request.get(`${BASE}/v1/settings`, { headers: headers() });
  expect(res.ok()).toBe(true);
  const data = await res.json();
  expect(data.policies).toBeTruthy();
  expect(data.bot_commands).toBeTruthy();
  expect(data.bot_commands.oracle).toBeTruthy();
  expect(data.bot_commands.shepherd).toBeTruthy();
});

// --- Topology ---

test('GET /v1/topology returns types and static channels', async ({ request }) => {
  const res = await request.get(`${BASE}/v1/topology`, { headers: headers() });
  expect(res.ok()).toBe(true);
  const data = await res.json();
  expect(Array.isArray(data.static_channels)).toBe(true);
  expect(Array.isArray(data.types)).toBe(true);
});

// --- Config ---

test('GET /v1/config returns server config', async ({ request }) => {
  const res = await request.get(`${BASE}/v1/config`, { headers: headers() });
  expect(res.ok()).toBe(true);
  const data = await res.json();
  expect(data.ergo).toBeTruthy();
  expect(data.bridge).toBeTruthy();
});

// --- API keys ---

test('GET /v1/api-keys returns key list', async ({ request }) => {
  const res = await request.get(`${BASE}/v1/api-keys`, { headers: headers() });
  expect(res.ok()).toBe(true);
  const data = await res.json();
  expect(Array.isArray(data)).toBe(true);
  expect(data.length).toBeGreaterThan(0); // at least the server key
});

test('POST /v1/api-keys creates key and DELETE revokes it', async ({ request }) => {
  const create = await request.post(`${BASE}/v1/api-keys`, {
    headers: headers(),
    data: { name: `e2e-key-${Date.now()}`, scopes: ['read'] },
  });
  expect(create.status()).toBe(201);
  const key = await create.json();
  expect(key.token).toBeTruthy();
  expect(key.id).toBeTruthy();

  // Revoke.
  const revoke = await request.delete(`${BASE}/v1/api-keys/${key.id}`, { headers: headers() });
  expect(revoke.status()).toBe(204);
});

// --- Instructions ---

test('PUT/GET/DELETE /v1/channels/{ch}/instructions round-trip', async ({ request }) => {
  const ch = 'e2e-instr';
  // Set.
  const put = await request.put(`${BASE}/v1/channels/${ch}/instructions`, {
    headers: headers(),
    data: { instructions: 'Welcome {nick} to {channel}!' },
  });
  expect(put.status()).toBe(204);

  // Get.
  const get = await request.get(`${BASE}/v1/channels/${ch}/instructions`, { headers: headers() });
  const data = await get.json();
  expect(data.instructions).toContain('Welcome {nick}');

  // Delete.
  const del = await request.delete(`${BASE}/v1/channels/${ch}/instructions`, { headers: headers() });
  expect(del.status()).toBe(204);
});

// --- Auth scoping ---

test('read-only key cannot create agents', async ({ request }) => {
  // Create a read-only key.
  const create = await request.post(`${BASE}/v1/api-keys`, {
    headers: headers(),
    data: { name: `e2e-readonly-${Date.now()}`, scopes: ['read'] },
  });
  const key = await create.json();

  // Try to register an agent with it.
  const res = await request.post(`${BASE}/v1/agents/register`, {
    headers: { Authorization: `Bearer ${key.token}`, 'Content-Type': 'application/json' },
    data: { nick: 'should-fail', type: 'worker' },
  });
  expect(res.status()).toBe(403);

  // Clean up.
  await request.delete(`${BASE}/v1/api-keys/${key.id}`, { headers: headers() });
});

// --- Blocker escalation ---

test('POST /v1/agents/{nick}/blocker accepts alert', async ({ request }) => {
  const res = await request.post(`${BASE}/v1/agents/test-agent/blocker`, {
    headers: headers(),
    data: { message: 'stuck on database migration', channel: '#test' },
  });
  expect(res.status()).toBe(204);
});
