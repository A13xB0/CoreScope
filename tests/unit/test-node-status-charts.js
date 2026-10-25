/**
 * Repeater status charts on the node analytics page
 * (public/node-status-charts.js): which charts appear, what they plot, and
 * that the card stays hidden without data.
 *
 * Usage: node tests/unit/test-node-status-charts.js
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
const OBS = '7e'.repeat(32);

function load() {
  const made = [];
  function Chart(canvas, cfg) { this.cfg = cfg; this.canvas = canvas; made.push(this); }
  Chart.prototype.destroy = function () { this.destroyed = true; };
  const ctx = {
    console, Number, String, Math, Date, Promise, encodeURIComponent,
    escapeHtml: (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;'),
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    document: { documentElement: {} },
    Chart,
  };
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync(path.join(__dirname, '..', '..', 'public/node-status-charts.js'), 'utf8'), ctx);
  return { ns: ctx.NodeStatusCharts, made };
}

// A card whose grid records the chart cells written into it.
function card() {
  const by = { innerHTML: '' };
  const canvases = {};
  const grid = {
    _html: '',
    set innerHTML(h) { this._html = h; (h.match(/data-ns="([a-z]+)"/g) || []).forEach(m => { canvases[m.slice(9, -1)] = { id: m.slice(9, -1) }; }); },
    get innerHTML() { return this._html; },
    querySelector: (sel) => canvases[(sel.match(/data-ns="([a-z]+)"/) || [])[1]] || null,
  };
  return { hidden: true, by, grid, querySelector: (sel) => (sel === '.ns-by' ? by : grid) };
}

function sample(ts, values, extra) {
  return Object.assign({ timestamp: ts, values: values }, extra || {});
}

const full = { series: [{ observer_id: OBS, observer_name: 'The <Pole>', samples: [
  sample('2026-10-25T09:00:00Z', { uptime_secs: 700000, noise_floor: -115, last_rssi: -110, last_snr: 2, tx_queue: 0 }),
  sample('2026-10-25T10:00:00Z', { uptime_secs: 703600, noise_floor: -116, last_rssi: -114, last_snr: 1.25, tx_queue: 1 },
    { per_hour: { recv_flood: 400, recv_direct: 20, sent_flood: 90, sent_direct: 5, tx_air_secs: 30, rx_air_secs: 300, recv_errors: 150 }, dup_rate: 0.74 }),
  sample('2026-10-25T11:00:00Z', { uptime_secs: 600, noise_floor: -117 }, { reboot: true }),
] }, { observer_id: '3b'.repeat(32), samples: [sample('2026-10-25T10:00:00Z', { uptime_secs: 1 })] }] };

console.log('Node status charts');
{
  const { ns, made } = load();
  const c = card();
  assert(ns.render(c, full, 7) === true && c.hidden === false, 'shown when an observer published stats');
  const ids = made.map(m => m.canvas.id);
  assert(['uptime', 'noise', 'signal', 'airtime', 'packets', 'sent', 'dups', 'errors', 'queue'].every(i => ids.includes(i)), 'all nine charts drawn');
  assert(c.by.innerHTML.includes('The &lt;Pole>') && c.by.innerHTML.includes('#/observers/' + OBS.toUpperCase()), 'names and links the observer, escaped');
  assert(c.by.innerHTML.includes('3 status replies') && c.by.innerHTML.includes('1 other observer not shown'), 'says how many replies and other observers');
  const uptime = made.find(m => m.canvas.id === 'uptime').cfg.data.datasets;
  assert(uptime[0].data.length === 3 && Math.abs(uptime[0].data[0].y - 700000 / 86400) < 1e-9, 'uptime plotted in days');
  assert(uptime[1] && uptime[1].label === 'Reboot' && uptime[1].data.length === 1, 'reboot marked');
  const dups = made.find(m => m.canvas.id === 'dups').cfg.data.datasets[0].data;
  assert(dups.length === 1 && Math.abs(dups[0].y - 74) < 1e-9, 'duplicate rate in percent, only where it exists');
  const signal = made.find(m => m.canvas.id === 'signal').cfg;
  assert(signal.data.datasets[1].yAxisID === 'y1' && signal.options.scales.y1, 'SNR on its own axis');
  assert(c.grid.innerHTML.includes('-117 dBm'), 'heading shows the newest value');
}
{
  const { ns } = load();
  const c = card();
  const sparse = { series: [{ observer_id: OBS, samples: [sample('2026-10-25T10:00:00Z', { uptime_secs: 5, noise_floor: null })] }] };
  ns.render(c, sparse, 7);
  assert(c.grid.innerHTML.includes('data-ns="uptime"') && !c.grid.innerHTML.includes('data-ns="noise"'), 'charts without data are left out');
}
{
  const { ns } = load();
  const c = card();
  assert(ns.render(c, { series: [] }, 7) === false && c.hidden === true, 'hidden with no series');
  assert(ns.render(c, null, 7) === false && c.hidden === true, 'hidden with no response');
}
{
  const { ns, made } = load();
  ns.render(card(), full, 7);
  ns.destroy();
  assert(made.every(m => m.destroyed), 'destroy releases every chart');
}
console.log('\n' + passed + ' passed, ' + failed + ' failed');
if (failed) process.exit(1);
