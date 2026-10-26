/**
 * Node Analytics battery card: readings from observers that poll a node
 * directly name the observer that took them — in the "Reported by" line, as
 * one chart line per observer, and in the readings table. Loads the real
 * public/node-analytics.js in a vm sandbox.
 *
 * Usage: node tests/unit/test-node-analytics-battery-observers.js
 */
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const SRC = fs.readFileSync(path.join(__dirname, '..', '..', 'public/node-analytics.js'), 'utf8');
let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}
const flush = () => new Promise((r) => setTimeout(r, 10));
const PK = 'ab'.repeat(32);
const OBS_A = '7e'.repeat(32);
const OBS_B = '3b'.repeat(32);

function analyticsData() {
  return {
    node: { public_key: PK, name: 'West Lomond', role: 'repeater' },
    computedStats: { totalTransmissions: 100, availabilityPct: 50, signalGrade: 'A', avgPacketsPerDay: 10,
      uniqueObservers: 2, relayPct: 5, longestSilenceMs: 0 },
    timeRange: null, peerInteractions: [], activityTimeline: [], snrTrend: [], packetTypeBreakdown: [],
    observerCoverage: [], hopDistribution: [], uptimeHeatmap: [],
  };
}

function load() {
  const els = {};
  const calls = [];
  const charts = [];
  const mkEl = () => ({ style: {}, textContent: '', innerHTML: '' });
  const ctx = {
    console, Math, JSON, Date, Number, String, Array, Object, Map, Promise, isNaN, setTimeout, encodeURIComponent,
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    Chart: Object.assign(function Chart(el, cfg) { charts.push(cfg); this.destroy = () => {}; }, { defaults: {} }),
    CLIENT_TTL: {}, escapeHtml: (s) => String(s).replace(/</g, '&lt;'), timeAgo: () => '5 min ago',
    viewGeneration: () => 0,
    api: (url) => new Promise((resolve, reject) => calls.push({ url, resolve, reject })),
    NodeHopAnalytics: { load() {}, destroy() {} },
  };
  let page = null;
  ctx.registerPage = (name, p) => { if (name === 'node-analytics') page = p; };
  ctx.window = ctx;
  ctx.document = { documentElement: {}, getElementById: (id) => els[id] || (els[id] = mkEl()) };
  vm.createContext(ctx);
  vm.runInContext(SRC, ctx);
  const app = { innerHTML: '', querySelectorAll: () => [] };
  const call = (prefix) => calls.filter((c) => c.url.startsWith(prefix));
  return { page, app, els, charts, call };
}

const sample = (ts, mv, obs, name, snr, hops) => ({
  timestamp: ts, battery_mv: mv, source: 'neighbor', observer_id: obs, observer_name: name, snr, reply_hops: hops,
});
const A = { source: 'neighbor', observer_id: OBS_A, observer_name: 'Corstorphine OBS', latest_mv: 3850,
  latest_ts: '2026-10-23T18:00:00Z', samples: 2, median_snr: 7.25, reply_hops: 0 };
const B = { source: 'neighbor', observer_id: OBS_B, observer_name: 'Pentland <OBS>', latest_mv: 3200,
  latest_ts: '2026-10-23T19:00:00Z', samples: 1, median_snr: -4, reply_hops: 2 };

(async () => {
  console.log('Node Analytics: neighbour battery readings');
  const t = load();
  t.page.init(t.app, PK + '/analytics');
  t.call('/nodes/')[0].resolve(analyticsData());
  await flush();
  t.call('/nodes/' + PK + '/battery')[0].resolve({
    status: 'ok', latest_mv: 3850, latest_ts: A.latest_ts,
    thresholds: { low_mv: 3300, critical_mv: 3000 },
    samples: [
      sample('2026-10-23T17:00:00Z', 3900, OBS_A, 'Corstorphine OBS', 7.5, 0),
      sample('2026-10-23T18:00:00Z', 3850, OBS_A, 'Corstorphine OBS', 7.0, 0),
      sample('2026-10-23T19:00:00Z', 3200, OBS_B, 'Pentland <OBS>', -4, 2),
    ],
    sources: [B, A], selected: A,
  });
  await flush();

  const by = t.els.batteryReportedBy;
  assert(by.style.display === '', 'the Reported by line is shown');
  assert(by.innerHTML.includes('Corstorphine OBS') && by.innerHTML.includes('#/observers/' + OBS_A.toUpperCase()),
    'it names and links the selected observer');
  assert(by.innerHTML.includes('direct') && by.innerHTML.includes('+7.3 dB'), 'with the route and median SNR');
  assert(by.innerHTML.includes('also reported by') && by.innerHTML.includes('Pentland &lt;OBS>'),
    'and lists the other observers, escaped');

  const cfg = t.charts[t.charts.length - 1];
  const labels = cfg.data.datasets.map((d) => d.label);
  // The battery is one line (the repeater's own voltage), not a line per
  // observer; who reported each reading is in the table below.
  assert(!labels.includes('Corstorphine OBS') && !labels.includes('Pentland <OBS>'), 'no per-observer chart lines');
  const batt = cfg.data.datasets.find((d) => d.label === 'Battery');
  assert(batt && batt.data.length === 3, 'one battery line holds every reading');
  assert(labels.includes('Low threshold') && labels.includes('Critical'), 'threshold lines stay');
  // The table still names every reading's reporter.
  assert(t.els.batteryReadings.innerHTML.includes('Corstorphine OBS') && t.els.batteryReadings.innerHTML.includes('Pentland &lt;OBS>'),
    'the readings table keeps the per-reading observer');

  const badge = t.els.batteryStatusBadge.innerHTML;
  assert(badge.includes('<svg') && badge.includes('3850 mV'), 'the status badge renders its icon as markup, not text');

  const table = t.els.batteryReadings.innerHTML;
  assert(t.els.batteryReadings.style.display === '', 'the readings table is shown');
  assert(table.indexOf('Pentland') < table.indexOf('Corstorphine'), 'newest reading first');
  assert(table.includes('via 2 hops') && table.includes('-4.0 dB'), 'rows carry route and SNR');

  console.log('Node Analytics: a single reading');
  {
    const w = load();
    w.page.init(w.app, PK + '/analytics');
    w.call('/nodes/')[0].resolve(analyticsData());
    await flush();
    w.call('/nodes/' + PK + '/battery')[0].resolve({
      status: 'ok', latest_mv: 3994, thresholds: { low_mv: 3300, critical_mv: 3000 },
      samples: [sample('2026-10-23T23:56:10Z', 3994, OBS_A, 'Corstorphine OBS', -5, 0)], sources: [A], selected: A,
    });
    await flush();
    const x = w.charts[w.charts.length - 1].options.scales.x;
    assert(x.max - x.min > 0, 'the time axis has a span, so the lone point is drawn');
  }

  console.log('Node Analytics: one-hop readings with chip temperature');
  {
    const r = load();
    r.page.init(r.app, PK + '/analytics');
    r.call('/nodes/')[0].resolve(analyticsData());
    await flush();
    const RELAY = 'cd'.repeat(32);
    const one = Object.assign(sample('2026-10-25T11:29:49Z', 3999, OBS_A, 'Corstorphine OBS', -5, 1),
      { via: RELAY, via_name: 'sco-edi-garvald', temperature_c: 16.2 });
    const two = sample('2026-10-25T10:29:49Z', 3990, OBS_A, 'Corstorphine OBS', -5, 1);
    const sel = Object.assign({}, A, { reply_hops: 1, via: RELAY, via_name: 'sco-edi-garvald' });
    r.call('/nodes/' + PK + '/battery')[0].resolve({
      status: 'ok', latest_mv: 3999, thresholds: { low_mv: 3300, critical_mv: 3000 },
      samples: [two, one], sources: [sel], selected: sel,
    });
    await flush();
    assert(r.els.batteryReportedBy.innerHTML.includes('via sco-edi-garvald'), 'the route names the relay');
    const table = r.els.batteryReadings.innerHTML;
    assert(table.includes('Chip temp') && table.includes('16.2 °C') && table.includes('—'), 'readings table gains a chip temperature column');
    const cfg = r.charts[r.charts.length - 1];
    const temp = cfg.data.datasets.find((d) => d.yAxisID === 'y1');
    assert(temp && temp.data.length === 1 && temp.data[0].y === 16.2, 'temperature drawn on its own axis');
    assert(cfg.options.scales.y1.display === true, 'the temperature axis is shown');
  }

  console.log('Node Analytics: own status reports only');
  const u = load();
  u.page.init(u.app, PK + '/analytics');
  u.call('/nodes/')[0].resolve(analyticsData());
  await flush();
  const self = { source: 'self', latest_mv: 4100, latest_ts: '2026-10-23T18:00:00Z', samples: 1 };
  u.call('/nodes/' + PK + '/battery')[0].resolve({
    status: 'ok', latest_mv: 4100, thresholds: { low_mv: 3300, critical_mv: 3000 },
    samples: [{ timestamp: '2026-10-23T18:00:00Z', battery_mv: 4100, source: 'self' }],
    sources: [self], selected: self,
  });
  await flush();
  assert(u.els.batteryReportedBy.innerHTML.includes('Own status report') && !u.els.batteryReportedBy.innerHTML.includes('<a '),
    'a self report says so, with no observer link');
  assert(!u.els.batteryReportedBy.innerHTML.includes('also reported by'), 'no other sources listed');
  {
    const cfg = u.charts[u.charts.length - 1];
    assert(!cfg.data.datasets.some((d) => d.yAxisID === 'y1') && cfg.options.scales.y1.display === false,
      'no temperature axis without temperatures');
  }

  // Shared-key readings: credited to the poller ("Requested by"), or to the
  // shared key itself when unclaimed ("Via the shared key", no fake link).
  {
    const sh = load();
    sh.page.init(sh.app, PK + '/analytics');
    sh.call('/nodes/')[0].resolve(analyticsData());
    await flush();
    // Include the poller itself: it hears its own reply, but is already named
    // in "Requested by", so it must not appear again under "heard by".
    const heardBy = [{ observer_id: OBS_A, observer_name: 'Cadham Village' },
      { observer_id: 'cc'.repeat(32), observer_name: 'Pole' },
      { observer_id: 'dd'.repeat(32), observer_name: 'Repeater McRepeaterface' }];
    const claimed = { source: 'neighbor', observer_id: OBS_A, observer_name: 'Cadham Village', key_mode: 'shared',
      latest_mv: 4050, latest_ts: '2026-10-23T18:00:00Z', samples: 1, reply_hops: 1, via: OBS_B, via_name: 'West Lomond',
      heard_by: heardBy };
    sh.call('/nodes/' + PK + '/battery')[0].resolve({
      status: 'ok', latest_mv: 4050, thresholds: { low_mv: 3300, critical_mv: 3000 },
      samples: [{ timestamp: '2026-10-23T18:00:00Z', battery_mv: 4050, source: 'neighbor',
        observer_id: OBS_A, observer_name: 'Cadham Village', key_mode: 'shared', reply_hops: 1, via: OBS_B, via_name: 'West Lomond' }],
      sources: [claimed], selected: claimed,
    });
    await flush();
    const h = sh.els.batteryReportedBy.innerHTML;
    assert(h.includes('Requested by') && h.includes('Cadham Village'), 'a claimed shared reading says "Requested by" the poller');
    assert(h.includes('West Lomond') && !h.includes('direct'), 'with the claimed route, not "direct"');
    assert(h.includes('heard by') && h.includes('Pole') && h.includes('Repeater McRepeaterface'),
      'the observers that overheard the shared-key reply are listed');
    assert(h.includes('#/observers/' + 'CC'.repeat(32)), 'a heard-by observer links to its page');
    assert((h.match(/Cadham Village/g) || []).length === 1, 'the poller is named once, not repeated under "heard by"');
  }
  {
    const un = load();
    un.page.init(un.app, PK + '/analytics');
    un.call('/nodes/')[0].resolve(analyticsData());
    await flush();
    const sentinel = { source: 'neighbor', observer_id: 'f0'.repeat(32), observer_name: 'shared key', key_mode: 'shared',
      latest_mv: 4050, latest_ts: '2026-10-23T18:00:00Z', samples: 1, reply_hops: 0,
      heard_by: [{ observer_id: 'cc'.repeat(32), observer_name: 'Pole' }] };
    un.call('/nodes/' + PK + '/battery')[0].resolve({
      status: 'ok', latest_mv: 4050, thresholds: { low_mv: 3300, critical_mv: 3000 },
      samples: [{ timestamp: '2026-10-23T18:00:00Z', battery_mv: 4050, source: 'neighbor',
        observer_id: 'f0'.repeat(32), observer_name: 'shared key', key_mode: 'shared', reply_hops: 0 }],
      sources: [sentinel], selected: sentinel,
    });
    await flush();
    const h = un.els.batteryReportedBy.innerHTML;
    assert(h.includes('Via') && h.includes('the shared key'), 'an unclaimed shared reading says "Via the shared key"');
    assert(!h.includes('direct'), 'and is not called "direct"');
    // Even with no poller credited, the observers that heard it are named and linked.
    assert(h.includes('heard by') && h.includes('Pole') && h.includes('#/observers/' + 'CC'.repeat(32)),
      'the overhearing observer is still listed and linked');
  }

  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  if (failed) process.exit(1);
})();
