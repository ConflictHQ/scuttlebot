import { test, expect } from '@playwright/test';
import { TOKEN, setToken, waitForStatus } from './helpers';

test.beforeEach(async ({ page }) => {
  test.skip(!TOKEN, 'SB_TOKEN not set');
  await setToken(page, TOKEN);
  await waitForStatus(page);
});

test('agents tab loads and shows table header', async ({ page }) => {
  await page.click('#tab-agents');
  await expect(page.locator('#agents-container')).toBeVisible();
});

test('register agent drawer opens and closes', async ({ page }) => {
  await page.click('#tab-agents');
  await page.click('button:has-text("+ register agent")');
  await expect(page.locator('#register-drawer')).toHaveClass(/open/);
  await page.click('#register-drawer button:has-text("✕")');
  await expect(page.locator('#register-drawer')).not.toHaveClass(/open/);
});

test('register a new agent and see credentials', async ({ page }) => {
  const nick = `e2e-agent-${Date.now()}`;

  await page.click('#tab-agents');
  await page.click('button:has-text("+ register agent")');
  await page.fill('#reg-nick', nick);
  await page.selectOption('#reg-type', 'worker');
  await page.click('#register-drawer button[type="submit"]');

  // Credentials box should appear.
  await expect(page.locator('#register-result')).toBeVisible({ timeout: 8000 });
  await expect(page.locator('#register-result')).toContainText(nick);
  await expect(page.locator('#register-result')).toContainText('passphrase');

  // Agent should now appear in the table.
  await expect(page.locator('#agents-container')).toContainText(nick, { timeout: 5000 });
});

test('agent search filters the list', async ({ page }) => {
  await page.click('#tab-agents');
  await page.fill('#agent-search', 'zzz-no-match-xyz');
  await expect(page.locator('#agents-container')).toContainText('no agents match');
  await page.fill('#agent-search', '');
});
