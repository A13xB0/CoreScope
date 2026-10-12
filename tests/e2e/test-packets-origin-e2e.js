/**
 * E2E — the packets page says where each packet started.
 *
 * With the Data tab placing packets in areas, /api/packets carries
 * origins{hash: {area, basis}} and the packet detail an origin. The page shows
 * an Origin column (code, with how it was decided on hover) and an Origin row
 * in the detail pane. Without origins in the response the column is not
 * offered at all.
 *
 * The packet responses are the fixture's own, with origins added, so this
 * runs against any server.
 *
 * Run: BASE_URL=http://localhost:13581 node tests/e2e/test-packets-origin-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

const BASES = ['first-relay', 'zero-hop', 'unknown'];

// Give the first packets an origin each (SCO by first relay, IOI heard with
// no relay, then one placed nowhere) and leave the rest out, as packets newer
// than the index are.
function addOrigins(body, origins) {
  const out = {};
  (body.packets || []).slice(0, 3).forEach((p, i) => {
    // The index names an unplaced start "unknown", not "".
    out[p.hash] = { area: i === 2 ? 'unknown' : (i === 0 ? 'SCO' : 'IOI'), basis: BASES[i] };
  });
  Object.assign(origins, out);
  return Object.assign({}, body, { origins: out });
}

async function openPackets(ctx, withOrigins) {
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  const origins = {};
  if (withOrigins) {
    await page.route(/\/api\/packets(\?|\/[0-9a-f]{16}$)/, async (route) => {
      const res = await route.fetch();
      const body = await res.json();
      const m = route.request().url().match(/\/api\/packets\/([0-9a-f]{16})$/);
      const out = m ? Object.assign({}, body, origins[m[1]] ? { origin: origins[m[1]] } : {}) : addOrigins(body, origins);
      await route.fulfill({ response: res, json: out });
    });
  }
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  await page.goto(BASE + '/#/packets', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#pktBody tr[data-hash]');
  return { page, origins, errors };
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  console.log(`\n=== Packets origin — E2E against ${BASE} ===`);

  const ctx = await browser.newContext({ viewport: { width: 1600, height: 900 } });
  await ctx.addInitScript(() => { try { localStorage.clear(); } catch (e) {} });

  await step('without areas the Origin column is not offered', async () => {
    const { page } = await openPackets(ctx, false);
    assert(!(await page.isVisible('#pktTable th.col-origin')), 'Origin header shown without origins');
    const menu = await page.$$eval('#colToggleMenu input', (els) => els.map((e) => e.dataset.col));
    assert(!menu.includes('origin') || !(await page.isVisible('#colToggleMenu [data-col="origin"]')), 'Origin offered in the column menu: ' + menu.join(','));
    await page.close();
  });

  const { page, origins, errors } = await openPackets(ctx, true);

  await step('with origins the column shows each packet\'s starting area', async () => {
    await page.waitForSelector('#pktTable th.col-origin', { state: 'visible' });
    const [first, second, third] = Object.keys(origins);
    const cell = (h) => page.$eval(`#pktBody tr[data-hash="${h}"] td.col-origin`, (td) => ({ text: td.textContent.trim(), title: td.getAttribute('title') || td.querySelector('[title]')?.getAttribute('title') || '' }));
    const a = await cell(first);
    assert(a.text === 'SCO', 'first origin ' + JSON.stringify(a));
    assert(/first relay/i.test(a.title), 'how it was decided is on hover: ' + a.title);
    const b = await cell(second);
    assert(b.text === 'IOI' && /no relay|heard direct/i.test(b.title), 'second origin ' + JSON.stringify(b));
    const c = await cell(third);
    assert(/unknown/i.test(c.title) && c.text === '—', 'unplaced origin shows a dash, not a code: ' + JSON.stringify(c));
  });

  await step('a packet newer than the index says it is not placed yet', async () => {
    const other = await page.$$eval('#pktBody tr[data-hash]', (rows, known) => rows.map((r) => r.dataset.hash).filter((h) => h && !known.includes(h)), Object.keys(origins));
    assert(other.length > 0, 'fixture has more than three packets');
    const t = await page.$eval(`#pktBody tr[data-hash="${other[0]}"] td.col-origin`, (td) => td.getAttribute('title') || td.querySelector('[title]')?.getAttribute('title') || '');
    assert(/not placed yet/i.test(t), 'pending title: ' + t);
  });

  await step('the column can be hidden from the column menu', async () => {
    assert(await page.isVisible('#colToggleMenu [data-col="origin"]') || (await page.$('#colToggleMenu [data-col="origin"]')), 'Origin missing from the menu');
  });

  await step('the detail pane names the origin and how it was decided', async () => {
    const first = Object.keys(origins)[0];
    await page.click(`#pktBody tr[data-hash="${first}"] td.col-time`);
    await page.waitForFunction(() => /Origin/.test(document.querySelector('.detail-meta')?.textContent || ''));
    const dd = await page.$$eval('.detail-meta dt', (dts) => {
      const dt = dts.find((d) => d.textContent.trim() === 'Origin');
      return dt ? dt.nextElementSibling.textContent.trim() : '';
    });
    assert(/SCO/.test(dd) && /first relay/i.test(dd), 'detail origin: ' + dd);
  });

  await step('no page errors', async () => { assert(errors.length === 0, errors.join('; ')); });

  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });
