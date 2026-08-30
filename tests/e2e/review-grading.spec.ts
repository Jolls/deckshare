import { test, expect, type Page } from '@playwright/test';

// Drives a real browser through signup -> deck -> note -> review, the path CLAUDE.md §10
// priority 6 and issue #100 want covered: nothing else in the repo exercises web/static/review.js
// against a live server, so a wiring regression there (keybindings, the flush debounce, the
// Origin/CSRF-carrying fetch) would ship undetected.

async function signUp(page: Page, email: string) {
  await page.goto('/signup');
  await page.fill('#email', email);
  await page.fill('#display_name', 'E2E Reviewer');
  await page.fill('#password', 'correct-horse-battery-staple');
  await page.getByRole('button', { name: 'Sign up' }).click();
  await expect(page).toHaveURL(/\/decks$/);
}

// Basic/Cloze are auto-seeded on signup (internal/auth/notetypes.go); a fresh deck + one Basic
// note gives the review queue exactly one New card, due immediately.
async function createDeckWithDueCard(page: Page, deckName: string) {
  await page.getByRole('link', { name: 'New deck' }).click();
  await page.fill('#name', deckName);
  await page.getByRole('button', { name: 'Create deck' }).click();
  await expect(page).toHaveURL(/\/decks\/[0-9a-f-]+$/);

  await page.getByRole('link', { name: 'Add note' }).click();
  await page.getByLabel('Note type').selectOption({ label: 'Basic' });
  await page.getByRole('button', { name: 'Continue' }).click();

  const fields = page.locator('textarea[name="field[]"]');
  await fields.nth(0).fill('What is the capital of France?');
  await fields.nth(1).fill('Paris');
  await page.getByRole('button', { name: 'Add note' }).click();
  await expect(page).toHaveURL(/\/decks\/[0-9a-f-]+$/);
}

test('keyboard grading POSTs the batch within the flush debounce window', async ({ page }) => {
  const email = `e2e-${Date.now()}-${Math.floor(Math.random() * 1e6)}@example.com`;
  await signUp(page, email);
  await createDeckWithDueCard(page, `E2E Deck ${Date.now()}`);

  await page.getByRole('link', { name: 'Study' }).click();
  await expect(page).toHaveURL(/\/review$/);

  const stage = page.locator('#review-stage');
  await stage.waitFor({ state: 'visible' });

  await page.keyboard.press('Space'); // reveal
  await expect(stage).toHaveAttribute('data-revealed', 'true');

  const batchResponse = page.waitForResponse(
    (res) => res.url().endsWith('/api/reviews/batch') && res.request().method() === 'POST',
    { timeout: 3000 }, // FLUSH_DEBOUNCE_MS (review.js) is 2000ms
  );
  await page.keyboard.press('Digit3'); // grade: Good

  const response = await batchResponse;
  expect(response.status()).toBe(200);

  const sentEvents = JSON.parse(response.request().postData() ?? '{}').events;
  const body = await response.json();
  const result = body.results.find((r: { id: string }) => r.id === sentEvents[0].id);

  expect(result).toBeTruthy();
  expect(result.status).toBe('applied');
});
