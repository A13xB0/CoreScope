/**
 * E2E — Live feed and legend: fold from the title, close, and always come
 * back.
 *
 * Before: closing the feed (or the legend) left a reopen button pinned
 * bottom-right, the legend's own default corner. The legend is drawn above
 * it (z-index 1000 against 500), so the button was invisible and the panel
 * looked gone for good.
 *
 * Now:
 *   - the reopen controls are toggles in the MESH LIVE header, which no
 *     panel can sit over; closing a panel leaves its toggle on top and
 *     clickable at its centre;
 *   - both panels fold to their header strip from a title button, and stay
 *     folded across a reload; dragging the header still moves the panel;
 *   - the legend shows short labels by default, with the descriptions one
 *     click away (remembered), and has its own close button.
 *
 * Run: BASE_URL=http://localhost:13581 node tests/e2e/test-live-panel-fold-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const KEYS = ['live-feed-hidden', 'live-legend-hidden', 'live-feed-folded', 'live-legend-folded', 'live-legend-descriptions',
  'panel-corner-liveFeed', 'panel-corner-liveLegend', 'panel-drag-liveFeed', 'panel-drag-liveLegend'];

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

async function freshLive(page) {
  await page.goto(BASE + '/#/live', { waitUntil: 'domcontentloaded' });
  await page.evaluate((keys) => { for (const k of keys) { try { localStorage.removeItem(k); } catch (_) {} } }, KEYS);
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#liveLegend', { state: 'attached' });
  await page.waitForTimeout(500);
}

// Is the element visible AND the topmost thing at its centre (nothing drawn
// over it)? This is the check the old bottom-right buttons failed.
async function onTop(page, sel) {
  return page.evaluate((s) => {
    const el = document.querySelector(s);
    if (!el) return 'missing';
    const r = el.getBoundingClientRect(), cs = getComputedStyle(el);
    if (r.width === 0 || r.height === 0 || cs.visibility === 'hidden' || cs.display === 'none') return 'not shown';
    const top = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    return top && (top === el || el.contains(top)) ? 'ok' : 'covered by ' + (top ? (top.id || top.className || top.tagName) : 'nothing');
  }, sel);
}
const isHidden = (page, id) => page.evaluate((i) => document.getElementById(i).classList.contains('hidden'), id);
const isFolded = (page, id) => page.evaluate((i) => document.getElementById(i).classList.contains('is-folded'), id);

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  console.log(`\n=== Live panel fold / close / reopen — E2E against ${BASE} ===`);

  for (const vp of [{ w: 1440, h: 900 }, { w: 1024, h: 768 }, { w: 700, h: 900 }]) {
    const tag = `[${vp.w}x${vp.h}]`;
    const ctx = await browser.newContext({ viewport: { width: vp.w, height: vp.h } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    const errors = [];
    page.on('pageerror', (e) => errors.push(e.message));
    await freshLive(page);

    await step(`${tag} the old bottom-right reopen buttons are gone`, async () => {
      const left = await page.evaluate(() => ['feedShowBtn', 'legendToggleBtn'].filter((id) => document.getElementById(id)));
      assert(left.length === 0, 'still in the page: ' + left.join(', '));
    });

    await step(`${tag} panel toggles sit in the MESH LIVE header, pressed while open`, async () => {
      for (const id of ['liveToggleFeed', 'liveToggleLegend']) {
        assert(await onTop(page, '#' + id) === 'ok', id + ': ' + await onTop(page, '#' + id));
        assert(await page.evaluate((i) => !!document.getElementById(i).closest('#liveHeader'), id), id + ' is not in #liveHeader');
        assert(await page.getAttribute('#' + id, 'aria-pressed') === 'true', id + ' should be pressed while its panel is open');
      }
    });

    for (const [panel, close, toggle] of [['liveFeed', '#feedHideBtn', '#liveToggleFeed'], ['liveLegend', '#legendCloseBtn', '#liveToggleLegend']]) {
      await step(`${tag} closing ${panel} leaves its toggle visible, on top and working`, async () => {
        await page.click(close);
        await page.waitForTimeout(350);
        assert(await isHidden(page, panel), panel + ' did not close');
        assert(await page.getAttribute(toggle, 'aria-pressed') === 'false', 'toggle should show the panel is closed');
        const where = await onTop(page, toggle);
        assert(where === 'ok', 'reopen toggle ' + where);
        await page.click(toggle);
        await page.waitForTimeout(350);
        assert(!await isHidden(page, panel), panel + ' did not come back');
        assert(await page.getAttribute(toggle, 'aria-pressed') === 'true', 'toggle should be pressed again');
      });
    }

    await step(`${tag} closed stays closed across a reload, and the toggle still brings it back`, async () => {
      await page.click('#legendCloseBtn');
      await page.reload({ waitUntil: 'domcontentloaded' });
      await page.waitForSelector('#liveLegend', { state: 'attached' });
      await page.waitForTimeout(500);
      assert(await isHidden(page, 'liveLegend'), 'legend reopened after reload');
      assert(await onTop(page, '#liveToggleLegend') === 'ok', 'toggle not reachable after reload');
      await page.click('#liveToggleLegend');
      await page.waitForTimeout(350);
      assert(!await isHidden(page, 'liveLegend'), 'legend did not come back');
    });

    for (const panel of ['liveLegend', 'liveFeed']) {
      await step(`${tag} ${panel} folds from its title to one strip, and stays folded after a reload`, async () => {
        const fold = `#${panel} .panel-fold`;
        const before = (await page.locator('#' + panel).boundingBox()).height;
        await page.click(fold);
        await page.waitForTimeout(200);
        assert(await isFolded(page, panel), 'did not fold');
        assert(await page.getAttribute(fold, 'aria-expanded') === 'false', 'aria-expanded should be false when folded');
        const after = (await page.locator('#' + panel).boundingBox()).height;
        assert(after <= 48 && after < before, `folded height ${after}px (was ${before}px)`);
        await page.reload({ waitUntil: 'domcontentloaded' });
        await page.waitForSelector('#' + panel, { state: 'attached' });
        await page.waitForTimeout(500);
        assert(await isFolded(page, panel), 'fold was not remembered');
        await page.click(fold);
        await page.waitForTimeout(200);
        assert(!await isFolded(page, panel), 'did not unfold');
        assert(await page.getAttribute(fold, 'aria-expanded') === 'true', 'aria-expanded should be true when open');
      });
    }

    await step(`${tag} dragging the legend header moves it and does not fold it`, async () => {
      const header = page.locator('#liveLegend .panel-header');
      const hb = await header.boundingBox();
      const fb = await page.locator('#liveLegend .panel-fold').boundingBox();
      // Grab the strip between the title button and the corner button.
      const x = fb.x + fb.width + 12, y = hb.y + hb.height / 2;
      const start = await page.locator('#liveLegend').boundingBox();
      await page.mouse.move(x, y); await page.mouse.down();
      await page.mouse.move(x - 120, y - 60, { steps: 8 }); await page.mouse.up();
      await page.waitForTimeout(250);
      const end = await page.locator('#liveLegend').boundingBox();
      assert(!await isFolded(page, 'liveLegend'), 'a drag folded the legend');
      // Dragging is on only with a fine pointer at >=768px (live.js dragMql).
      const dragOn = await page.evaluate(() => matchMedia('(pointer: fine) and (min-width: 768px)').matches);
      if (dragOn) assert(Math.abs(end.x - start.x) > 40 || Math.abs(end.y - start.y) > 20, 'legend did not move');
    });

    await step(`${tag} the legend shows short labels by default; descriptions are one click and remembered`, async () => {
      await freshLive(page);
      const longShown = () => page.evaluate(() => [...document.querySelectorAll('#liveLegend .legend-long')].filter((e) => e.offsetParent !== null).length);
      assert(await longShown() === 0, 'descriptions are showing by default');
      const advert = await page.evaluate(() => {
        const li = document.querySelector('#liveLegend li[data-enum="ADVERT"]');
        return li ? { label: li.querySelector('.legend-label').textContent.trim(), title: li.getAttribute('title') || '' } : null;
      });
      assert(advert && advert.label.length > 0 && /—/.test(advert.title), 'ADVERT row should show its short label with the description as hover text: ' + JSON.stringify(advert));
      await page.click('#legendDescBtn');
      await page.waitForTimeout(150);
      assert(await longShown() > 0, 'descriptions did not appear');
      assert(await page.getAttribute('#legendDescBtn', 'aria-pressed') === 'true', 'descriptions button should be pressed');
      await page.reload({ waitUntil: 'domcontentloaded' });
      await page.waitForSelector('#liveLegend', { state: 'attached' });
      await page.waitForTimeout(500);
      assert(await longShown() > 0, 'descriptions setting was not remembered');
    });

    await step(`${tag} no page errors`, async () => { assert(errors.length === 0, errors.join('; ')); });
    await ctx.close();
  }

  // Phones keep today's behaviour: feed and legend stay off, so no toggles.
  const phone = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
  const p = await phone.newPage();
  await freshLive(p);
  await step('[390 phone] panel toggles are not shown where the panels cannot open', async () => {
    const shown = await p.evaluate(() => [...document.querySelectorAll('#livePanelToggles button')].filter((b) => b.offsetParent !== null).length);
    assert(shown === 0, shown + ' toggles shown on a phone');
  });
  await phone.close();

  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });
