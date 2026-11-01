/* Community polling on the pages (public/node-monitors.js): the node page's
 * Polling line, Monitored by card and pause banner, the nodes list's
 * Monitored cell, sort value and filter, the map popup line, the observer
 * page's Monitors list and the analytics policy panel.
 *
 * Usage: node tests/unit/test-node-monitors.js
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

const ctx = {
  console, Number, String, Math, JSON, Date, isFinite, Promise,
  escapeHtml: (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;'),
  timeAgo: () => '5 min ago',
};
ctx.window = ctx;
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(path.join(__dirname, '..', '..', 'public/node-monitors.js'), 'utf8'), ctx);
const NM = ctx.NodeMonitors;

const A = 'aa'.repeat(32), B = 'bb'.repeat(32), C = 'cc'.repeat(32), RELAY = 'dd'.repeat(32);
const later = new Date(Date.now() + 3600000).toISOString();
const mon = (o) => Object.assign({ observer_id: A, name: 'pole.lab', hops: 0, effective_hops: 0, via: null, via_name: '',
  key: 'shared', snr: 6.5, plugin_version: '0.8.0', community: true, role: 'polling', first_seen: '2026-11-01T08:00:00Z',
  last_claim: '2026-11-01T09:55:00Z', last_reading: '2026-11-01T09:00:00Z', readings_7d: 151, next_due: later, standby_due: null }, o);
const data = (o) => Object.assign({ node_id: 'ee'.repeat(32), window_s: 3600, paused: false, excluded: false,
  policy_version: 'a1b2c3d4', min_plugin_version: '0.8.2', monitors: [] }, o);

console.log('Node page');
{
  const d = data({ monitors: [
    mon({}),
    mon({ observer_id: B, name: 'Montrave', hops: 1, effective_hops: 1, via: RELAY, via_name: 'Lucklaw <Hill>', role: 'standby', standby_due: later, readings_7d: 0, last_reading: null }),
    mon({ observer_id: C, name: 'Kirkcaldy East', hops: 0, effective_hops: 1, role: 'standby' }),
    mon({ observer_id: 'ff'.repeat(32), name: 'Ladybank', role: 'version_disabled', plugin_version: '0.8.0' }),
  ] });
  const line = NM.pollingLine(d);
  assert(/^Hourly by <a href="#\/observers\/AA+">pole\.lab<\/a> \(direct\) · 2 on standby$/.test(line), 'Polling line names the poller, route and standby count: ' + line);
  const card = NM.cardHtml(d);
  assert(card.includes('Monitored by') && card.includes('4 observers · 1 direct'), 'card head counts observers and the eligible direct ones');
  assert(card.indexOf('pole.lab') < card.indexOf('Montrave') && card.indexOf('Montrave') < card.indexOf('Ladybank'), 'rows keep the ranking order');
  assert(card.includes('Direct (unconfirmed)') && card.includes('ranked as 1 hop'), 'unconfirmed direct is flagged');
  assert(card.includes('via <a href="#/nodes/' + RELAY + '">Lucklaw &lt;Hill&gt;</a>'), 'relay named, linked and escaped');
  assert(card.includes('would step in after'), 'standby shows when it would step in');
  assert(card.includes('below the minimum 0.8.2') && card.includes('Version disabled'), 'disabled version row explains itself');
  assert(NM.banner(d) === '', 'no banner while polling runs');
}
{
  const d = data({ paused: true, paused_reason: 'Airtime survey this weekend', paused_until: later,
    monitors: [mon({ role: 'paused' }), mon({ observer_id: B, role: 'paused' })] });
  assert(NM.pollingLine(d).includes('Paused (network-wide)'), 'paused line');
  assert(NM.banner(d).includes('paused network-wide') && NM.banner(d).includes('Airtime survey this weekend.') && NM.banner(d).includes('Resumes'), 'pause banner with reason and end');
  assert(NM.cardHtml(d).includes('2 observers · paused'), 'card head says paused');
}
{
  const d = data({ excluded: true, monitors: [mon({ role: 'excluded' })] });
  assert(NM.pollingLine(d).includes('excluded by CoreScope'), 'excluded line');
  assert(NM.cardHtml(d).includes('is excluded by CoreScope'), 'excluded note in the card');
  assert(NM.pollingLine(data({})) === '' && NM.cardHtml(data({})) === '', 'nothing to show for an unclaimed repeater');
  assert(NM.pollingLine(data({ monitors: [mon({ role: 'independent' })] })).includes('polling independently'), 'independent pollers');
}

console.log('Nodes list and map');
{
  assert(NM.listCell(undefined).includes('—'), 'dash when not monitored');
  assert(NM.listCell({ count: 3, direct: 1, poller_name: 'pole.lab', window_s: 3600 }).includes('3 · Direct'), 'count and direct');
  assert(NM.listCell({ count: 1, direct: 0 }).includes('1 · 1 hop'), '1-hop only');
  assert(NM.listCell({ count: 0, direct: 0, excluded: true }).includes('Excluded'), 'excluded');
  assert(NM.listCell({ count: 0, direct: 0, paused: true }).includes('Paused'), 'paused');
  const vals = [{ count: 1, direct: 1 }, { count: 3, direct: 0 }, { count: 2, direct: 1 }].map(NM.listValue);
  assert(vals[0] > vals[1] && vals[2] > vals[0], 'direct coverage sorts ahead of 1-hop only, then by count');
  assert(NM.listValue(null) === null && NM.listValue({ count: 0 }) === null, 'unmonitored rows have no sort value');
  const rows = [{ monitored: { count: 2, direct: 1 } }, { monitored: { count: 1, direct: 0 } }, {}, { monitored: { count: 0, excluded: true } }];
  const pick = (f) => rows.filter((n) => NM.matches(n, f)).length;
  assert(pick('') === 4 && pick('yes') === 2 && pick('direct') === 1 && pick('no') === 2, 'Monitored filter');
  const row = (l, v) => l + ':' + v;
  const p = NM.popupRows({ monitored: { count: 3, direct: 1, poller_name: 'pole.lab', window_s: 3600 } }, row);
  assert(p.startsWith('Monitoring:3 (1 direct)') && p.includes('Polled hourly by pole.lab'), 'map popup line: ' + p);
  assert(NM.popupRows({}, row) === '', 'no popup line without data');
  assert(NM.windowText(7200) === 'every 2 h' && NM.windowText(1800) === 'every 30 min', 'window wording');
}

console.log('Observer page');
{
  const d = { observer_id: A, name: 'pole.lab', blocked: false, plugin_version: '0.8.0', version_ok: true, nodes: [
    { node_id: 'e1'.repeat(32), name: 'West Lomond', hops: 0, effective_hops: 0, via: null, role: 'polling', last_reading: '2026-11-01T09:00:00Z', readings_7d: 20 },
    { node_id: 'e2'.repeat(32), name: 'Lucklaw Hill', hops: 1, effective_hops: 1, via: RELAY, via_name: 'West Lomond', role: 'standby', last_reading: null, readings_7d: 0 },
  ] };
  const h = NM.observerHtml(d);
  assert(h.includes('2 repeaters · polls 1 · standby 1') && h.includes('West Lomond') && h.includes('Standby'), 'Monitors table');
  const off = NM.observerHtml(Object.assign({}, d, { version_ok: false, min_plugin_version: '0.8.2' }));
  assert(off.includes('Plugin 0.8.0 is disabled here. Upgrade to 0.8.2 or later.'), 'disabled version notice');
  const blocked = NM.observerHtml(Object.assign({}, d, { blocked: true }));
  assert(blocked.includes('Blocked by this CoreScope') && !blocked.includes('<table'), 'blocked observer shows the notice instead of the table');
  assert(NM.observerHtml({ nodes: [] }) === '', 'nothing for an observer that monitors nothing');
}

console.log('Policy panel');
{
  const h = NM.policyHtml({
    policy: { version: 'a1b2c3d4', direct_s: 3600, one_hop_s: 7200, grace_s: 1800, max_requests: 2, takeover_after: 2,
      paused: false, excluded_nodes: ['e1'.repeat(32), 'e2'.repeat(32)], min_plugin_version: '0.8.2', min_version_reason: 'login storm' },
    summary: { monitored_nodes: 41, direct_nodes: 23, observers: 17, observers_below_min: 2, blocked_observers: 0 },
  });
  assert(h.includes('Polling on · policy a1b2c3d4'), 'state and version');
  assert(h.includes('<b>1 h</b><span>direct poller') && h.includes('<b>2 h</b><span>1-hop poller') && h.includes('<b>30 min</b>'), 'limits');
  assert(h.includes('41 repeaters · 23 by a direct poller · 17 observers taking part'), 'monitored summary');
  assert(h.includes('2 repeaters, at their owners') && h.includes('0.8.2: "login storm" · 2 observers need to upgrade'), 'excluded and minimum');
  assert(h.includes('<dt>Blocked</dt><dd>none</dd>'), 'blocked none');
  const p = NM.policyHtml({ policy: { paused: true, paused_reason: 'survey', version: 'b' }, summary: {} });
  assert(p.includes('Battery polling is paused.') && p.includes('Paused · policy b'), 'paused policy');
}

console.log('\n' + passed + ' passed, ' + failed + ' failed');
process.exit(failed ? 1 : 0);
