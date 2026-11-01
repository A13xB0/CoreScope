/**
 * E2E — the Scope Audit's area dropdown.
 *
 * The audit can be narrowed to the repeaters that STAND in an area
 * (?area=, their node position), next to the view and window controls. It
 * uses the same area picker as the map and node list, so a chosen area
 * follows you between pages. The choice goes in the request and the address;
 * #/scope-audit?area=X opens with it. With no areas configured there is no
 * dropdown. The areas list is mocked, so this runs against any server.
 *
 * Run: BASE_URL=http://localhost:13581 node tests/e2e/test-scope-audit-area-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const AREAS = [{ key: 'BEL', label: 'Belgium' }, { key: 'FRA', label: 'France' }];

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

async function open(browser, hash, areas, stored) {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(10000);
  const seen = { audit: [], errors: [] };
  page.on('pageerror', (e) => seen.errors.push(e.message));
  page.on('request', (r) => { if (r.url().includes('/api/scope-audit')) seen.audit.push(r.url()); });
  await page.route('**/api/config/areas', (r) => r.fulfill({ json: areas }));
  await page.goto(BASE + '/#/home', { waitUntil: 'domcontentloaded' });
  await page.evaluate((v) => { try { if (v) localStorage.setItem('meshcore-area-filter', v); else localStorage.removeItem('meshcore-area-filter'); } catch (_) {} }, stored || null);
  await page.goto(BASE + '/' + hash, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#saBody', { state: 'attached' });
  await page.waitForTimeout(1200);
  return { ctx, page, seen };
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  console.log(`\n=== Scope Audit area dropdown — E2E against ${BASE} ===`);

  {
    const { ctx, page } = await open(browser, '#/scope-audit', []);
    await step('with no areas configured there is no area dropdown', async () => {
      const shown = await page.evaluate(() => { const el = document.getElementById('saAreaFilter'); return !!el && el.offsetParent !== null && el.textContent.trim() !== ''; });
      assert(!shown, 'area dropdown shown without areas');
    });
    await ctx.close();
  }

  {
    const { ctx, page, seen } = await open(browser, '#/scope-audit', AREAS);
    await step('with areas the dropdown sits in the audit controls, starting on All', async () => {
      const t = await page.textContent('#saAreaFilter .region-dropdown-trigger');
      assert(/All/.test(t), 'trigger reads ' + t);
      assert(await page.evaluate(() => !!document.getElementById('saAreaFilter').closest('.sa-controls')), 'not in the audit controls');
      assert(seen.audit.length > 0 && !seen.audit.some((u) => u.includes('area=')), 'first request should carry no area: ' + seen.audit.join(' '));
    });
    await step('choosing an area reloads the audit for it and puts it in the address', async () => {
      await page.click('#saAreaFilter .region-dropdown-trigger');
      await page.click('#saAreaFilter [data-area="FRA"]');
      await page.waitForTimeout(800);
      assert(seen.audit.some((u) => u.includes('area=FRA')), 'no request with area=FRA: ' + seen.audit.join(' '));
      assert(/area=FRA/.test(await page.evaluate(() => location.hash)), 'address: ' + await page.evaluate(() => location.hash));
    });
    await step('the area survives switching to the transport view', async () => {
      await page.click('#saMode button[data-mode="transport"]');
      await page.waitForTimeout(800);
      const last = seen.audit[seen.audit.length - 1];
      assert(/mode=transport/.test(last) && /area=FRA/.test(last), 'last request ' + last);
    });
    await step('choosing All clears it', async () => {
      await page.click('#saAreaFilter .region-dropdown-trigger');
      await page.click('#saAreaFilter [data-area="__all__"]');
      await page.waitForTimeout(800);
      assert(!/area=/.test(seen.audit[seen.audit.length - 1]), 'still filtered: ' + seen.audit[seen.audit.length - 1]);
      assert(!/area=/.test(await page.evaluate(() => location.hash)), 'address still has an area');
    });
    await step('no page errors', async () => { assert(seen.errors.length === 0, seen.errors.join('; ')); });
    await ctx.close();
  }

  {
    const { ctx, page } = await open(browser, '#/scope-audit', AREAS);
    await page.route('**/api/scope-audit*', (r) => {
      const area = new URL(r.request().url()).searchParams.get('area');
      r.fulfill({ json: { window: '24h', since: '', area: area || undefined, repeaters: [] } });
    });
    await step('an area with nothing in this view says so, and points back to All', async () => {
      await page.click('#saAreaFilter .region-dropdown-trigger');
      await page.click('#saAreaFilter [data-area="FRA"]');
      await page.waitForTimeout(800);
      const msg = await page.textContent('#saBody');
      assert(/No repeater standing in France/.test(msg) && /All/.test(msg), 'empty state reads: ' + msg.trim().slice(0, 120));
    });
    await ctx.close();
  }

  {
    const { ctx, page, seen } = await open(browser, '#/scope-audit?window=7d&area=BEL', AREAS);
    await step('#/scope-audit?area=BEL opens filtered to Belgium', async () => {
      assert(seen.audit.some((u) => /area=BEL/.test(u) && /window=7d/.test(u)), 'requests: ' + seen.audit.join(' '));
      assert(/Belgium/.test(await page.textContent('#saAreaFilter .region-dropdown-trigger')), 'trigger does not show Belgium');
    });
    await ctx.close();
  }

  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });
