/**
 * The map's Battery & temperature layer (public/map-power.js): charge estimate,
 * the Only filters and Captured within limit, the tag, and the popup rows.
 *
 * Usage: node tests/unit/test-map-power.js
 */
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

function load(store) {
  const ctx = {
    console, Number, String, Math, JSON, Date, isFinite, Promise,
    localStorage: { getItem: (k) => (store || {})[k] || null, setItem: () => {} },
    escapeHtml: (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;'),
  };
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync(path.join(__dirname, '..', '..', 'public/map-power.js'), 'utf8'), ctx);
  return ctx.MapPower;
}

const NOW = Date.parse('2026-10-27T10:00:00Z');
const PK = 'ab'.repeat(32);
const iso = (h) => new Date(NOW - h * 3600000).toISOString().replace(/\.\d+Z$/, 'Z');
const node = (extra) => Object.assign({ public_key: PK, name: 'Bishop <Hill>', role: 'repeater' }, extra);
const L = { divIcon: (o) => o };

console.log('Map power layer');
{
  const mp = load();
  assert(mp.percent(4200) === 100 && mp.percent(3000) === 0, 'charge ends of the curve');
  assert(mp.percent(3910) === 66, '3.91 V is about 66%');
  assert(mp.percent(0) === null && mp.percent(null) === null, 'no voltage, no charge');
}
{
  const mp = load();
  const n = node({ battery: { mv: 3910, ts: iso(2), status: 'ok', stale: false } });
  assert(!mp.active() && mp.icon(n, false, L, NOW) === null, 'off by default: normal markers');
  assert(mp.accepts(node(), NOW), 'off: filters nothing');
}
{
  const mp = load({ 'meshcore-map-power': JSON.stringify({ show: 'both', onlyBattery: true, within: 6 }) });
  const fresh = node({ battery: { mv: 3910, ts: iso(2), status: 'ok', stale: false }, temperature: { c: 6.2, ts: iso(1) } });
  const old = node({ battery: { mv: 3910, ts: iso(9), status: 'ok', stale: false } });
  const none = node({ battery: { mv: 0, ts: iso(1), status: 'no_battery', stale: false } });
  assert(mp.narrows(), 'Only filter narrows the map');
  assert(mp.accepts(fresh, NOW), 'reading inside the limit passes');
  assert(!mp.accepts(old, NOW), 'reading older than Captured within counts as missing');
  assert(!mp.accepts(none, NOW) && !mp.accepts(node(), NOW), 'no battery fitted or no reading: hidden');
  const t = mp.icon(fresh, false, L, NOW);
  const h = t.icon.html;
  assert(h.includes('66%') && h.includes('3.91V'), 'tag shows charge as percentage and voltage');
  assert(h.includes('6°') && h.includes('2h'), 'tag shows enclosure temperature and the older age');
  assert(h.includes('mp-level-ok') && !h.includes('mp-old'), 'level class, not faded');
  assert(h.includes('Bishop &lt;Hill>'), 'name escaped in the label');
  assert(t.w > 38 && t.h === 24, 'size given for deconfliction');
  const s = mp.icon(node({ battery: { mv: 3500, ts: iso(1), status: 'low', stale: true } }), false, L, NOW).icon.html;
  assert(s.includes('mp-old') && s.includes('mp-level-critical'), 'stale reading fades; 10% is critical');
  assert(mp.icon(node({ role: 'companion', battery: fresh.battery }), false, L, NOW) === null, 'companions keep their marker');
}
{
  const mp = load({ 'meshcore-map-power': JSON.stringify({ show: 'temperature', onlyTemperature: true, within: 3 }) });
  const warm = node({ temperature: { c: 18.5, ts: iso(1) } });
  const old = node({ temperature: { c: 18.5, ts: iso(5) } });
  assert(mp.accepts(warm, NOW) && !mp.accepts(old, NOW) && !mp.accepts(node(), NOW), 'Only with a temperature reading, inside the limit');
  const h = mp.icon(warm, false, L, NOW).icon.html;
  assert(h.includes('19°') && !h.includes('%'), 'temperature only: no charge');
  const battOnly = node({ battery: { mv: 4060, ts: iso(1), status: 'ok' } });
  mp._setState({ onlyTemperature: false });
  const bh = mp.icon(battOnly, false, L, NOW).icon.html;
  assert(bh.includes('—') && !bh.includes('%'), 'battery-only repeater in Temperature mode still gets a tag, with a dash');
  assert(mp.icon(node(), false, L, NOW) === null, 'no readings at all: plain marker');
}
{
  const mp = load();
  const rows = mp.popupRows(node({ battery: { mv: 3910, ts: iso(2), status: 'ok' }, temperature: { c: 6.2, ts: iso(2) } }), NOW);
  assert(rows.includes('≈66%') && rows.includes('3.91 V') && rows.includes('2h ago'), 'popup battery row');
  assert(rows.includes('Enclosure') && rows.includes('6.2 °C'), 'popup enclosure temperature');
  assert(!/forecast|weather/i.test(rows), 'no forecast');
  assert(mp.popupRows(node({ battery: { mv: 0, status: 'no_battery' } }), NOW).includes('No battery fitted'), 'no battery fitted');
  assert(mp.popupRows(node({ role: 'companion' }), NOW) === '', 'nothing for companions');
}
console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
