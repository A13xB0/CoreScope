/**
 * E2E — the Health page (Network Watch).
 *
 * With netWatch on, a Health link joins the nav. The page shows how many
 * repeaters have gone quiet and the network against usual; each quiet
 * repeater with when it went quiet and what is usual for it; the configured
 * routes, better or worse than usual; and a probe between two repeaters that
 * lists every hop against its usual and says where the traffic drops off.
 * Off, the link stays hidden and the page says how to turn it on.
 *
 * The netwatch API answers are mocked, so this runs against any server.
 *
 * Run: BASE_URL=http://localhost:13581 node tests/e2e/test-health-e2e.js
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

const K = (c) => c.repeat(64);
const hours = (n, f) => Array.from({ length: n }, (_, i) => f(i));
const STATUS = {
  ready: true, hour: '2026-10-07T05:00:00Z', slotHours: 6, days: 7, judged: 142,
  open: [{ node: K('a'), name: 'Keir Hill', lat: 57.17, lon: -2.3, start: '2026-10-06T23:00:00Z', flagged: '2026-10-07T05:00:00Z', usual: 158, now: 6, advertEveryH: 2,
    kind: 'quiet', chance: 2e-9, observers: 13, repeats: 2, affects: ['Highlands ⇄ central belt: Highlands → Central belt'], tier: 'core', windowH: 2 },
    { node: K('h'), name: 'Glen Local', lat: 56.8, lon: -4.2, start: '2026-10-07T01:00:00Z', flagged: '2026-10-07T05:00:00Z', usual: 4, now: 0, advertEveryH: 0,
      kind: 'unheard', tier: 'local', windowH: 4, heardEveryH: 2, observers: 3, repeats: 1 }],
  tiers: { core: 38, local: 104 },
  recent: [{ node: K('b'), name: 'Tarbat RPT', lat: 57.83, lon: -3.83, start: '2026-10-04T06:00:00Z', flagged: '2026-10-04T11:00:00Z', end: '2026-10-06T10:00:00Z', usual: 208, now: 2, advertEveryH: 2, kind: 'quiet', chance: 1e-6, observers: 12, repeats: 1 }],
  feed: { down: false },
  events: [
    { hour: '2026-10-06T23:00:00Z', kind: 'quiet', node: K('a'), name: 'Keir Hill', text: 'Keir Hill went quiet' },
    { hour: '2026-10-06T00:00:00Z', kind: 'step', where: 'Highlands ⇄ central belt: Highlands → Central belt', before: 0.85, after: 0.48, text: 'Highlands ⇄ central belt: Highlands → Central belt fell from 85% to 48% of adverts getting through' },
    { hour: '2026-10-06T10:00:00Z', kind: 'back', node: K('b'), name: 'Tarbat RPT', text: 'Tarbat RPT is back' },
    { hour: '2026-10-04T06:00:00Z', kind: 'quiet', node: K('b'), name: 'Tarbat RPT', text: 'Tarbat RPT went quiet' },
    { hour: '2026-09-20T06:00:00Z', kind: 'quiet', node: K('f'), name: 'Old One', text: 'Old One went quiet' },
  ],
  drift: [{ node: K('b'), name: 'Tarbat RPT', observer: 'O1', observerName: 'Edinburgh Obs', before: 6, now: -3, since: '2026-10-04T00:00:00Z' }],
  nightly: [{ node: K('g'), name: 'Solar Hill', darkFrom: 18, darkTo: 7, nights: 6, earlier: true, shiftH: 1.5 }],
  observers: [{ id: 'O1', name: 'Edinburgh Obs', now: 410, usual: 400, quiet: false }, { id: 'O2', name: 'Caithness Observer', now: 0, usual: 60, quiet: true }],
  networkNow: 1900, networkUsual: 2000,
  corridors: [{ name: 'Highlands ⇄ central belt', a: 'Highlands', b: 'Central belt', lastDay: 724, usualDay: 822, daily: [800, 822, 850, 724], carriers: [{ node: K('c'), name: 'Goyle Hill', packets: 500 }],
    directions: [
      { from: 'Highlands', to: 'Central belt', lastDay: 109, usualDay: 165, daily: [160, 170, 165, 109], carriers: [{ node: K('c'), name: 'Goyle Hill', packets: 100 }],
        reach: { last: 0.48, usual: 0.85, lastAdverts: 94, lastReached: 45, daily: [0.84, 0.86, -1, 0.85, 0.48], low: 0.76, high: 0.97, verdict: 'worse',
          since: '2026-10-06T00:00:00Z', cause: { node: K('a'), name: 'Keir Hill', explains: 0.8, usual: 0.7, last: 0 } },
        reroute: { from: K('a'), fromName: 'Keir Hill', to: K('c'), toName: 'Goyle Hill', since: '2026-10-06T00:00:00Z' } },
      { from: 'Central belt', to: 'Highlands', lastDay: 615, usualDay: 840, daily: [850, 840, 900, 615], carriers: [{ node: K('a'), name: 'Keir Hill', packets: 3 }] }] },
    { name: 'Scotland and North East England', a: 'Scotland', b: 'North East England', lastDay: 612, usualDay: 2, daily: [0, 0, 0, 0, 0, 0, 40, 612], carriers: [],
      directions: [
        { from: 'Scotland', to: 'North East England', lastDay: 400, usualDay: 1, daily: [0, 0, 0, 0, 0, 0, 25, 400], carriers: [],
          reach: { basis: 'chat', last: 0.35, usual: 0, lastAdverts: 536, lastReached: 188, daily: [0, 0, 0, 0.12, 0.35], low: 0, high: 0.06, verdict: 'new' } },
        { from: 'North East England', to: 'Scotland', lastDay: 212, usualDay: 1, daily: [0, 0, 0, 0, 0, 0, 15, 212], carriers: [],
          reach: { basis: 'chat', last: 0.2, usual: 0.6, lastAdverts: 50, lastReached: 10, daily: [0.6, 0.62, 0.58, 0.2], low: 0.4, high: 0.8, verdict: 'worse' } }] }],
  routes: [
    { name: 'North link', from: K('d'), fromName: 'Inverness', to: K('e'), toName: 'Edinburgh', now: 40, usual: 120, lastDay: 300, usualDay: 900, daily: [880, 900, 910, 300],
      reach: { last: 0.3, usual: 0.9, lastAdverts: 120, lastReached: 36, daily: [0.9, 0.91, 0.89, 0.3], low: 0.8, high: 0.98, verdict: 'worse', target: 0.7, burn: 'fast' } },
    { name: 'Ireland gateway', from: K('f'), fromName: 'Galloway', to: K('e'), toName: 'Edinburgh', now: 60, usual: 50, lastDay: 260, usualDay: 200, daily: [190, 200, 210, 260] },
    { name: 'Gateway to Ayr', from: K('f'), fromName: 'Galloway', to: K('g'), toName: 'Ayr', now: 9, usual: 157, lastDay: 17, usualDay: 738, daily: [700, 740, 738, 17], observersDown: ['AYR-Observer'] },
  ],
};
const PROBE = {
  hour: '2026-10-07T05:00:00Z', note: '',
  route: [{ node: K('d'), name: 'Inverness' }, { node: K('a'), name: 'Keir Hill' }, { node: K('c'), name: 'Goyle Hill' }, { node: K('e'), name: 'Edinburgh' }],
  hops: [
    { from: K('d'), fromName: 'Inverness', to: K('a'), toName: 'Keir Hill', now: 110, usual: 120, quiet: false, series: hours(72, () => 20), expected: hours(72, () => 20) },
    { from: K('a'), fromName: 'Keir Hill', to: K('c'), toName: 'Goyle Hill', now: 2, usual: 115, quiet: true, series: hours(72, (i) => (i < 66 ? 19 : 0)), expected: hours(72, () => 19) },
    { from: K('c'), fromName: 'Goyle Hill', to: K('e'), toName: 'Edinburgh', now: 5, usual: 110, quiet: true, series: hours(72, (i) => (i < 66 ? 18 : 1)), expected: hours(72, () => 18) },
  ],
};

async function openPage(browser, { on, feedDown }) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  await ctx.route('**/api/config/client', async (route) => {
    const res = await route.fetch(); const body = await res.json();
    await route.fulfill({ response: res, json: Object.assign({}, body, { netWatch: on }) });
  });
  await ctx.route(/\/api\/netwatch(\/|\?|$)/, (route) => {
    const u = route.request().url();
    if (!on) return route.fulfill({ status: 404, json: { error: 'network watch is off (netWatch.enabled in config.json)' } });
    if (u.includes('/probe')) return route.fulfill({ json: PROBE });
    if (u.includes('/node/')) return route.fulfill({ json: { node: K('a'), name: 'Keir Hill', end: STATUS.hour, relays: hours(360, (i) => (i < 354 ? 25 : 0)), adverts: hours(360, (i) => (i % 2 ? 0 : 1)), expected: hours(360, (i) => (i < 168 ? -1 : 25)),
      advertEveryMin: 120, advertDelivery: [1, 0.92, 1, 0] } });
    return route.fulfill({ json: feedDown ? Object.assign({}, STATUS, { open: [], feed: { down: true, since: '2026-10-07T03:00:00Z' } }) : STATUS });
  });
  const page = await ctx.newPage();
  page.setDefaultTimeout(10000);
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  return { ctx, page, errors };
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  console.log(`\n=== Health page — E2E against ${BASE} ===`);
  const { ctx, page, errors } = await openPage(browser, { on: true });

  await step('with the watch on, Health joins the nav', async () => {
    await page.goto(BASE + '/#/home', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.nav-links a[data-route="health"]', { state: 'attached' });
  });

  await step('the summary says how many repeaters have gone quiet and how the network compares', async () => {
    await page.goto(BASE + '/#/health', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#hQuiet');
    assert((await page.textContent('#hQuiet b')).trim() === '2', 'quiet count');
    assert(/38 core and 104 local/.test(await page.textContent('#hSub')), 'tiers: ' + await page.textContent('#hSub'));
    assert(/95%/.test(await page.textContent('#hNet')), 'network vs usual: ' + await page.textContent('#hNet'));
    assert(/1/.test(await page.textContent('#hObs b')), 'quiet observers');
    assert((await page.textContent('#hRoutes b')).trim() === '1', 'one route worse than usual');
  });

  await step('each quiet repeater says when it went quiet and what is usual for it, with its chart', async () => {
    const card = await page.textContent('#hOpen .h-inc');
    assert(/Keir Hill/.test(card) && /158/.test(card) && /adverts stopped/.test(card), card);
    // attached, not visible: a flat line has no height to be visible by.
    await page.waitForSelector('#hOpen .h-inc svg polyline', { state: 'attached' });
    assert(/Tarbat RPT/.test(await page.textContent('#hBack')), 'recently back');
  });

  await step('a quiet repeater shows its evidence, how often it happens and what it explains', async () => {
    const card = (await page.textContent('#hOpen .h-inc')).replace(/\s+/g, ' ');
    assert(/Quiet/.test(card) && /1 in a billion|less than 1 in/.test(card) && /13 observers/.test(card), 'evidence: ' + card);
    assert(/2nd time/.test(card) && /Highlands → Central belt/.test(card), 'repeats and affects: ' + card);
    await page.waitForFunction(() => /Adverts every 2 h/.test(document.querySelector('#hOpen .h-inc').textContent));
    assert(/0% heard yesterday/.test(await page.textContent('#hOpen .h-inc')), 'advert delivery');
  });

  await step('core and local repeaters say which they are and how they were judged', async () => {
    const cards = await page.$$eval('#hOpen .h-inc', (els) => els.map((e) => e.textContent.replace(/\s+/g, ' ')));
    assert(/Core/.test(cards[0]) && /in 2 h/.test(cards[0]), 'core: ' + cards[0]);
    assert(/Glen Local/.test(cards[1]) && /Local/.test(cards[1]) && /Not heard/.test(cards[1]) && /usually heard at least every 2 h/i.test(cards[1]), 'local: ' + cards[1]);
  });

  await step('a corridor that dropped says since when, the range it usually falls in, and its likely cause', async () => {
    const d = (await page.$$eval('.h-corr:first-child .h-dir', (els) => els.map((e) => e.textContent.replace(/\s+/g, ' '))))[0];
    assert(/76–97%/.test(d) && /since Tue 6 Oct/.test(d), 'range and since: ' + d);
    assert(/Likely cause: Keir Hill/.test(d) && /80% of the drop/.test(d), 'cause: ' + d);
    assert(/Rerouted from Keir Hill to Goyle Hill/.test(d), 'reroute: ' + d);
  });

  await step('every bar says its day', async () => {
    const title = await page.$eval('.h-corr:first-child .h-dir svg rect:last-of-type title', (t) => t.textContent);
    assert(/Tue 6 Oct/.test(title) && /48%/.test(title), 'bar tooltip: ' + title);
  });

  await step('the timeline says what happened in words, with the last week in brief', async () => {
    const tl = (await page.textContent('#hTimeline')).replace(/\s+/g, ' ');
    assert(/Keir Hill went quiet/.test(tl) && /fell from 85% to 48%/.test(tl), 'events: ' + tl);
    assert(/Last 7 days: 2 went quiet, 1 came back, 1 drop/.test(tl), 'digest: ' + tl);
  });

  await step('early warnings list fading signals and nights that start earlier', async () => {
    const w = (await page.textContent('#hWarn')).replace(/\s+/g, ' ');
    assert(/Tarbat RPT/.test(w) && /Edinburgh Obs/.test(w) && /6 dB/.test(w) && /−3 dB/.test(w), 'drift: ' + w);
    assert(/Solar Hill/.test(w) && /earlier/.test(w), 'nightly: ' + w);
  });

  await step('routes say whether they are better or worse than usual', async () => {
    const rows = await page.$$eval('#hRoutesTable tbody tr', (trs) => trs.map((tr) => tr.textContent.replace(/\s+/g, ' ')));
    assert(rows.length === 3, 'three routes');
    assert(/Gateway to Ayr/.test(rows[2]) && /Observer offline/.test(rows[2]) && /AYR-Observer/.test(rows[2]) && !/Worse/.test(rows[2]), 'observer down: ' + rows[2]);
    assert(/North link/.test(rows[0]) && /30%/.test(rows[0]) && /usually 90%/.test(rows[0]) && /Worse/.test(rows[0]), 'north link on advert reach: ' + rows[0]);
    assert(/Burning fast/.test(rows[0]) && /70%/.test(rows[0]), 'target: ' + rows[0]);
    assert(/Ireland gateway/.test(rows[1]) && /\+30%/.test(rows[1]) && /Better/.test(rows[1]), 'gateway: ' + rows[1]);
  });

  await step('a corridor is judged in each direction, so one way dropping shows even when the total looks normal', async () => {
    const card = (await page.$$eval('.h-corr', (els) => els.map((e) => e.textContent.replace(/\s+/g, ' '))))[0];
    assert(/Highlands → Central belt/.test(card) && /Central belt → Highlands/.test(card), 'both directions: ' + card);
    const dirs = await page.$$eval('.h-corr:first-child .h-dir', (els) => els.map((e) => e.textContent.replace(/\s+/g, ' ')));
    assert(dirs.length === 2 && /Worse/.test(dirs[1]) && /−27%/.test(dirs[1]), 'without adverts, the packet count decides: ' + dirs.join(' | '));
    // with adverts, the share of them that crossed decides, the packet count is context
    assert(/48% of 94 adverts/.test(dirs[0]) && /usually 85%/.test(dirs[0]) && /Worse/.test(dirs[0]), 'advert reach: ' + dirs[0]);
    assert(/All packets: 109 yesterday, usually 165/.test(dirs[0]), 'packet count as context: ' + dirs[0]);
    assert(/one bar per day/i.test(await page.textContent('#hCorrNote')), 'the bars say what they are');
  });

  await step('a corridor with almost no history says it is new, not a huge percentage', async () => {
    const txt = (await page.$$eval('.h-corr', (els) => els.map((e) => e.textContent)))[1];
    assert(/New/.test(txt) && !/\d{4,}%/.test(txt), 'new corridor: ' + txt);
  });

  await step('a chat-based direction counts channel messages, not adverts', async () => {
    const dirs = await page.$$eval('.h-corr:nth-child(2) .h-dir', (els) => els.map((e) => e.textContent.replace(/\s+/g, ' ')));
    assert(/New/.test(dirs[0]) && !/Normal/.test(dirs[0]), 'nothing used to cross, so it is new: ' + dirs[0]);
    assert(/20% of 50 channel messages/.test(dirs[1]) && /usually 60%/.test(dirs[1]) && /Worse/.test(dirs[1]) && !/adverts/.test(dirs[1]), 'chat reach: ' + dirs[1]);
  });

  await step('probing a route lists every hop and points at where the traffic drops off', async () => {
    await page.click('#hRoutesTable tbody tr:first-child button');
    await page.waitForSelector('#hProbeOut .h-hop');
    const hops = await page.$$eval('#hProbeOut .h-hop', (els) => els.map((e) => e.textContent.replace(/\s+/g, ' ')));
    assert(hops.length === 3, 'three hops: ' + hops.length);
    assert(/Inverness → Keir Hill/.test(hops[0]) && /Normal/.test(hops[0]), hops[0]);
    assert(/Keir Hill → Goyle Hill/.test(hops[1]) && /Quiet/.test(hops[1]), hops[1]);
    const verdict = await page.textContent('#hProbeVerdict');
    assert(/drops off/i.test(verdict) && /Keir Hill → Goyle Hill/.test(verdict), verdict);
    assert(/probe=/.test(await page.evaluate(() => location.hash)), 'the probe is in the address so it can be shared');
  });

  await step('a probe link opens straight onto the probe', async () => {
    await page.goto(BASE + '/#/health?probe=' + K('d') + ',' + K('e'), { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#hProbeOut .h-hop');
  });

  await step('no page errors', async () => { assert(errors.length === 0, errors.join('; ')); });
  await ctx.close();

  const fd = await openPage(browser, { on: true, feedDown: true });
  await step('when the feed stops, the page says so instead of blaming repeaters', async () => {
    await fd.page.goto(BASE + '/#/health', { waitUntil: 'domcontentloaded' });
    await fd.page.waitForSelector('#hFeed');
    assert(/feed has stopped/i.test(await fd.page.textContent('#hFeed')), await fd.page.textContent('#hFeed'));
  });
  await fd.ctx.close();

  const off = await openPage(browser, { on: false });
  await step('with the watch off, the link stays hidden and the page says how to turn it on', async () => {
    await off.page.goto(BASE + '/#/home', { waitUntil: 'domcontentloaded' });
    await off.page.waitForTimeout(800);
    assert(!(await off.page.$('.nav-links a[data-route="health"]')), 'Health link shown while off');
    await off.page.goto(BASE + '/#/health', { waitUntil: 'domcontentloaded' });
    await off.page.waitForSelector('.h-off');
    assert(/netWatch/.test(await off.page.textContent('.h-off')), 'says which setting');
  });
  await off.ctx.close();

  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });
