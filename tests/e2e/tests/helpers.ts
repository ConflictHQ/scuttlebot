import { Page } from '@playwright/test';

export const TOKEN    = process.env.SB_TOKEN    || '';
export const USERNAME = process.env.SB_USERNAME || 'admin';
export const PASSWORD = process.env.SB_PASSWORD || '';

/**
 * Inject token directly into localStorage and reload.
 * Bypasses the login screen — useful when SB_TOKEN is available.
 */
export async function setToken(page: Page, token: string) {
  await page.goto('/ui/');
  await page.evaluate((t) => {
    localStorage.setItem('sb_token', t);
    localStorage.removeItem('sb_username');
  }, token);
  await page.reload();
}

/**
 * Drive the login screen with username + password.
 * Used when SB_USERNAME / SB_PASSWORD are set instead of SB_TOKEN.
 */
export async function login(page: Page, username: string, password: string) {
  await page.goto('/ui/');
  // Clear any existing token so the login screen appears.
  await page.evaluate(() => {
    localStorage.removeItem('sb_token');
    localStorage.removeItem('sb_username');
  });
  await page.reload();
  await page.locator('#login-screen').waitFor({ state: 'visible' });
  await page.fill('#login-username', username);
  await page.fill('#login-password', password);
  await page.click('#login-btn');
  // Wait for login screen to disappear.
  await page.locator('#login-screen').waitFor({ state: 'hidden', timeout: 8000 });
}

/** Wait for the status card to show "ok". */
export async function waitForStatus(page: Page) {
  await page.locator('#stat-status').waitFor({ state: 'visible' });
  await page.waitForFunction(
    () => document.getElementById('stat-status')?.textContent?.includes('ok'),
    { timeout: 8000 },
  );
}
