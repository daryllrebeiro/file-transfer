import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

test('home page has no automated accessibility violations', async ({ page }) => {
  await page.goto('/');
  const results = await new AxeBuilder({ page }).analyze();
  expect(results.violations).toEqual([]);
});

test('not-found route has no automated accessibility violations', async ({ page }) => {
  await page.goto('/this-path-does-not-exist');
  const results = await new AxeBuilder({ page }).analyze();
  expect(results.violations).toEqual([]);
});
