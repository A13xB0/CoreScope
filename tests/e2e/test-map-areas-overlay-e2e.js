/**
 * E2E — the map's Areas overlay.
 *
 * Areas (config.json polygons or an areasFile) place nodes and packets, but
 * were never drawn. The map gets an "Areas" toggle: off by default, shown
 * only when areas exist, drawing every area's outline with its label under
 * the markers from GET /api/config/areas/geojson. The area chosen in the
 * area filter is highlighted, and clicking an area selects it in the filter
 * (clicking it again clears it). The toggle is remembered and deep-links as
 * #/map?areas=1.
 *
 * The two areas endpoints are mocked, so this runs against any server.
 *
 * Run: BASE_URL=http://localhost:13581 node tests/e2e/test-map-areas-overlay-e2e.js
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

// Two halves of the world, so any map view is inside one of them.
const AREAS = [{ key: 'WEST', label: 'West', layer: 'test' }, { key: 'EAST', label: 'East', layer: 'test' }];
const box = (w, e) => [[[[w, -85], [e, -85], [e, 85], [w, 85], [w, -85]]]];
const GEO = {
  type: 'FeatureCollection',
  features: [
    { type: 'Feature', properties: { code: 'WEST', label: 'West', layer: 'test' }, geometry: { type: 'MultiPolygon', coordinates: box(-179.9, 0) } },
    { type: 'Feature', properties: { code: 'EAST', label: 'East', layer: 'test' }, geometry: { type: 'MultiPolygon', coordinates: box(0, 179.9) } }
  ]
};

async function openMap(ctx, hash, areas) {
  const page = await ctx.newPage();
  page.setDefaultTimeout(10000);
  const seen = { geo: 0, nodes: [] };
  page.on('pageerror', (e) => { seen.error = e.message; });
  await page.route('**/api/config/areas', (r) => r.fulfill({ json: areas }));
  await page.route('**/api/config/areas/geojson*', (r) => { seen.geo++; r.fulfill({ json: areas.length ? GEO : { type: 'FeatureCollection', features: [] } }); });
  page.on('request', (req) => { if (/\/api\/nodes\?/.test(req.url())) seen.nodes.push(req.url()); });
  await page.goto(BASE + '/' + hash, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#leaflet-map[data-loaded="true"]', { timeout: 20000 });
  await page.waitForTimeout(600);
  return { page, seen };
}
const paths = (page) => page.evaluate(() => document.querySelectorAll('.leaflet-areasPane-pane path').length);
const labels = (page) => page.evaluate(() => [...document.querySelectorAll('.leaflet-tooltip.area-label')].map((t) => t.textContent.trim()).sort());
const toggleShown = (page) => page.evaluate(() => { const l = document.getElementById('mcAreasOverlayLabel'); return !!l && getComputedStyle(l).display !== 'none'; });

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  console.log(`\n=== map Areas overlay — E2E against ${BASE} ===`);

  {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    const { page } = await openMap(ctx, '#/map', []);
    await step('without areas there is no Areas toggle', async () => {
      assert(!await toggleShown(page), 'toggle shown with no areas');
    });
    await ctx.close();
  }

  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const { page, seen } = await openMap(ctx, '#/map', AREAS);
  await page.evaluate(() => { try { localStorage.removeItem('meshcore-map-areas'); localStorage.removeItem('meshcore-area-filter'); } catch (_) {} });

  await step('with areas the toggle is there, off by default, and nothing is drawn or fetched', async () => {
    assert(await toggleShown(page), 'toggle not shown');
    assert(!await page.isChecked('#mcAreasOverlay'), 'overlay on by default');
    assert(await paths(page) === 0, 'areas drawn while off');
    assert(seen.geo === 0, 'shapes fetched while off');
  });

  await step('turning it on draws every area, under the markers, with its label', async () => {
    await page.check('#mcAreasOverlay');
    await page.waitForFunction(() => document.querySelectorAll('.leaflet-areasPane-pane path').length === 2);
    assert(JSON.stringify(await labels(page)) === JSON.stringify(['East', 'West']), 'labels: ' + JSON.stringify(await labels(page)));
    const z = await page.evaluate(() => ({
      areas: +getComputedStyle(document.querySelector('.leaflet-areasPane-pane')).zIndex,
      markers: +getComputedStyle(document.querySelector('.leaflet-marker-pane')).zIndex
    }));
    assert(z.areas < z.markers, `areas pane z ${z.areas} should be under markers ${z.markers}`);
    assert(seen.geo === 1, 'shapes fetched ' + seen.geo + ' times');
  });

  await step('the area chosen in the area filter is highlighted', async () => {
    await page.evaluate(() => window.AreaFilter.setSelected('WEST'));
    await page.waitForTimeout(300);
    const sel = await page.evaluate(() => [...document.querySelectorAll('.leaflet-areasPane-pane path.area-selected')].length);
    assert(sel === 1, sel + ' highlighted areas');
    await page.evaluate(() => window.AreaFilter.setSelected(null));
    await page.waitForTimeout(300);
    assert(await page.evaluate(() => document.querySelectorAll('.leaflet-areasPane-pane path.area-selected').length) === 0, 'highlight stayed after clearing');
  });

  await step('clicking an area selects it in the filter and reloads nodes for it; clicking again clears', async () => {
    const pane = await page.locator('#leaflet-map').boundingBox();
    // Top-left of the view: inside an area, away from the controls and most markers.
    const x = pane.x + 70, y = pane.y + pane.height - 60;
    const before = seen.nodes.length;
    await page.mouse.click(x, y);
    await page.waitForTimeout(600);
    const picked = await page.evaluate(() => window.AreaFilter.getSelected());
    assert(picked === 'WEST' || picked === 'EAST', 'selected ' + picked);
    assert(seen.nodes.slice(before).some((u) => u.includes('area=' + picked)), 'nodes were not reloaded for ' + picked);
    await page.mouse.click(x, y);
    await page.waitForTimeout(600);
    assert(await page.evaluate(() => window.AreaFilter.getSelected()) === null, 'second click did not clear');
  });

  await step('the toggle is remembered across a reload, without fetching twice per visit', async () => {
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#leaflet-map[data-loaded="true"]', { timeout: 20000 });
    await page.waitForFunction(() => document.querySelectorAll('.leaflet-areasPane-pane path').length === 2);
    assert(await page.isChecked('#mcAreasOverlay'), 'toggle not restored');
    await page.uncheck('#mcAreasOverlay');
    await page.waitForTimeout(200);
    assert(await paths(page) === 0, 'areas still drawn after turning off');
  });

  await step('no page errors', async () => { assert(!seen.error, seen.error); });
  await ctx.close();

  {
    const c2 = await browser.newContext({ viewport: { width: 1024, height: 768 } });
    const { page: p2, seen: s2 } = await openMap(c2, '#/map?areas=1', AREAS);
    await step('#/map?areas=1 opens with the overlay on', async () => {
      await p2.waitForFunction(() => document.querySelectorAll('.leaflet-areasPane-pane path').length === 2);
      assert(await p2.isChecked('#mcAreasOverlay'), 'toggle not on from the link');
      assert(!s2.error, s2.error);
    });
    await c2.close();
  }

  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });
