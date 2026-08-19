import { test, expect } from '@playwright/test';
import { readFile } from 'node:fs/promises';

test('sender can create a receiver link', async ({ page }) => {
  page.on('console', msg => console.log('SENDER:', msg.text()));
  const original = Buffer.from('real local transfer payload');
  await page.goto('/');
  await page.setInputFiles('input[type="file"]', { name: 'sample.txt', mimeType: 'text/plain', buffer: original });
  await page.getByRole('button', { name: /Create transfer link/ }).click();
  await expect(page.getByText('YOUR LINK')).toBeVisible();
  const receiverURL = await page.locator('.linkbox').evaluate(element => element.firstChild?.textContent?.trim() ?? '');
  expect(receiverURL).toContain('/receive/');
  const receiver = await page.context().newPage();
  receiver.on('console', msg => console.log('RECEIVER:', msg.text()));
  await receiver.addInitScript(() => { Object.defineProperty(window, 'showSaveFilePicker', { value: undefined, configurable: true }); });
  await receiver.goto(receiverURL);
  const downloadPromise = receiver.waitForEvent('download');
  await receiver.getByRole('button', { name: 'Download file' }).click();
  const download = await downloadPromise;
  const downloaded = await readFile((await download.path())!);
  expect(downloaded.equals(original)).toBe(true);
  await expect(page.getByText('Transfer complete')).toBeVisible({ timeout: 15_000 });
});
