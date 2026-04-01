import { test, expect } from '@playwright/test';
import { TOKEN, USERNAME, PASSWORD, setToken, login, waitForStatus } from './helpers';

test('root redirects to /ui/', async ({ page }) => {
  await page.goto('/');
  expect(page.url()).toContain('/ui/');
});

test('ui without auth shows login screen', async ({ page }) => {
  await page.goto('/ui/');
  await page.evaluate(() => {
    localStorage.removeItem('sb_token');
    localStorage.removeItem('sb_username');
  });
  await page.reload();
  await expect(page.locator('#login-screen')).toBeVisible();
});

test('login screen has username, password, and submit button', async ({ page }) => {
  await page.goto('/ui/');
  await page.evaluate(() => localStorage.removeItem('sb_token'));
  await page.reload();
  await expect(page.locator('#login-username')).toBeVisible();
  await expect(page.locator('#login-password')).toBeVisible();
  await expect(page.locator('#login-btn')).toBeVisible();
});

test('login screen has "use API token instead" fallback', async ({ page }) => {
  await page.goto('/ui/');
  await page.evaluate(() => localStorage.removeItem('sb_token'));
  await page.reload();
  await expect(page.locator('summary:has-text("use API token instead")')).toBeVisible();
});

test('setting token via fallback hides login screen', async ({ page }) => {
  test.skip(!TOKEN, 'SB_TOKEN not set');
  await page.goto('/ui/');
  await page.evaluate(() => localStorage.removeItem('sb_token'));
  await page.reload();
  await page.locator('summary:has-text("use API token instead")').click();
  await page.fill('#token-login-input', TOKEN);
  await page.click('button:has-text("apply")');
  await expect(page.locator('#login-screen')).toBeHidden({ timeout: 5000 });
  await waitForStatus(page);
});

test('valid token via setToken bypasses login screen and loads status', async ({ page }) => {
  test.skip(!TOKEN, 'SB_TOKEN not set');
  await setToken(page, TOKEN);
  await expect(page.locator('#login-screen')).toBeHidden();
  await waitForStatus(page);
  await expect(page.locator('#stat-status')).toContainText('ok');
});

test('login with valid credentials hides login screen and loads status', async ({ page }) => {
  test.skip(!PASSWORD, 'SB_PASSWORD not set');
  await login(page, USERNAME, PASSWORD);
  await expect(page.locator('#login-screen')).toBeHidden();
  await waitForStatus(page);
});

test('login with wrong password shows error message', async ({ page }) => {
  test.skip(!PASSWORD, 'SB_PASSWORD not set');
  await page.goto('/ui/');
  await page.evaluate(() => localStorage.removeItem('sb_token'));
  await page.reload();
  await page.locator('#login-screen').waitFor({ state: 'visible' });
  await page.fill('#login-username', USERNAME);
  await page.fill('#login-password', 'definitely-wrong-xyz');
  await page.click('#login-btn');
  await expect(page.locator('#login-error')).toBeVisible({ timeout: 5000 });
  await expect(page.locator('#login-screen')).toBeVisible();
});

test('header shows user display after auth', async ({ page }) => {
  test.skip(!TOKEN, 'SB_TOKEN not set');
  await setToken(page, TOKEN);
  await expect(page.locator('#header-user-display')).toBeVisible();
});

test('sign out clears session and shows login screen', async ({ page }) => {
  test.skip(!TOKEN, 'SB_TOKEN not set');
  await setToken(page, TOKEN);
  await page.click('button[onclick="logout()"]');
  await page.locator('#login-screen').waitFor({ state: 'visible', timeout: 5000 });
});

test('invalid token triggers 401 and shows login screen', async ({ page }) => {
  await setToken(page, 'invalid-token-xyz');
  // 401 from any API call → login screen shown.
  await expect(page.locator('#login-screen')).toBeVisible({ timeout: 8000 });
});
