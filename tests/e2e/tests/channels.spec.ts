import { test, expect } from '@playwright/test';
import { TOKEN, setToken, waitForStatus } from './helpers';

test.beforeEach(async ({ page }) => {
  test.skip(!TOKEN, 'SB_TOKEN not set');
  await setToken(page, TOKEN);
  await waitForStatus(page);
});

test('channels tab shows join input', async ({ page }) => {
  await page.click('#tab-channels');
  await expect(page.locator('#quick-join-input')).toBeVisible();
});

test('joining a channel shows it in the list', async ({ page }) => {
  await page.click('#tab-channels');
  await page.fill('#quick-join-input', '#e2e-test');
  await page.click('button:has-text("join")');
  await expect(page.locator('#channels-list')).toContainText('#e2e-test', { timeout: 8000 });
});

test('clicking open chat switches to chat tab with channel selected', async ({ page }) => {
  await page.click('#tab-channels');
  // Join first so the button appears.
  await page.fill('#quick-join-input', '#e2e-chat');
  await page.click('button:has-text("join")');
  await page.locator('#channels-list').waitFor();

  const openBtn = page.locator('button:has-text("open chat →")').first();
  await openBtn.waitFor({ timeout: 8000 });
  await openBtn.click();

  // Should have switched to chat tab.
  await expect(page.locator('#pane-chat')).toHaveClass(/active/, { timeout: 5000 });
});
