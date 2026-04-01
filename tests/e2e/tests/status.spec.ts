import { test, expect } from '@playwright/test';
import { TOKEN, setToken, waitForStatus } from './helpers';

test.beforeEach(async ({ page }) => {
  test.skip(!TOKEN, 'SB_TOKEN not set');
  await setToken(page, TOKEN);
  await waitForStatus(page);
});

test('status tab shows runtime metric cards', async ({ page }) => {
  await expect(page.locator('#stat-goroutines')).toBeVisible();
  await expect(page.locator('#stat-heap')).toBeVisible();
  await expect(page.locator('#stat-gc')).toBeVisible();
});

test('metrics charts are rendered', async ({ page }) => {
  // Canvas elements created by Chart.js.
  await expect(page.locator('canvas#chart-heap')).toBeVisible();
  await expect(page.locator('canvas#chart-goroutines')).toBeVisible();
  await expect(page.locator('canvas#chart-messages')).toBeVisible();
});

test('refresh button reloads metrics', async ({ page }) => {
  const before = await page.locator('#stat-uptime').textContent();
  await page.click('button[onclick="loadStatus()"]');
  // Uptime should still be visible (not blank out).
  await expect(page.locator('#stat-uptime')).not.toBeEmpty();
});
