import { test, expect } from '@playwright/test';

test('sender transfers a multi-chunk file over the server relay', async ({ page }) => {
  const payload = Buffer.alloc(3 * 1024 * 1024);
  for (let i = 0; i < payload.length; i++) payload[i] = i % 251;

  await page.addInitScript(() => localStorage.setItem('relay:default_transport', 'relay'));
  await page.goto('/');
  await page.setInputFiles('input[type="file"]', { name: 'relay.bin', mimeType: 'application/octet-stream', buffer: payload });
  await page.getByRole('button', { name: /Create transfer link/ }).click();
  await expect(page.getByText('YOUR LINK')).toBeVisible();

  const receiverURL = await page.locator('.linkbox').evaluate(el => el.firstChild?.textContent?.trim() ?? '');
  expect(receiverURL).toContain('/receive/');

  const receiver = await page.context().newPage();
  await receiver.addInitScript(() => {
    window.showSaveFilePicker = async () => {
      const chunks: Uint8Array[] = [];
      return {
        createWritable: async () => ({
          write: async (data: Uint8Array) => { chunks.push(new Uint8Array(data.buffer, data.byteOffset, data.byteLength)); },
          close: async () => { (window as any).__relayReceived = chunks; },
        }),
      };
    };
  });
  await receiver.goto(receiverURL);

  await receiver.getByRole('button', { name: 'Download file' }).click();
  await expect(page.getByText('Transfer complete')).toBeVisible({ timeout: 30_000 });
  await expect(receiver.getByRole('heading', { name: 'File received.' })).toBeVisible({ timeout: 30_000 });

  const received = await receiver.evaluate(async () => {
    const parts = (window as any).__relayReceived as Uint8Array[];
    const total = parts.reduce((sum, p) => sum + p.byteLength, 0);
    const out = new Uint8Array(total);
    let offset = 0;
    for (const part of parts) {
      out.set(part, offset);
      offset += part.byteLength;
    }
    const digest = await crypto.subtle.digest('SHA-256', out);
    return Array.from(new Uint8Array(digest)).map(b => b.toString(16).padStart(2, '0')).join('');
  });
  const { createHash } = await import('node:crypto');
  expect(received).toBe(createHash('sha256').update(payload).digest('hex'));
});
