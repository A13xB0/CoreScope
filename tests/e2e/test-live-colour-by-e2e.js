/**
 * E2E — Live "Colour packets by".
 *
 * The "Color by hash" checkbox becomes a "Colour by" menu: Packet type,
 * Scope, Scope kind, Path hash size, or Hash (the old checkbox's colouring).
 * The legend's first section follows the choice: packet types, the scopes
 * seen recently with counts, the four scope kinds, or the three hash sizes.
 * The choice is remembered, migrates the old checkbox (off -> Packet type,
 * otherwise Hash, its old default), keeps the packets page's hash colouring
 * in step, and deep-links as #/live?colourBy=<mode>.
 *
 * Run: BASE_URL=http://localhost:13581 node tests/e2e/test-live-colour-by-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const KEYS = ['live-colour-by', 'meshcore-color-packets-by-hash', 'live-legend-hidden', 'live-legend-folded'];

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

async function openLive(page, hash, storage) {
  await page.goto(BASE + '/#/home', { waitUntil: 'domcontentloaded' });
  await page.evaluate(([keys, set]) => {
    for (const k of keys) { try { localStorage.removeItem(k); } catch (_) {} }
    for (const k of Object.keys(set || {})) { try { localStorage.setItem(k, set[k]); } catch (_) {} }
  }, [KEYS, storage || {}]);
  await page.goto(BASE + '/' + (hash || '#/live'), { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#liveLegend', { state: 'attached' });
  await page.waitForTimeout(600);
}
const mode = (page) => page.evaluate(() => { const s = document.getElementById('liveColourBy'); return s ? s.value : null; });
const legendTitle = (page) => page.evaluate(() => { const t = document.getElementById('legendGroupTitle'); return t ? t.textContent.trim() : null; });
const legendLabels = (page) => page.evaluate(() => [...document.querySelectorAll('#legendGroupList li .legend-label')].map((e) => e.textContent.trim()));
async function choose(page, value) {
  await page.evaluate(() => { const c = document.getElementById('liveControls'); if (c) c.classList.remove('is-collapsed'); });
  await page.selectOption('#liveColourBy', value);
  await page.waitForTimeout(300);
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  console.log(`\n=== Live colour packets by — E2E against ${BASE} ===`);
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));

  await openLive(page);
  await step('a "Colour by" menu replaces the "Color by hash" checkbox, with five choices', async () => {
    assert(await page.evaluate(() => !document.getElementById('liveColorHashToggle')), 'the old checkbox is still there');
    const opts = await page.evaluate(() => { const s = document.getElementById('liveColourBy'); return s ? [...s.options].map((o) => o.value) : null; });
    assert(JSON.stringify(opts) === JSON.stringify(['type', 'scope', 'sclass', 'hashsize', 'hash']), 'options: ' + JSON.stringify(opts));
    assert(await page.evaluate(() => !!document.getElementById('liveColourBy').closest('#liveControls')), 'menu is not in the live controls');
  });
  await step('with nothing saved it starts on Hash, the old checkbox default', async () => {
    assert(await mode(page) === 'hash', 'mode ' + await mode(page));
  });

  await step('Packet type: the legend lists the packet types', async () => {
    await choose(page, 'type');
    assert(/packet types/i.test(await legendTitle(page)), 'title ' + await legendTitle(page));
    assert(await page.evaluate(() => !!document.querySelector('#legendGroupList li[data-enum="ADVERT"]')), 'no ADVERT row');
  });
  await step('Scope kind: four fixed groups', async () => {
    await choose(page, 'sclass');
    assert(/scope kind/i.test(await legendTitle(page)), 'title ' + await legendTitle(page));
    const l = await legendLabels(page);
    assert(l.length === 4 && l.includes('Region scope') && l.includes('Unscoped flood') && l.includes('Direct'), 'rows ' + JSON.stringify(l));
  });
  await step('Path hash size: 1-, 2- and 3-byte', async () => {
    await choose(page, 'hashsize');
    assert(/hash size/i.test(await legendTitle(page)), 'title ' + await legendTitle(page));
    const l = await legendLabels(page);
    for (const want of ['1-byte', '2-byte', '3-byte']) assert(l.includes(want), want + ' missing: ' + JSON.stringify(l));
  });
  await step('Scope: the scopes seen recently, with counts, busiest first', async () => {
    await choose(page, 'scope');
    assert(/scopes/i.test(await legendTitle(page)), 'title ' + await legendTitle(page));
    await page.evaluate(() => {
      const C = window._liveColourBy;
      const p = (route, scope) => ({ route_type: route, scope_name: scope, raw_hex: '0140ff', decoded: { header: { payloadTypeName: 'GRP_TXT' } } });
      for (let i = 0; i < 3; i++) C.note(p(0, '#sco'));
      C.note(p(0, '#ioi'));
      C.note(p(1, null));
      C.renderLegendNow();
    });
    const rows = await page.evaluate(() => [...document.querySelectorAll('#legendGroupList li')].map((li) => li.textContent.replace(/\s+/g, ' ').trim()));
    assert(rows.length >= 3, 'rows ' + JSON.stringify(rows));
    assert(/#sco.*3/.test(rows[0]), 'busiest first with its count: ' + JSON.stringify(rows));
    assert(rows.some((r) => /Not scoped/.test(r)), 'no "Not scoped" row: ' + JSON.stringify(rows));
  });
  await step('the choice is remembered and keeps the packets page hash colouring in step', async () => {
    assert(await page.evaluate(() => localStorage.getItem('live-colour-by')) === 'scope', 'not saved');
    assert(await page.evaluate(() => localStorage.getItem('meshcore-color-packets-by-hash')) === 'false', 'packets page still colours by hash');
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#liveLegend', { state: 'attached' });
    await page.waitForTimeout(500);
    assert(await mode(page) === 'scope', 'mode after reload ' + await mode(page));
    await choose(page, 'hash');
    assert(await page.evaluate(() => localStorage.getItem('meshcore-color-packets-by-hash')) === 'true', 'hash mode should turn the packets page colouring back on');
  });
  await step('the legend descriptions button still works in a group mode', async () => {
    await choose(page, 'sclass');
    const before = await page.evaluate(() => [...document.querySelectorAll('#legendGroupList .legend-long')].filter((e) => e.offsetParent !== null).length);
    await page.click('#legendDescBtn');
    await page.waitForTimeout(150);
    const after = await page.evaluate(() => [...document.querySelectorAll('#legendGroupList .legend-long')].filter((e) => e.offsetParent !== null).length);
    assert(before === 0 && after > 0, `descriptions ${before} -> ${after}`);
  });
  await step('no page errors', async () => { assert(errors.length === 0, errors.join('; ')); });

  const p2 = await ctx.newPage();
  await step('an old "Color by hash" off migrates to Packet type', async () => {
    await openLive(p2, '#/live', { 'meshcore-color-packets-by-hash': 'false' });
    assert(await mode(p2) === 'type', 'mode ' + await mode(p2));
  });
  await step('#/live?colourBy=sclass opens on Scope kind', async () => {
    await openLive(p2, '#/live?colourBy=sclass');
    assert(await mode(p2) === 'sclass', 'mode ' + await mode(p2));
  });

  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });
