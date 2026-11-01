/* === CoreScope — node-monitors.js ===
 * Community polling, as the pages show it. Observers that run the Neighbour
 * Battery plugin claim the repeaters they poll; CoreScope elects one poller
 * per repeater under its polling policy (neighborBattery.polling) and the
 * others stand by.
 *
 *  - node page: the "Monitored by" card, a one-line Polling summary beside
 *    Battery (also in the side panel), and a banner while polling is paused
 *    network-wide — all from /api/nodes/{pk}/monitors;
 *  - observer page: the repeaters it monitors (/api/observers/{id}/monitors);
 *  - nodes list and map: the compact monitored {count, direct, …} on each
 *    /api/nodes row (listCell, listValue, matches, popupRows);
 *  - analytics: the read-only "Battery polling policy" panel
 *    (/api/guest-polling/summary).
 *
 * Everything here is read-only: the policy is changed in config only.
 */
'use strict';

(function () {
  var esc = function (s) {
    return (typeof escapeHtml === 'function') ? escapeHtml(s) : String(s == null ? '' : s).replace(/[&<>"]/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c];
    });
  };
  var ago = function (ts) { return (typeof timeAgo === 'function') ? timeAgo(ts) : String(ts); };

  var ROLES = {
    polling: { text: 'Polling', cls: 'nm-role-polling', tip: 'Elected poller: it asks this repeater for its battery once per window.' },
    standby: { text: 'Standby', cls: 'nm-role-standby', tip: 'Waits while the poller keeps the reading fresh; steps in after the window plus the grace, or once the poller misses its windows.' },
    independent: { text: 'Independent', cls: 'nm-role-muted', tip: 'Polls on its own (community sharing off, or no other observer is eligible), still within the policy limits.' },
    excluded: { text: 'Excluded', cls: 'nm-role-muted', tip: 'This repeater is excluded from polling; the observer sends nothing to it.' },
    paused: { text: 'Paused', cls: 'nm-role-paused', tip: 'Polling is paused network-wide.' },
    version_disabled: { text: 'Version disabled', cls: 'nm-role-warn', tip: 'Its plugin is below the minimum version: claims and readings are ignored until it upgrades.' },
    blocked: { text: 'Blocked', cls: 'nm-role-bad', tip: 'Blocked by this CoreScope: its claims and readings are ignored.' },
    stale: { text: 'Lapsed', cls: 'nm-role-muted', tip: 'Its claim was not renewed in the last hour.' },
    disabled: { text: 'Disabled', cls: 'nm-role-bad', tip: 'This observer may not poll.' },
  };

  function roleBadge(role) {
    var r = ROLES[role] || { text: role || '?', cls: 'nm-role-muted', tip: '' };
    return '<span class="nm-badge ' + r.cls + '" title="' + esc(r.tip) + '">' + esc(r.text) + '</span>';
  }

  function nodeLink(id, name) {
    return '<a href="#/nodes/' + encodeURIComponent(id) + '">' + esc(name || String(id).slice(0, 8)) + '</a>';
  }
  function observerLink(id, name) {
    return '<a href="#/observers/' + encodeURIComponent(String(id || '').toUpperCase()) + '">' + esc(name || String(id || '').slice(0, 8)) + '</a>';
  }

  // windowText: 3600 -> "hourly", 7200 -> "every 2 h", 1800 -> "every 30 min".
  function windowText(s) {
    s = Number(s) || 0;
    if (s === 3600) return 'hourly';
    if (s > 0 && s % 3600 === 0) return 'every ' + (s / 3600) + ' h';
    if (s > 0 && s % 60 === 0) return 'every ' + (s / 60) + ' min';
    return s > 0 ? 'every ' + s + ' s' : '';
  }
  function durationText(s) {
    s = Number(s) || 0;
    if (s >= 3600 && s % 3600 === 0) return (s / 3600) + ' h';
    if (s >= 3600) return Math.floor(s / 3600) + ' h ' + Math.round((s % 3600) / 60) + ' min';
    return Math.round(s / 60) + ' min';
  }
  function clock(ts) {
    var t = Date.parse(ts);
    if (!isFinite(t)) return '';
    var d = new Date(t);
    var hm = String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0');
    var day = (t - Date.now() > 20 * 3600000) ? d.toLocaleDateString(undefined, { weekday: 'short', day: 'numeric', month: 'short' }) + ', ' : '';
    return day + hm;
  }

  // routeHtml: "Direct", "Direct (unconfirmed)", "1 hop via <relay>".
  function routeHtml(m) {
    if (m.hops === 0 && m.effective_hops === 0) return '<span class="nm-badge nm-route">Direct</span>';
    if (m.hops === 0) {
      return '<span class="nm-badge nm-route nm-route-unconfirmed" title="Claimed direct, but neither the neighbor graph (last 24 h) nor a recent reading confirms it, so it ranks as 1 hop until it brings one.">Direct (unconfirmed)</span>';
    }
    var via = '';
    if (m.via) via = ' via ' + (m.via.length === 64 ? nodeLink(m.via, m.via_name) : esc(m.via_name || m.via));
    return '<span class="nm-badge nm-route">' + m.hops + ' hop' + (m.hops === 1 ? '' : 's') + '</span>' + via;
  }

  function routeWord(m) {
    if (!m) return '';
    return m.effective_hops === 0 ? 'direct' : (m.hops === 0 ? '1 hop' : m.hops + ' hop' + (m.hops === 1 ? '' : 's'));
  }

  // pollingLine is the one-line summary beside Battery, or '' when nobody
  // claims the repeater and it is not excluded.
  function pollingLine(data) {
    if (!data) return '';
    var mons = data.monitors || [];
    if (data.excluded) return '<span class="text-muted">Not polled: excluded by CoreScope</span>';
    if (!mons.length) return '';
    if (data.paused) {
      return '<span class="nm-paused-text">Paused (network-wide)</span>' + (data.paused_until ? ' · until ' + esc(clock(data.paused_until)) : '');
    }
    var poller = null, standby = 0;
    mons.forEach(function (m) { if (m.role === 'polling') poller = m; else if (m.role === 'standby') standby++; });
    if (!poller) {
      var indep = mons.filter(function (m) { return m.role === 'independent'; }).length;
      return indep ? indep + ' observer' + (indep === 1 ? '' : 's') + ' polling independently'
        : '<span class="text-muted">Not polled: no eligible observer</span>';
    }
    var w = windowText(data.window_s);
    var line = (w ? w.charAt(0).toUpperCase() + w.slice(1) + ' by ' : 'By ') + observerLink(poller.observer_id, poller.name) + ' (' + routeWord(poller) + ')';
    if (standby) line += ' · ' + standby + ' on standby';
    return line;
  }

  // banner is the network-wide pause notice, or ''.
  function banner(data) {
    if (!data || !data.paused) return '';
    return '<div class="nm-banner" role="status"><strong>Battery polling is paused network-wide.</strong>' +
      (data.paused_reason ? ' ' + esc(data.paused_reason) + (/[.!?]$/.test(data.paused_reason) ? '' : '.') : '') +
      (data.paused_until ? ' Resumes ' + esc(clock(data.paused_until)) + '.' : '') + '</div>';
  }

  function rowDetail(m, data) {
    var bits = [];
    if (m.role === 'version_disabled') {
      return 'Plugin ' + esc(m.plugin_version || 'unknown') + (data.min_plugin_version ? ', below the minimum ' + esc(data.min_plugin_version) : '') +
        ' · claims and readings ignored until it upgrades';
    }
    if (m.role === 'blocked') return 'Blocked by this CoreScope · claims and readings ignored';
    if (m.role === 'stale') return 'Last claimed ' + esc(ago(m.last_claim)) + ' · no longer counted';
    if (m.last_reading) bits.push('Last reading ' + esc(ago(m.last_reading)));
    else bits.push('Claimed ' + esc(ago(m.first_seen)) + ' · no readings yet');
    if (m.role === 'polling' && m.next_due) bits.push('next ' + esc(clock(m.next_due)));
    if (m.role === 'standby' && m.standby_due) bits.push('would step in after ' + esc(clock(m.standby_due)));
    if (m.readings_7d) bits.push(m.readings_7d + ' reading' + (m.readings_7d === 1 ? '' : 's') + ' (7 d)');
    if (typeof m.snr === 'number') bits.push('SNR ' + m.snr.toFixed(1));
    if (m.hops === 0 && m.effective_hops !== 0) bits.push('ranked as 1 hop until it brings one');
    return bits.join(' · ');
  }

  // cardHtml is the inside of the "Monitored by" card, or '' when there is
  // nothing to show.
  function cardHtml(data) {
    if (!data) return '';
    var mons = data.monitors || [];
    if (!mons.length && !data.excluded) return '';
    var direct = mons.filter(function (m) {
      return m.effective_hops === 0 && (m.role === 'polling' || m.role === 'standby' || m.role === 'independent');
    }).length;
    var head = mons.length + ' observer' + (mons.length === 1 ? '' : 's') +
      (data.paused ? ' · paused' : data.excluded ? ' · excluded' : ' · ' + direct + ' direct');
    var out = '<h4 title="Observers running the Neighbour Battery plugin that claim this repeater. CoreScope elects one to poll it; the others stand by. Policy ' + esc(data.policy_version || '') + '.">Monitored by <span class="nm-head-sum">' + head + '</span></h4>';
    if (data.excluded) {
      out += '<div class="nm-note">This repeater is excluded by CoreScope. Observers that claim it are listed but send nothing.</div>';
    }
    out += '<div class="nm-rows">' + mons.map(function (m) {
      return '<div class="nm-row' + (m.role === 'version_disabled' || m.role === 'blocked' || m.role === 'stale' ? ' nm-row-dim' : '') + '">' +
        '<div class="nm-row-main"><span class="nm-name">' + observerLink(m.observer_id, m.name) + '</span> ' + routeHtml(m) +
        '<span class="nm-row-role">' + roleBadge(m.role) + '</span></div>' +
        '<div class="nm-row-detail">' + rowDetail(m, data) + '</div></div>';
    }).join('') + '</div>';
    return out;
  }

  function fill(el, html) {
    if (!el || !el.isConnected) return;
    if (!html) { el.hidden = true; return; }
    var val = el.querySelector('.nb-val');
    if (val) val.innerHTML = html; else el.innerHTML = html;
    el.hidden = false;
    if (el.tagName === 'DIV' && val) el.style.display = 'contents';
  }

  // mount fills the node page's pieces from one /monitors call: line (a
  // Polling row holding .nb-val), card (the Monitored by card) and banner.
  // Any may be null; each stays hidden when there is nothing to show.
  async function mount(pubkey, els) {
    if (!pubkey || !els) return;
    var data;
    try {
      data = await api('/nodes/' + encodeURIComponent(pubkey) + '/monitors', { ttl: 60000 });
    } catch (_e) {
      return; // feature off (404) or unreachable: show nothing
    }
    fill(els.line, pollingLine(data));
    fill(els.card, cardHtml(data));
    fill(els.banner, banner(data));
  }

  // ── Nodes list and map ──────────────────────────────────────────────────

  // listValue sorts the Monitored column: count, with direct coverage ahead
  // of 1-hop only; null (last) when the row is not monitored.
  function listValue(m) {
    if (!m || !(m.count > 0)) return null;
    return m.count + (m.direct > 0 ? 1000 : 0);
  }

  function listCell(m) {
    if (!m) return '<span class="text-muted">—</span>';
    if (m.excluded) return '<span class="text-muted" title="Excluded from polling">Excluded</span>';
    if (m.paused && !(m.count > 0)) return '<span class="nm-paused-text" title="Polling is paused network-wide">Paused</span>';
    if (!(m.count > 0)) return '<span class="text-muted" title="Claimed, but no eligible observer">0</span>';
    var title = m.count + ' eligible observer' + (m.count === 1 ? '' : 's') + ', ' + m.direct + ' direct' +
      (m.poller_name ? ' · polled ' + windowText(m.window_s) + ' by ' + m.poller_name : '');
    return '<span title="' + esc(title) + '">' + m.count + ' · ' + (m.direct > 0 ? 'Direct' : '1 hop') + '</span>';
  }

  // matches is the nodes list's Monitored filter: '' any, 'yes' monitored,
  // 'direct' by a direct poller, 'no' not monitored.
  function matches(n, filter) {
    if (!filter) return true;
    var m = n && n.monitored;
    var on = !!(m && m.count > 0);
    if (filter === 'yes') return on;
    if (filter === 'direct') return on && m.direct > 0;
    if (filter === 'no') return !on;
    return true;
  }

  // popupRows adds the Monitoring line to a node's battery-layer popup.
  function popupRows(n, row) {
    var m = n && n.monitored;
    if (!m || typeof row !== 'function') return '';
    if (m.excluded) return row('Monitoring', '<span style="color:var(--text-muted)">Excluded</span>');
    if (!(m.count > 0)) return m.paused ? row('Monitoring', 'Paused') : '';
    var v = m.count + ' (' + m.direct + ' direct)';
    if (m.poller_name) v += '<br><span style="color:var(--text-muted)">Polled ' + esc(windowText(m.window_s)) + ' by ' + esc(m.poller_name) + '</span>';
    return row('Monitoring', v);
  }

  // ── Observer page ───────────────────────────────────────────────────────

  function observerHtml(data) {
    if (!data) return '';
    var nodes = data.nodes || [];
    if (!nodes.length && !data.blocked) return '';
    var polls = nodes.filter(function (x) { return x.role === 'polling'; }).length;
    var standby = nodes.filter(function (x) { return x.role === 'standby'; }).length;
    var out = '<h4>Monitors <span class="nm-head-sum">' + nodes.length + ' repeater' + (nodes.length === 1 ? '' : 's') +
      ' · polls ' + polls + ' · standby ' + standby + '</span></h4>';
    if (data.blocked) {
      return out + '<div class="nm-banner nm-banner-bad">Blocked by this CoreScope. Its claims and readings are ignored.</div>';
    }
    if (!data.version_ok && data.min_plugin_version) {
      out += '<div class="nm-banner nm-banner-warn">Plugin ' + esc(data.plugin_version || 'unknown') + ' is disabled here. Upgrade to ' +
        esc(data.min_plugin_version) + ' or later.' + (data.min_version_reason ? ' ' + esc(data.min_version_reason) : '') + '</div>';
    }
    out += '<table class="data-table nm-table"><thead><tr><th scope="col">Repeater</th><th scope="col">Route</th><th scope="col">Role</th>' +
      '<th scope="col">Last reading</th><th scope="col" title="Readings credited to this observer in the last 7 days">Readings (7 d)</th></tr></thead><tbody>' +
      nodes.map(function (x) {
        return '<tr><td>' + nodeLink(x.node_id, x.name) + '</td><td>' + routeHtml(x) + '</td><td>' + roleBadge(x.role) + '</td>' +
          '<td>' + (x.last_reading ? esc(ago(x.last_reading)) : '<span class="text-muted">—</span>') + '</td>' +
          '<td>' + (x.readings_7d || '<span class="text-muted">—</span>') + '</td></tr>';
      }).join('') + '</tbody></table>';
    return out;
  }

  async function mountObserver(el, observerId) {
    if (!el || !observerId) return;
    var data;
    try {
      data = await api('/observers/' + encodeURIComponent(observerId) + '/monitors', { ttl: 60000 });
    } catch (_e) {
      return;
    }
    fill(el, observerHtml(data));
  }

  // ── Analytics: the polling policy, read-only ────────────────────────────

  function policyHtml(body) {
    var p = (body && body.policy) || {}, s = (body && body.summary) || {};
    var out = '';
    if (p.paused) {
      out += '<div class="nm-banner" role="status"><strong>Battery polling is paused.</strong>' +
        (p.paused_reason ? ' ' + esc(p.paused_reason) : '') + (p.paused_until ? ' Resumes ' + esc(clock(p.paused_until)) + '.' : '') + '</div>';
    }
    var stat = function (v, label) { return '<div class="nm-stat"><b>' + esc(v) + '</b><span>' + esc(label) + '</span></div>'; };
    out += '<div class="node-full-card nm-policy">' +
      '<h4 title="The limits this CoreScope sets for observers that poll repeaters. They change in config only.">Battery polling policy <span class="nm-head-sum">' +
      (p.paused ? 'Paused' : 'Polling on') + ' · policy ' + esc(p.version || '') + '</span></h4>' +
      '<div class="nm-stats">' + stat(durationText(p.direct_s), 'direct poller') + stat(durationText(p.one_hop_s), '1-hop poller') +
      stat(durationText(p.grace_s), 'standby grace') + stat(String(p.max_requests || 0), 'requests per window') + '</div>' +
      '<dl class="nm-policy-list">' +
      '<dt>Monitored</dt><dd>' + (s.monitored_nodes || 0) + ' repeater' + (s.monitored_nodes === 1 ? '' : 's') + ' · ' +
        (s.direct_nodes || 0) + ' by a direct poller · ' + (s.observers || 0) + ' observer' + (s.observers === 1 ? '' : 's') + ' taking part</dd>' +
      '<dt>Takeover</dt><dd>A standby steps in after ' + (p.takeover_after || 0) + ' missed window' + (p.takeover_after === 1 ? '' : 's') + '</dd>' +
      '<dt>Excluded</dt><dd>' + ((p.excluded_nodes || []).length ? (p.excluded_nodes.length + ' repeater' + (p.excluded_nodes.length === 1 ? '' : 's') + ', at their owners\' request') : 'none') + '</dd>' +
      '<dt>Minimum plugin</dt><dd>' + (p.min_plugin_version ? esc(p.min_plugin_version) + (p.min_version_reason ? ': "' + esc(p.min_version_reason) + '"' : '') +
        (s.observers_below_min ? ' · ' + s.observers_below_min + ' observer' + (s.observers_below_min === 1 ? ' needs' : 's need') + ' to upgrade' : '') : 'none') + '</dd>' +
      '<dt>Blocked</dt><dd>' + (s.blocked_observers ? s.blocked_observers + ' observer' + (s.blocked_observers === 1 ? '' : 's') : 'none') + '</dd>' +
      '</dl>' +
      '<div class="nm-note">Observers can see the rules they are held to, and repeater owners how often they are asked. Nothing here is editable.</div>' +
      '</div>';
    return out;
  }

  async function renderPolicy(el) {
    if (!el) return;
    var body;
    try {
      body = await api('/guest-polling/summary', { ttl: 60000 });
    } catch (_e) {
      el.innerHTML = '<div class="text-muted" style="padding:24px">Battery polling is not enabled on this CoreScope.</div>';
      return;
    }
    el.innerHTML = policyHtml(body);
  }

  window.NodeMonitors = {
    mount: mount, mountObserver: mountObserver, renderPolicy: renderPolicy,
    pollingLine: pollingLine, cardHtml: cardHtml, banner: banner, observerHtml: observerHtml, policyHtml: policyHtml,
    listCell: listCell, listValue: listValue, matches: matches, popupRows: popupRows,
    windowText: windowText,
  };
})();
