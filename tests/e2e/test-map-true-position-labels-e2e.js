/**
 * E2E — the map with clustering off keeps every node where it is.
 *
 * Clustering off used to push overlapping markers up to 132px away from
 * their GPS position and draw a red dashed line plus a red dot back to it,
 * with every repeater drawn as a black hash pill. Now:
 *   - every marker sits at its own position; nothing is moved, so there are
 *     no offset lines or dots;
 *   - repeaters are role markers like every other node;
 *   - "Hash prefix labels" puts the hash beside a repeater where there is
 *     room (right, left, above or below), and leaves it off where there is
 *     not: labels never overlap each other or a marker. Zooming in makes
 *     room for more;
 *   - the node under the pointer shows its hash in a tooltip, labels on or off;
 *   - clustering on is unchanged.
 *
 * Run: BASE_URL=http://localhost:13581 node tests/e2e/test-map-true-position-labels-e2e.js
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

async function openMap(page, { clustering, labels }) {
  await page.goto(BASE + '/#/home', { waitUntil: 'domcontentloaded' });
  await page.evaluate(([c, l]) => {
    localStorage.setItem('meshcore-map-clustering', String(c));
    localStorage.setItem('meshcore-map-hash-labels', String(l));
    localStorage.removeItem('map-view');
  }, [clustering, labels]);
  await page.goto(BASE + '/#/map', { waitUntil: 'load' });
  await page.waitForFunction(() => document.querySelectorAll('.leaflet-marker-icon').length > 5, null, { timeout: 15000 });
  await page.waitForTimeout(800);
}

const boxes = (page, sel) => page.$$eval(sel, (els) => els.map((e) => {
  const r = e.getBoundingClientRect();
  return { l: r.left, t: r.top, r: r.right, b: r.bottom, w: r.width, h: r.height, text: e.textContent.trim() };
}).filter((b) => b.w > 0 && b.h > 0));

const overlap = (a, b) => a.l < b.r - 0.5 && a.r > b.l + 0.5 && a.t < b.b - 0.5 && a.b > b.t + 0.5;

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  console.log(`\n=== Map with clustering off: true positions and hash labels — E2E against ${BASE} ===`);
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(10000);
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));

  await openMap(page, { clustering: false, labels: true });

  await step('every marker sits at its own position', async () => {
    const m = await page.evaluate(() => window.__meshcoreMapInternals.markerPositions());
    assert(m.length > 5, 'fixture has markers: ' + m.length);
    const moved = m.filter((x) => !x.atNode);
    assert(moved.length === 0, moved.length + ' of ' + m.length + ' markers drawn away from their node');
  });

  await step('no offset lines or dots are drawn', async () => {
    const n = await page.$$eval('.leaflet-overlay-pane path[stroke-dasharray="6,4"]', (els) => els.length);
    assert(n === 0, n + ' dashed offset lines');
  });

  await step('repeaters are role markers, not hash pills', async () => {
    assert((await page.$$('.meshcore-label-marker')).length === 0, 'hash pills still drawn with clustering off');
  });

  let labelsAtStart = 0;
  await step('hash labels sit beside repeaters and never overlap each other or a marker', async () => {
    const labels = await boxes(page, '.mc-hash-label');
    const marks = await boxes(page, '.leaflet-marker-icon.meshcore-marker');
    assert(labels.length > 0, 'no hash labels at all');
    labelsAtStart = labels.length;
    for (let i = 0; i < labels.length; i++) {
      for (let j = i + 1; j < labels.length; j++) assert(!overlap(labels[i], labels[j]), 'labels overlap: ' + labels[i].text + ' / ' + labels[j].text);
      for (const m of marks) assert(!overlap(labels[i], m), 'label ' + labels[i].text + ' covers a marker');
    }
    assert(labels.every((l) => /^[^0-9A-F]*[0-9A-F]{2,6}$/.test(l.text)), 'label text: ' + labels.map((l) => l.text).join(','));
  });

  await step('zooming in makes room for more labels, still at true positions', async () => {
    await page.evaluate(() => { const m = window.__meshcoreMapInternals.leafletMap(); m.setZoom(m.getZoom() + 2, { animate: false }); });
    await page.waitForTimeout(900);
    const labels = await boxes(page, '.mc-hash-label');
    const inView = await page.evaluate(() => window.__meshcoreMapInternals.markerPositions().filter((x) => x.inView && x.repeater).length);
    assert(labels.length >= Math.min(labelsAtStart, inView), 'labels ' + labels.length + ' after zooming in (were ' + labelsAtStart + ', repeaters in view ' + inView + ')');
    const moved = (await page.evaluate(() => window.__meshcoreMapInternals.markerPositions())).filter((x) => !x.atNode);
    assert(moved.length === 0, 'markers moved after zoom: ' + moved.length);
    await page.evaluate(() => { const m = window.__meshcoreMapInternals.leafletMap(); m.setZoom(m.getZoom() - 2, { animate: false }); });
    await page.waitForTimeout(600);
  });

  await step('the node under the pointer shows its hash', async () => {
    const pos = await page.evaluate(() => window.__meshcoreMapInternals.markerPositions().find((x) => x.repeater && x.inView));
    assert(pos, 'a repeater in view');
    await page.mouse.move(pos.x, pos.y);
    await page.waitForSelector('.leaflet-tooltip.mc-hash-tip', { state: 'visible' });
    const t = await page.textContent('.leaflet-tooltip.mc-hash-tip');
    assert(t.indexOf(pos.hash) >= 0, 'tooltip "' + t + '" lacks ' + pos.hash);
  });

  await step('with hash labels off there are no labels, and hover still names the node', async () => {
    await openMap(page, { clustering: false, labels: false });
    assert((await page.$$('.mc-hash-label')).length === 0, 'labels drawn with the toggle off');
    const pos = await page.evaluate(() => window.__meshcoreMapInternals.markerPositions().find((x) => x.repeater && x.inView));
    await page.mouse.move(pos.x, pos.y);
    await page.waitForSelector('.leaflet-tooltip.mc-hash-tip', { state: 'visible' });
  });

  await step('clustering on is unchanged: clusters, no beside-labels', async () => {
    await openMap(page, { clustering: true, labels: true });
    assert((await page.$$('.mc-cluster')).length > 0, 'no cluster icons');
    assert((await page.$$('.mc-hash-label')).length === 0, 'beside-labels drawn under clustering');
  });

  await step('no page errors', async () => { assert(errors.length === 0, errors.join('; ')); });

  await page.evaluate(() => { localStorage.removeItem('meshcore-map-clustering'); localStorage.removeItem('meshcore-map-hash-labels'); });
  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });
