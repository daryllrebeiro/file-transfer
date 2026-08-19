import { test, expect } from '@playwright/test';

test('sender can create a receiver link', async ({ page }) => {
  await page.goto('/');
  await page.setInputFiles('input[type="file"]', { name: 'sample.txt', mimeType: 'text/plain', buffer: Buffer.from('sample transfer') });
  await page.getByRole('button', { name: /Create transfer link/ }).click();
  await expect(page.getByText('YOUR LINK')).toBeVisible();
  const receiverURL = await page.locator('.linkbox').evaluate(element => element.firstChild?.textContent?.trim() ?? '');
  expect(receiverURL).toContain('/receive/');
  const receiver = await page.context().newPage();
  await receiver.goto(receiverURL);
  await expect(receiver.getByRole('button', { name: 'Download file' })).toBeVisible();
});
