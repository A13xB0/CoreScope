/**
 * E2E — Live panels snap to corners.
 *
 * Dragging the feed, legend or node-detail panel and letting go drops it in
 * the nearest corner (the quadrant its centre is in), anchored there by
 * data-position, so it stays in that corner as the window resizes. A corner
 * another panel holds is swapped: that panel takes the dragged one's old
 * corner. The top-right corner sits beside the map's control column. The title (the fold button) is a drag handle too; a plain click on
 * it still folds. While dragging, a hint marks the corner it will land in. Holding
 * Shift on release keeps the old free placement.
 *
 * Run: BASE_URL=http://localhost:13581 node tests/e2e/test-live-panel-snap-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const PANELS = ['liveFeed', 'liveLegend', 'liveNodeDetail'];

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

async function openLive(page) {
  await page.goto(BASE + '/#/home', { waitUntil: 'domcontentloaded' });
  await page.evaluate((ids) => {
    for (const id of ids) {
      for (const k of ['panel-drag-', 'panel-corner-']) { try { localStorage.removeItem(k + id); } catch (_) {} }
    }
  }, PANELS);
  await page.goto(BASE + '/#/live', { waitUntil: 'load' });
  await page.waitForSelector('#liveLegend .panel-header', { timeout: 8000 });
  await page.waitForTimeout(500);
}

const headerCentre = (page, id) => page.$eval('#' + id + ' .panel-header', (el) => {
  const r = el.getBoundingClientRect();
  return { x: r.left + Math.min(40, r.width / 2), y: r.top + r.height / 2 };
});

// Drag panel id so that its header moves to (tx, ty); returns before release
// when hold is set, so the caller can look at the hint.
async function drag(page, id, tx, ty, { hold = false, shift = false } = {}) {
  const h = await headerCentre(page, id);
  await page.mouse.move(h.x, h.y);
  await page.mouse.down();
  await page.mouse.move(tx, ty, { steps: 12 });
  if (hold) return;
  if (shift) await page.keyboard.down('Shift');
  await page.mouse.up();
  if (shift) await page.keyboard.up('Shift');
  await page.waitForTimeout(250);
}

const state = (page, id) => page.$eval('#' + id, (el) => ({
  position: el.getAttribute('data-position'),
  dragged: el.dataset.dragged || null,
  inline: el.style.top || el.style.left || el.style.transform || '',
  corner: localStorage.getItem('panel-corner-' + el.id),
  free: localStorage.getItem('panel-drag-' + el.id),
  rect: (() => { const r = el.getBoundingClientRect(); return { left: r.left, right: r.right, top: r.top, bottom: r.bottom }; })(),
}));

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  console.log(`\n=== Live panels snap to corners — E2E against ${BASE} ===`);
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));

  await openLive(page);
  await step('fixture: the legend starts bottom-right and the feed bottom-left', async () => {
    assert((await state(page, 'liveLegend')).position === 'br', 'legend ' + JSON.stringify(await state(page, 'liveLegend')));
    assert((await state(page, 'liveFeed')).position === 'bl', 'feed ' + JSON.stringify(await state(page, 'liveFeed')));
  });

  await step('while dragging, a hint marks the corner the panel will land in', async () => {
    await drag(page, 'liveLegend', 300, 200, { hold: true });
    const hint = await page.evaluate(() => {
      const h = document.getElementById('panelSnapHint');
      return h && !h.hidden && getComputedStyle(h).display !== 'none' ? h.dataset.corner : null;
    });
    await page.mouse.up();
    await page.waitForTimeout(250);
    assert(hint === 'tl', 'hint corner ' + hint);
    assert(await page.evaluate(() => { const h = document.getElementById('panelSnapHint'); return !h || h.hidden; }), 'hint still shown after release');
  });

  await step('letting go drops the legend into the top-left corner, anchored there', async () => {
    const s = await state(page, 'liveLegend');
    assert(s.position === 'tl', 'data-position ' + s.position);
    assert(!s.dragged && !s.inline, 'free placement left behind: ' + JSON.stringify(s));
    assert(s.corner === 'tl' && !s.free, 'storage: corner=' + s.corner + ' free=' + s.free);
  });

  // The live header bar sits at the top-left; a panel in a top corner sits
  // below it, however tall the header is (it wraps onto two rows when narrow).
  const underHeader = (page, id) => page.evaluate((pid) => {
    const p = document.getElementById(pid).getBoundingClientRect();
    const h = document.getElementById('liveHeader').getBoundingClientRect();
    return { panelTop: p.top, headerBottom: h.bottom, clear: p.top >= h.bottom || p.left >= h.right || p.right <= h.left };
  }, id);
  await step('a panel in the top-left corner sits below the header bar, not over it', async () => {
    let u = await underHeader(page, 'liveLegend');
    assert(u.clear, 'legend top ' + u.panelTop + ' is above the header bottom ' + u.headerBottom + ' at 1280px');
    await page.setViewportSize({ width: 820, height: 800 });
    await page.waitForTimeout(400);
    u = await underHeader(page, 'liveLegend');
    assert(u.clear, 'legend top ' + u.panelTop + ' is above the header bottom ' + u.headerBottom + ' at 820px');
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.waitForTimeout(300);
  });

  await step('a panel dropped top-right stays in that corner as the window narrows', async () => {
    await drag(page, 'liveFeed', 1100, 200);
    await page.waitForTimeout(500); // the corner change is animated
    const before = await state(page, 'liveFeed');
    assert(before.position === 'tr', 'feed data-position ' + before.position);
    const gap = before.rect.right;
    await page.setViewportSize({ width: 1000, height: 800 });
    await page.waitForTimeout(400);
    const after = await state(page, 'liveFeed');
    assert(Math.abs((1280 - gap) - (1000 - after.rect.right)) <= 2,
      'feed right edge ' + after.rect.right + ', want ' + (1000 - (1280 - gap)) + ' (same inset as at 1280)');
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.waitForTimeout(300);
  });

  // The map's zoom, layers, fullscreen and settings buttons run down the
  // top-right edge; a panel parked in that corner sits beside them.
  const overlapsControls = (page, id) => page.evaluate((pid) => {
    const p = document.getElementById(pid).getBoundingClientRect();
    return [...document.querySelectorAll('.leaflet-top.leaflet-right .leaflet-control')].some((c) => {
      const r = c.getBoundingClientRect();
      return r.width > 0 && p.left < r.right && p.right > r.left && p.top < r.bottom && p.bottom > r.top;
    });
  }, id);
  await step('a panel in the top-right corner sits beside the map controls, not over them', async () => {
    assert((await state(page, 'liveFeed')).position === 'tr', 'fixture: feed not top-right');
    const u = await underHeader(page, 'liveFeed');
    assert(u.clear, 'the feed sits over the header bar: top ' + u.panelTop + ', header bottom ' + u.headerBottom);
    assert(!(await overlapsControls(page, 'liveFeed')), 'the feed covers the map controls at 1280px');
    await page.setViewportSize({ width: 900, height: 800 });
    await page.waitForTimeout(400);
    assert(!(await overlapsControls(page, 'liveFeed')), 'the feed covers the map controls at 900px');
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.waitForTimeout(300);
  });

  await step('dropping on a held corner swaps the two panels', async () => {
    // legend tl, feed tr: drag the legend to the top-right
    await drag(page, 'liveLegend', 1100, 200);
    const legend = await state(page, 'liveLegend'), feed = await state(page, 'liveFeed');
    assert(legend.position === 'tr', 'legend ' + legend.position);
    assert(feed.position === 'tl' && feed.corner === 'tl', 'feed should take tl, got ' + feed.position + '/' + feed.corner);
  });

  await step('the corner button still cycles corners after a snap', async () => {
    await page.click('#liveLegend .panel-corner-btn');
    await page.waitForTimeout(200);
    const s = await state(page, 'liveLegend');
    assert(s.position && s.position !== 'tr', 'corner button did nothing: ' + s.position);
  });

  await step('the snap survives a reload', async () => {
    const want = (await state(page, 'liveLegend')).position;
    await page.reload({ waitUntil: 'load' });
    await page.waitForSelector('#liveLegend .panel-header');
    await page.waitForTimeout(500);
    assert((await state(page, 'liveLegend')).position === want, 'after reload ' + (await state(page, 'liveLegend')).position + ', want ' + want);
  });

  await step('holding Shift on release keeps free placement', async () => {
    await drag(page, 'liveLegend', 640, 420, { shift: true });
    const s = await state(page, 'liveLegend');
    assert(s.dragged === 'true' && s.position === null && s.free, 'not free: ' + JSON.stringify(s));
  });

  await step('with "Snap panels" off, a dropped panel stays where it was let go, and the choice is kept', async () => {
    assert(await page.isChecked('#liveSnapToggle'), 'Snap panels is on by default');
    await page.uncheck('#liveSnapToggle');
    await drag(page, 'liveFeed', 600, 380);
    const s = await state(page, 'liveFeed');
    assert(s.dragged === 'true' && s.position === null && s.free, 'snapped anyway: ' + JSON.stringify(s));
    await page.reload({ waitUntil: 'load' });
    await page.waitForSelector('#liveFeed .panel-header');
    await page.waitForTimeout(500);
    assert(!(await page.isChecked('#liveSnapToggle')), 'the off choice did not survive a reload');
    assert((await state(page, 'liveFeed')).dragged === 'true', 'the free position did not survive a reload');
    await page.check('#liveSnapToggle');
    await drag(page, 'liveFeed', 1100, 700);
    assert((await state(page, 'liveFeed')).position === 'br', 'turned back on, dropping snaps again');
  });

  const folded = (page, id) => page.$eval('#' + id + ' .panel-fold', (b) => b.getAttribute('aria-expanded') === 'false');
  await step('a plain click on the title still folds the panel, once', async () => {
    await openLive(page);
    assert(!(await folded(page, 'liveLegend')), 'fixture: legend starts folded');
    await page.click('#liveLegend .panel-fold');
    await page.waitForTimeout(200);
    assert(await folded(page, 'liveLegend'), 'click did not fold (or folded twice)');
    await page.click('#liveLegend .panel-fold');
    await page.waitForTimeout(200);
    assert(!(await folded(page, 'liveLegend')), 'second click did not unfold');
  });

  await step('dragging by the title moves the panel without folding it', async () => {
    await drag(page, 'liveLegend', 300, 200);
    await page.waitForTimeout(100);
    const s = await state(page, 'liveLegend');
    assert(s.position === 'tl', 'legend ' + s.position);
    assert(!(await folded(page, 'liveLegend')), 'the drag folded the legend');
  });

  await step('no page errors', async () => { assert(errors.length === 0, errors.join('; ')); });

  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });
