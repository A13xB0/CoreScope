/* The Battery column on the nodes list (public/nodes.js + node-battery.js):
 * the header and its sort key, the cell for each kind of reading, and the
 * numeric sort with missing/stale rows last in both directions.
 *
 * Usage: node tests/unit/test-nodes-battery-column.js
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

console.log('\n=== nodes list: Battery column ===');

const src = fs.readFileSync(REPO_ROOT + '/public/nodes.js', 'utf8');

test('nodes table header has a sortable, responsive Battery column', () => {
  assert.ok(/<th[^>]*data-sort-key="battery"[^>]*data-priority="\d"[^>]*>Battery<\/th>/.test(src),
    'expected <th data-sort-key="battery" data-priority=…>Battery</th>');
});

test('renderRows emits the battery cell from n.battery', () => {
  assert.ok(/NodeBattery\.listCell\(n\.battery\)/.test(src), 'expected NodeBattery.listCell(n.battery) in renderRows');
});

function makeSandbox() {
  const ctx = {
    window: { addEventListener: () => {}, dispatchEvent: () => {} },
    document: {
      readyState: 'complete',
      createElement: () => ({ id: '', textContent: '', innerHTML: '' }),
      head: { appendChild: () => {} },
      getElementById: () => null,
      addEventListener: () => {},
      querySelectorAll: () => [],
      querySelector: () => null,
    },
    console, Date, Infinity, Math, Array, Object, String, Number, JSON, RegExp,
    Error, TypeError, parseInt, parseFloat, isNaN, isFinite,
    encodeURIComponent, decodeURIComponent,
    setTimeout: () => {}, clearTimeout: () => {},
    setInterval: () => {}, clearInterval: () => {},
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
    performance: { now: () => Date.now() },
    localStorage: (() => {
      const store = {};
      return {
        getItem: k => store[k] || null,
        setItem: (k, v) => { store[k] = String(v); },
        removeItem: k => { delete store[k]; },
      };
    })(),
    location: { hash: '' },
    CustomEvent: class CustomEvent {},
    Map, Set, Promise, URLSearchParams,
    addEventListener: () => {}, dispatchEvent: () => {},
    requestAnimationFrame: (cb) => setTimeout(cb, 0),
  };
  ctx.getHashParams = function () { return new URLSearchParams(''); };
  ctx.registerPage = () => {};
  ctx.RegionFilter = { init: () => {}, getSelected: () => null, onRegionChange: () => {} };
  ctx.onWS = () => {};
  ctx.offWS = () => {};
  ctx.invalidateApiCache = () => {};
  ctx.favStar = () => '';
  ctx.bindFavStars = () => {};
  ctx.getFavorites = () => [];
  ctx.isFavorite = () => false;
  ctx.connectWS = () => {};
  ctx.HopResolver = { init: () => {}, resolve: () => ({}), ready: () => false };
  ctx.api = () => Promise.resolve({ nodes: [], counts: {} });
  ctx.CLIENT_TTL = { nodeList: 90000, nodeDetail: 240000, nodeHealth: 240000 };
  ctx.initTabBar = () => {};
  ctx.makeColumnsResizable = () => {};
  ctx.debounce = (fn) => fn;
  vm.createContext(ctx);
  return ctx;
}
function loadInCtx(ctx, file) {
  vm.runInContext(fs.readFileSync(file, 'utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
}

const ctx = makeSandbox();
loadInCtx(ctx, REPO_ROOT + '/public/roles.js');
loadInCtx(ctx, REPO_ROOT + '/public/app.js');
loadInCtx(ctx, REPO_ROOT + '/public/node-battery.js');
loadInCtx(ctx, REPO_ROOT + '/public/nodes.js');
const NB = ctx.window.NodeBattery;
const sortNodes = ctx.window._nodesSortNodes;
const setState = ctx.window._nodesSetSortState;

test('cell: volts in the status colour', () => {
  const ok = NB.listCell({ mv: 3940, ts: '2026-10-26T10:00:00Z', status: 'ok', stale: false });
  assert.ok(ok.includes('3.94 V'), ok);
  assert.ok(ok.includes('var(--status-green-text'), ok);
  const low = NB.listCell({ mv: 3210, status: 'low', stale: false });
  assert.ok(low.includes('3.21 V') && low.includes('var(--status-yellow'), low);
  const crit = NB.listCell({ mv: 2950, status: 'critical', stale: false });
  assert.ok(crit.includes('2.95 V') && crit.includes('var(--status-red'), crit);
});

test('cell: "no battery" for 0 mV, a dash when missing or stale', () => {
  assert.ok(NB.listCell({ mv: 0, status: 'no_battery', stale: false }).includes('no battery'));
  for (const b of [undefined, null, { mv: 3900, status: 'ok', stale: true }]) {
    const html = NB.listCell(b);
    assert.ok(html.includes('—') && !html.includes(' V<'), JSON.stringify(b) + ' -> ' + html);
  }
});

const rows = () => [
  { name: 'Stale', battery: { mv: 4200, status: 'ok', stale: true } },
  { name: 'High', battery: { mv: 4100, status: 'ok', stale: false } },
  { name: 'None' },
  { name: 'Flat', battery: { mv: 2900, status: 'critical', stale: false } },
  { name: 'Bare', battery: { mv: 0, status: 'no_battery', stale: false } },
];

test('sort ascending by mV, missing and stale last', () => {
  setState({ column: 'battery', direction: 'asc' });
  const names = sortNodes(rows()).map(n => n.name);
  assert.deepStrictEqual(names.slice(0, 3), ['Bare', 'Flat', 'High']);
  assert.deepStrictEqual(names.slice(3).sort(), ['None', 'Stale']);
});

test('sort descending by mV, missing and stale still last', () => {
  setState({ column: 'battery', direction: 'desc' });
  const names = sortNodes(rows()).map(n => n.name);
  assert.deepStrictEqual(names.slice(0, 3), ['High', 'Flat', 'Bare']);
  assert.deepStrictEqual(names.slice(3).sort(), ['None', 'Stale']);
});

console.log('\n' + passed + ' passed, ' + failed + ' failed');
process.exit(failed ? 1 : 0);
