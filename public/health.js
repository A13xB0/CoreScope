/* === CoreScope — health.js (Network Watch: the Health page) ===
 * Repeaters that have gone quiet, observers and all traffic against usual,
 * configured routes better or worse than usual, corridors between zones, and a
 * probe that walks the usual route between two repeaters hop by hop.
 * Data: /api/netwatch, /api/netwatch/node/{pk}, /api/netwatch/probe.
 */
(function () {
  'use strict';

  var root = null, refreshTimer = null, map = null, repeaters = null;

  function esc(s) { return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) { return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]; }); }
  function when(iso) {
    var d = new Date(iso);
    return isNaN(d) ? '—' : d.toLocaleString('en-GB', { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });
  }
  function change(now, usual) {
    if (!usual) return null;
    return Math.round((now / usual - 1) * 100);
  }
  function pctText(c) { return c == null ? '—' : (c > 0 ? '+' : c < 0 ? '−' : '') + Math.abs(c) + '%'; }
  // judge compares a last day with its usual one; with almost no usual traffic
  // a percentage means nothing, so new traffic is called new.
  function judge(last, usual, down) {
    if (down && down.length) {
      return { text: 'Observer offline', cls: 'is-warn', pct: '', note: down.join(', ') + (down.length === 1 ? ' is' : ' are') + ' offline and usually hears most of it' };
    }
    if (usual < 10) {
      return last >= 30 ? { text: 'New', cls: 'is-good', pct: '' } : { text: 'Too little traffic', cls: '', pct: '' };
    }
    var c = change(last, usual), v = verdict(c);
    v.pct = pctText(c);
    return v;
  }
  // judgeReach compares the share of adverts that got through with its usual
  // share; adverts come on every repeater's own schedule, so it is steady.
  // sentOf names what a reach counts: adverts, or channel messages where a
  // link is configured with basis "chat".
  function sentOf(r) { return r.basis === 'chat' ? 'channel messages' : 'adverts'; }
  // judgeReach uses the server's verdict: the share against the range it
  // usually falls in (wider for small samples and for shares that swing).
  var VERDICTS = { worse: { text: 'Worse', cls: 'is-bad' }, better: { text: 'Better', cls: 'is-good' }, 'new': { text: 'New', cls: '' }, normal: { text: 'Normal', cls: '' } };
  function judgeReach(r, down) {
    if (down && down.length) return judge(0, 0, down);
    var v = VERDICTS[r.verdict] || VERDICTS.normal;
    return { text: v.text, cls: v.cls };
  }
  function pc(x) { return Math.round(x * 100) + '%'; }
  // day names a UTC day, which is what each bar and step is.
  function day(iso) {
    var d = typeof iso === 'number' ? new Date(iso) : new Date(iso);
    return isNaN(d) ? '—' : d.toLocaleDateString('en-GB', { weekday: 'short', day: 'numeric', month: 'short', timeZone: 'UTC' });
  }
  // barDay is the date of bar i of n, the last being the last complete day.
  function barDay(hourIso, i, n) {
    var last = Math.floor((Date.parse(hourIso) / 3600e3 + 1) / 24) - 1;
    return day((last - (n - 1 - i)) * 86400e3);
  }
  function ordinal(n) {
    var s = ['th', 'st', 'nd', 'rd'], v = n % 100;
    return n + (s[(v - 20) % 10] || s[v] || s[0]);
  }
  // oneIn says how unlikely something was by chance.
  function oneIn(p) {
    if (p <= 1e-9) return 'less than 1 in a billion';
    if (p <= 1e-6) return 'less than 1 in a million';
    if (p <= 1e-3) return 'less than 1 in a thousand';
    return '1 in ' + Math.round(1 / p);
  }
  function every(min) { return min >= 60 ? (Math.round(min / 6) / 10) + ' h' : min + ' min'; }
  function dB(v) { return (v < 0 ? '−' : '') + Math.abs(Math.round(v)) + ' dB'; }
  // reachMore is what a reach adds below its headline: the date of a recent
  // step, the likely cause, and a target's burn.
  function reachMore(r) {
    var out = '';
    if (r.since) out += '<div class="h-note">' + (r.last < r.usual ? 'Down' : 'Up') + ' since ' + esc(day(r.since)) + '; usual is the level before that.</div>';
    if (r.cause) out += '<div class="h-note">Likely cause: <a href="#/nodes/' + encodeURIComponent(r.cause.node) + '">' + esc(r.cause.name) + '</a> (' + pc(r.cause.explains) + ' of the drop)</div>';
    if (r.target) {
      var b = { fast: ['Burning fast', 'is-bad'], slow: ['Burning slowly', 'is-warn'], ok: ['On target', 'is-good'] }[r.burn] || ['Target', ''];
      out += '<div class="h-note"><span class="h-chip ' + b[1] + '">' + b[0] + '</span> target ' + pc(r.target) + '</div>';
    }
    return out;
  }
  function range(r) { return r.verdict === 'new' || r.low == null ? '' : ' (' + Math.round(r.low * 100) + '–' + pc(r.high) + ' on a normal day)'; }
  // judgeAny judges a route or corridor direction on advert reach when it has
  // it, else on its packet count.
  function judgeAny(x) { return x.reach ? judgeReach(x.reach, x.observersDown) : judge(x.lastDay, x.usualDay, x.observersDown); }
  // reachBars draws daily shares (0..1) on a fixed 0–100% scale; days with
  // too few adverts are a faint stub.
  // The usual range is a faint band behind the bars; every bar says its day.
  function reachBars(daily, w, h, r, hourIso) {
    w = w || 140; h = h || 34;
    var n = daily.length, bw = w / Math.max(n, 1), y = function (v) { return h - Math.max(0, v) * (h - 2); };
    var band = r && r.low != null && r.verdict !== 'new'
      ? '<rect class="h-band" x="0" y="' + y(r.high).toFixed(1) + '" width="' + w + '" height="' + Math.max(1, y(r.low) - y(r.high)).toFixed(1) + '"/>' : '';
    return '<svg viewBox="0 0 ' + w + ' ' + h + '" width="' + w + '" height="' + h + '" role="img" aria-label="share that got through, by day">' + band + daily.map(function (v, i) {
      var bh = v < 0 ? 2 : Math.max(1, v * (h - 2));
      var tip = (hourIso ? barDay(hourIso, i, n) + ': ' : '') + (v < 0 ? 'too few to say' : pc(v));
      return '<rect x="' + (i * bw + 1).toFixed(1) + '" y="' + (h - bh).toFixed(1) + '" width="' + Math.max(1, bw - 2).toFixed(1) + '" height="' + bh.toFixed(1) +
        '" rx="1" fill="' + (v < 0 ? 'var(--border)' : 'var(--accent)') + '" fill-opacity="' + (i === n - 1 ? 1 : 0.45) + '"><title>' + esc(tip) + '</title></rect>';
    }).join('') + '</svg>';
  }
  function verdict(c) {
    if (c == null) return { text: 'Not enough history', cls: '' };
    if (c <= -25) return { text: 'Worse', cls: 'is-bad' };
    if (c >= 25) return { text: 'Better', cls: 'is-good' };
    return { text: 'Normal', cls: '' };
  }
  function query() { return new URLSearchParams(location.hash.split('?')[1] || ''); }

  // spark draws a series against its usual (-1 where unknown) as an SVG.
  function spark(series, expected, w, h) {
    w = w || 160; h = h || 40;
    var max = 1, i;
    for (i = 0; i < series.length; i++) max = Math.max(max, series[i], expected && expected[i] > 0 ? expected[i] : 0);
    var x = function (k) { return (k / Math.max(1, series.length - 1) * (w - 2) + 1).toFixed(1); };
    var y = function (v) { return (h - 3 - v / max * (h - 6)).toFixed(1); };
    var a = [], e = [];
    for (i = 0; i < series.length; i++) {
      a.push(x(i) + ',' + y(series[i]));
      if (expected && expected[i] >= 0) e.push(x(i) + ',' + y(expected[i]));
    }
    return '<svg viewBox="0 0 ' + w + ' ' + h + '" width="' + w + '" height="' + h + '" aria-hidden="true">' +
      (e.length ? '<polyline points="' + e.join(' ') + '" fill="none" stroke="var(--text-muted)" stroke-width="1" stroke-dasharray="3 2"/>' : '') +
      '<polyline points="' + a.join(' ') + '" fill="none" stroke="var(--accent)" stroke-width="1.6"/></svg>';
  }
  function bars(daily, w, h) {
    w = w || 140; h = h || 34;
    var max = Math.max.apply(null, daily.concat([1])), n = daily.length, bw = w / Math.max(n, 1);
    return '<svg viewBox="0 0 ' + w + ' ' + h + '" width="' + w + '" height="' + h + '" aria-hidden="true">' + daily.map(function (v, i) {
      var bh = Math.max(1, v / max * (h - 2));
      return '<rect x="' + (i * bw + 1).toFixed(1) + '" y="' + (h - bh).toFixed(1) + '" width="' + Math.max(1, bw - 2).toFixed(1) + '" height="' + bh.toFixed(1) +
        '" rx="1" fill="var(--accent)" fill-opacity="' + (i === n - 1 ? 1 : 0.45) + '"/>';
    }).join('') + '</svg>';
  }

  function shell() {
    return '<div class="h-page">' +
      '<header class="h-head"><div><h2>Network health</h2>' +
      '<p class="h-sub" id="hSub">Repeaters, routes and observers against what is usual for them.</p></div></header>' +
      '<div id="hBody"><p class="h-sub">Loading…</p></div>' +
      '<section class="h-card h-probe" id="hProbe">' +
        '<h3>Probe between two repeaters</h3>' +
        '<p class="h-sub">Follows the route packets usually take and shows every hop against its usual, so the hop where traffic drops off stands out.</p>' +
        '<form class="h-probe-form" id="hProbeForm">' +
          '<label for="hFrom">From</label><input id="hFrom" list="hRepeaters" autocomplete="off" placeholder="Repeater name">' +
          '<label for="hTo">To</label><input id="hTo" list="hRepeaters" autocomplete="off" placeholder="Repeater name">' +
          '<button type="submit" class="btn btn-primary">Probe</button>' +
          '<datalist id="hRepeaters"></datalist>' +
        '</form>' +
        '<p class="h-verdict" id="hProbeVerdict" aria-live="polite"></p>' +
        '<div id="hProbeOut"></div>' +
      '</section>' +
      '</div>';
  }

  function stat(id, value, label, alarm) {
    return '<div class="h-stat' + (alarm ? ' is-bad' : '') + '" id="' + id + '"><b>' + esc(value) + '</b><span>' + esc(label) + '</span></div>';
  }

  function incidentCard(i, open) {
    var hours = Math.round(((open ? Date.now() : Date.parse(i.end)) - Date.parse(i.start)) / 3600e3);
    var deaf = i.kind === 'not-relaying';
    var lines = [];
    if (open) {
      lines.push((deaf ? 'Stopped relaying since ' : 'Quiet since ') + when(i.start) + ' · flagged ' + when(i.flagged));
      lines.push('Usually ' + i.usual + ' relays in 6 h, ' + i.now + ' now' + (deaf ? ', though its adverts still arrive' : ', adverts every ' + i.advertEveryH + ' h') +
        (i.chance ? '. By chance ' + oneIn(i.chance) + (i.observers ? ', judged through ' + i.observers + ' observer' + (i.observers === 1 ? '' : 's') + ' up then and now' : '') : ''));
      if (i.repeats > 1) lines.push('The ' + ordinal(i.repeats) + ' time in the window');
      if (i.affects && i.affects.length) lines.push('Explains: ' + i.affects.join('; '));
    } else {
      lines.push('Back ' + when(i.end) + ' after ' + (hours >= 48 ? Math.round(hours / 24) + ' days' : hours + ' h'));
    }
    return '<div class="h-inc' + (open ? ' is-open' : '') + '" data-node="' + esc(i.node) + '">' +
      '<div class="h-inc-text"><div><a href="#/nodes/' + encodeURIComponent(i.node) + '">' + esc(i.name) + '</a> ' +
        (open ? '<span class="h-chip ' + (deaf ? 'is-warn' : 'is-bad') + '">' + (deaf ? 'Not relaying' : 'Quiet') + '</span>' : '') + '</div>' +
        lines.map(function (l) { return '<span>' + esc(l) + '</span>'; }).join('') +
        (open ? '<span class="h-deliv"></span>' : '') + '</div>' +
      '<div class="h-inc-chart" data-chart="' + esc(i.node) + '"></div></div>';
  }

  // timeline lists what happened, newest first, under a brief of the last
  // seven days.
  function timeline(st) {
    var since = Date.parse(st.hour) - 7 * 86400e3, n = {};
    st.events.forEach(function (e) {
      if (Date.parse(e.hour) < since) return;
      var k = e.kind === 'not-relaying' ? 'quiet' : e.kind === 'step' ? (e.after < e.before ? 'drop' : 'rise') : e.kind;
      n[k] = (n[k] || 0) + 1;
    });
    var words = [['quiet', 'went quiet', 'went quiet'], ['back', 'came back', 'came back'], ['drop', 'drop', 'drops'], ['rise', 'rise', 'rises'],
      ['reroute', 'reroute', 'reroutes'], ['drift', 'fading signal', 'fading signals']];
    var brief = words.filter(function (w) { return n[w[0]]; }).map(function (w) { return n[w[0]] + ' ' + (n[w[0]] === 1 ? w[1] : w[2]); });
    return '<section class="h-card" id="hTimeline"><h3>What happened</h3>' +
      '<p class="h-sub">Last 7 days: ' + (brief.length ? brief.join(', ') : 'nothing out of the ordinary') + '.</p>' +
      (st.events.length ? '<ol class="h-events">' + st.events.slice(0, 15).map(function (e) {
        return '<li class="is-' + esc(e.kind) + '"><time>' + esc(when(e.hour)) + '</time><span>' + esc(e.text) + '</span></li>';
      }).join('') + '</ol>' : '<p class="h-empty">Nothing has happened in the window.</p>') +
      '<p class="h-sub">Also as a feed for any feed reader: <a href="/api/netwatch/feed.atom">feed.atom</a>.</p></section>';
  }

  // warnings: signals fading and repeaters that go dark at night, before
  // anything fails.
  function warnings(st) {
    var items = (st.drift || []).map(function (d) {
      return '<li><a href="#/nodes/' + encodeURIComponent(d.node) + '">' + esc(d.name) + '</a>: signal at ' + esc(d.observerName) + ' down from ' + dB(d.before) + ' to ' + dB(d.now) + ' over the last 3 days</li>';
    }).concat((st.nightly || []).map(function (n) {
      return '<li><a href="#/nodes/' + encodeURIComponent(n.node) + '">' + esc(n.name) + '</a> goes dark at night (' + ('0' + n.darkFrom).slice(-2) + ':00–' + ('0' + n.darkTo).slice(-2) + ':00 UTC, ' + n.nights + ' of the last 7 nights)' +
        (n.earlier ? ', and now ' + (Math.round(n.shiftH * 10) / 10) + ' h earlier than the nights before: a battery losing ground' : '') + '</li>';
    }));
    return '<section class="h-card" id="hWarn"><h3>Early warnings</h3>' +
      (items.length ? '<ul class="h-warn">' + items.join('') + '</ul>' : '<p class="h-empty">No early warnings: no repeater’s signal is fading, and none goes dark at night.</p>') +
      '<p class="h-sub">A signal is fading when, at the same observer, the last 3 days sit 6 dB or more below the usual.</p></section>';
  }

  function render(st) {
    var body = document.getElementById('hBody');
    if (!body) return;
    var net = st.networkUsual ? Math.round(st.networkNow / st.networkUsual * 100) : null;
    var quietObs = st.observers.filter(function (o) { return o.quiet; });
    var worse = st.routes.filter(function (r) { return judgeAny(r).text === 'Worse'; });
    document.getElementById('hSub').textContent = st.judged + ' repeaters judged · last complete hour ' + when(st.hour) +
      ' · each one compared with the same ' + st.slotHours + ' hours on the previous ' + st.days + ' days';
    var html = (st.feed && st.feed.down ? '<div class="h-card h-feed" id="hFeed" role="alert"><b>The packet feed has stopped</b> since ' + esc(when(st.feed.since)) +
      '. Every observer went quiet at once, so the problem is the feed, not the mesh, and nothing is judged until it is back.</div>' : '') +
      '<div class="h-stats">' +
      stat('hQuiet', st.open.length, st.open.length === 1 ? 'repeater gone quiet' : 'repeaters gone quiet', st.open.length > 0) +
      stat('hNet', net == null ? '—' : net + '%', 'all traffic, last ' + st.slotHours + ' h vs usual', net != null && net < 50) +
      stat('hObs', quietObs.length, quietObs.length === 1 ? 'observer quiet' : 'observers quiet', quietObs.length > 0) +
      stat('hRoutes', worse.length, worse.length === 1 ? 'route worse than usual' : 'routes worse than usual', worse.length > 0) +
      '</div>';
    html += '<div class="h-grid">' +
      '<section class="h-card"><h3>Gone quiet</h3><div class="h-list" id="hOpen">' +
        (st.open.length ? st.open.map(function (i) { return incidentCard(i, true); }).join('') :
          '<p class="h-empty">Nothing has gone quiet. Every judged repeater is passing traffic and sending adverts as usual.</p>') +
      '</div><h3 class="h-gap">Back recently</h3><div class="h-list" id="hBack">' +
        (st.recent.length ? st.recent.slice(0, 8).map(function (i) { return incidentCard(i, false); }).join('') : '<p class="h-empty">None in the window.</p>') +
      '</div></section>' +
      '<section class="h-card"><h3>Where</h3><div class="h-map" id="hMap"></div><p class="h-sub">Red: gone quiet. Grey: back recently.</p></section>' +
      '</div>';
    html += '<div class="h-grid">' + timeline(st) + warnings(st) + '</div>';
    if (st.routes.length) {
      html += '<section class="h-card"><h3>Routes</h3><p class="h-sub">Packets that pass the first repeater and then the second, against usual.</p>' +
        '<div class="h-scroll"><table class="h-table" id="hRoutesTable"><thead><tr><th>Route</th><th>Last day</th><th class="num">Packets</th><th class="num">Last ' + st.slotHours + ' h</th><th>Days</th><th>From → to</th><th></th></tr></thead><tbody>' +
        st.routes.map(function (r, idx) {
          var v = judgeAny(r);
          var detail = r.reach ? pc(r.reach.last) + ' of ' + sentOf(r.reach) + ' got through, usually ' + pc(r.reach.usual) + range(r.reach) : (v.pct || '');
          return '<tr><td><b>' + esc(r.name) + '</b></td>' +
            '<td><span class="h-chip ' + v.cls + '">' + esc(v.text) + '</span> <span class="h-num">' + esc(detail) + '</span>' +
              (v.note ? '<div class="h-note">' + esc(v.note) + '</div>' : (r.reach ? reachMore(r.reach) : '')) + '</td>' +
            '<td class="num">' + r.lastDay + ' <span class="h-muted">/ ' + r.usualDay + '</span></td>' +
            '<td class="num">' + r.now + ' <span class="h-muted">/ ' + r.usual + '</span></td>' +
            '<td>' + (r.reach ? reachBars(r.reach.daily.slice(-14), 140, 34, r.reach, st.hour) : bars(r.daily.slice(-14))) + '</td>' +
            '<td>' + esc(r.fromName) + ' → ' + esc(r.toName) + '</td>' +
            '<td><button type="button" class="btn" data-probe="' + idx + '">Probe</button></td></tr>';
        }).join('') + '</tbody></table></div><p class="h-sub">Judged on the share of adverts (or channel messages, for routes set to chat) that pass the first repeater and then the second, against the range it falls in on a normal day (from the previous ' + st.days + ' days, wider when they swing or when few adverts were sent). After a step to a new level, the level before stays the usual for two weeks. Packets are the count of everything that took the route. One bar per day, the bright one is yesterday.</p></section>';
    } else {
      html += '<section class="h-card"><h3>Routes</h3><p class="h-empty">No routes configured. Name two repeaters in <code>netWatch.routes</code> in config.json to follow the traffic between them.</p></section>';
    }
    if (st.corridors.length) {
      html += '<section class="h-card"><h3>Corridors</h3><p class="h-sub" id="hCorrNote">Each way, judged on the share of adverts from repeaters in one zone that reached the other: adverts come on every repeater\u2019s own schedule, so the share is steady where traffic is not. Where a neighbour no longer sends its adverts across, the corridor is set to channel messages instead, counted from the first repeater that relayed them. One bar per day over the window (hover for the day), the bright one is yesterday; the shaded band is the range it usually falls in, and faint stubs are days with too few adverts to say. A worse day names its likely cause: the repeater whose share of the crossings fell away while the ones before it kept carrying.</p><div class="h-corridors">' + st.corridors.map(function (c) {
        var dirs = (c.directions || []).map(function (d) {
          var v = judgeAny(d);
          var head = d.reach
            ? '<div><b>' + pc(d.reach.last) + ' of ' + d.reach.lastAdverts + ' ' + sentOf(d.reach) + '</b> from ' + esc(d.from) + ' got through yesterday, usually ' + pc(d.reach.usual) + range(d.reach) +
              ' <span class="h-chip ' + v.cls + '">' + esc(v.text) + '</span></div>' + (v.note ? '' : reachMore(d.reach))
            : '<div>' + d.lastDay + ' yesterday, usually ' + d.usualDay + ' <span class="h-chip ' + v.cls + '">' + esc(v.text) + '</span> ' + v.pct + '</div>';
          return '<div class="h-dir"><div><b>' + esc(d.from) + ' → ' + esc(d.to) + '</b></div>' + head +
            (v.note ? '<div class="h-note">' + esc(v.note) + '</div>' : '') +
            (d.reroute ? '<div class="h-note">Rerouted from ' + esc(d.reroute.fromName) + ' to ' + esc(d.reroute.toName) + ' on ' + esc(day(d.reroute.since)) + '</div>' : '') +
            (d.reach ? reachBars(d.reach.daily, 260, 36, d.reach, st.hour) + '<div class="h-muted">All packets: ' + d.lastDay + ' yesterday, usually ' + d.usualDay + '</div>' : bars(d.daily, 260, 36)) +
            (d.carriers.length ? '<div class="h-muted">Carried by ' + d.carriers.map(function (k) { return esc(k.name) + ' (' + k.packets + ')'; }).join(', ') + '</div>' : '') +
            '</div>';
        }).join('');
        var total = judge(c.lastDay, c.usualDay);
        return '<div class="h-corr"><div><b>' + esc(c.name) + '</b><span class="h-muted"> ' + esc(c.a) + ' and ' + esc(c.b) + '</span></div>' + dirs +
          '<div class="h-muted">Both ways: ' + c.lastDay + ' yesterday, usually ' + c.usualDay + (total.pct ? ' (' + total.pct + ')' : '') + '</div></div>';
      }).join('') + '</div></section>';
    }
    html += '<section class="h-card"><h3>Observers</h3><div class="h-scroll"><table class="h-table"><thead><tr><th>Observer</th><th class="num">Last ' + st.slotHours + ' h</th><th class="num">Usual</th><th></th></tr></thead><tbody>' +
      st.observers.slice().sort(function (a, b) { return (b.quiet - a.quiet) || (b.usual - a.usual); }).map(function (o) {
        return '<tr><td>' + esc(o.name) + '</td><td class="num">' + o.now + '</td><td class="num">' + o.usual + '</td><td>' +
          (o.quiet ? '<span class="h-chip is-bad">Quiet</span>' : '<span class="h-chip">Normal</span>') + '</td></tr>';
      }).join('') + '</tbody></table></div><p class="h-sub">Repeaters are compared only through observers that are up both now and in their usual, so an observer going offline is never blamed on the repeaters it hears.</p></section>';
    body.innerHTML = html;

    body.querySelectorAll('[data-probe]').forEach(function (b) {
      b.addEventListener('click', function () {
        var r = st.routes[+b.getAttribute('data-probe')];
        probe(r.from, r.to, r.fromName, r.toName);
      });
    });
    body.querySelectorAll('[data-chart]').forEach(function (el) { loadChart(el); });
    drawMap(st);
  }

  function loadChart(el) {
    fetch('/api/netwatch/node/' + encodeURIComponent(el.getAttribute('data-chart'))).then(function (r) { return r.ok ? r.json() : null; }).then(function (s) {
      if (!s || !el.isConnected) return;
      var n = Math.min(72, s.relays.length);
      el.innerHTML = spark(s.relays.slice(-n), s.expected.slice(-n), 180, 42);
      var deliv = el.parentNode && el.parentNode.querySelector('.h-deliv'), dl = s.advertDelivery || [];
      if (deliv && s.advertEveryMin && dl.length && dl[dl.length - 1] >= 0) {
        deliv.textContent = 'Adverts every ' + every(s.advertEveryMin) + ' · ' + pc(dl[dl.length - 1]) + ' heard yesterday';
      }
    }).catch(function () {});
  }

  function drawMap(st) {
    var el = document.getElementById('hMap');
    if (!el || typeof L === 'undefined') return;
    if (map) { map.remove(); map = null; }
    var pts = st.open.concat(st.recent).filter(function (i) { return i.lat != null && i.lon != null; });
    map = L.map(el, { zoomControl: true, attributionControl: false, scrollWheelZoom: false });
    if (typeof window._applyTilesToNodeMap === 'function') window._applyTilesToNodeMap(map);
    else L.tileLayer('https://{s}.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png', { maxZoom: 18 }).addTo(map);
    var bad = getComputedStyle(document.documentElement).getPropertyValue('--status-red').trim() || '#ef4444';
    var muted = getComputedStyle(document.documentElement).getPropertyValue('--text-muted').trim() || '#888';
    st.recent.forEach(function (i) { if (i.lat != null) L.circleMarker([i.lat, i.lon], { radius: 5, color: '#fff', weight: 1, fillColor: muted, fillOpacity: 0.9 }).bindTooltip(esc(i.name) + ' · back').addTo(map); });
    st.open.forEach(function (i) { if (i.lat != null) L.circleMarker([i.lat, i.lon], { radius: 7, color: '#fff', weight: 1.5, fillColor: bad, fillOpacity: 1 }).bindTooltip(esc(i.name) + ' · quiet since ' + when(i.start)).addTo(map); });
    if (pts.length) map.fitBounds(pts.map(function (i) { return [i.lat, i.lon]; }), { padding: [30, 30], maxZoom: 9, animate: false });
    else map.setView([55, -3], 5);
  }

  function loadRepeaters() {
    if (repeaters) return Promise.resolve(repeaters);
    return fetch('/api/nodes?limit=3000').then(function (r) { return r.json(); }).then(function (d) {
      repeaters = (d.nodes || []).filter(function (n) { return n.role === 'repeater' || n.role === 'room'; });
      var dl = document.getElementById('hRepeaters');
      if (dl) dl.innerHTML = repeaters.map(function (n) { return '<option value="' + esc(label(n)) + '"></option>'; }).join('');
      return repeaters;
    }).catch(function () { repeaters = []; return repeaters; });
  }
  function label(n) { return (n.name || n.public_key.slice(0, 8)) + ' · ' + n.public_key.slice(0, 8); }
  function keyFor(text) {
    var t = (text || '').trim();
    if (/^[0-9a-f]{64}$/i.test(t)) return t.toLowerCase();
    var hit = (repeaters || []).filter(function (n) { return label(n) === t || n.name === t; })[0];
    return hit ? hit.public_key.toLowerCase() : null;
  }

  function probe(from, to, fromName, toName) {
    var out = document.getElementById('hProbeOut'), v = document.getElementById('hProbeVerdict');
    if (!out) return;
    if (fromName) document.getElementById('hFrom').value = fromName;
    if (toName) document.getElementById('hTo').value = toName;
    history.replaceState(null, '', '#/health?probe=' + from + ',' + to);
    v.textContent = 'Probing…'; out.innerHTML = '';
    document.getElementById('hProbe').scrollIntoView({ block: 'start', behavior: 'smooth' });
    fetch('/api/netwatch/probe?from=' + encodeURIComponent(from) + '&to=' + encodeURIComponent(to)).then(function (r) { return r.json(); }).then(function (p) {
      if (!p.route || !p.route.length) { v.textContent = p.note || 'No usual route between these two.'; return; }
      var first = p.hops.filter(function (h) { return h.quiet; })[0];
      var blind = p.hops.filter(function (h) { return h.observersDown && h.observersDown.length; })[0];
      if (first) v.textContent = 'Traffic drops off at ' + first.fromName + ' → ' + first.toName + ': ' + first.now + ' packets in the last 6 h, usually ' + first.usual + '.';
      else if (blind) v.textContent = 'Can’t tell past ' + blind.fromName + ': ' + blind.observersDown.join(', ') + ', which usually hears most of it, is offline.';
      else v.textContent = 'Every hop is carrying its usual traffic.';
      v.className = 'h-verdict' + (first ? ' is-bad' : blind ? ' is-warn' : ' is-good');
      out.innerHTML = '<ol class="h-hops">' + p.hops.map(function (h) {
        var c = change(h.now, h.usual);
        return '<li class="h-hop' + (h.quiet ? ' is-bad' : '') + '"><div class="h-hop-text"><b>' + esc(h.fromName) + ' → ' + esc(h.toName) + '</b>' +
          '<span>' + h.now + ' packets in the last 6 h, usually ' + h.usual + ' (' + pctText(c) + ')</span></div>' +
          (h.observersDown && h.observersDown.length
            ? '<span class="h-chip is-warn" title="' + esc(h.observersDown.join(', ')) + ' offline">Observer offline</span>'
            : '<span class="h-chip ' + (h.quiet ? 'is-bad' : '') + '">' + (h.quiet ? 'Quiet' : 'Normal') + '</span>') +
          spark(h.series, h.expected, 220, 40) + '</li>';
      }).join('') + '</ol><p class="h-sub">Charts: the last 72 hours per hop; dashed is usual for that hour.</p>';
    }).catch(function () { v.textContent = 'The probe failed. Try again in a moment.'; });
  }

  function load() {
    return fetch('/api/netwatch').then(function (r) {
      if (r.status === 404) return { off: true };
      return r.json();
    }).then(function (st) {
      var body = document.getElementById('hBody');
      if (!body) return;
      if (st.off) {
        body.innerHTML = '<div class="h-card h-off"><h3>Network health is off on this server</h3><p>Turn it on with <code>netWatch.enabled</code> in config.json. It needs about a week of packets before it can say what is usual.</p></div>';
        document.getElementById('hProbe').hidden = true;
        return;
      }
      if (!st.ready) { body.innerHTML = '<p class="h-sub">Learning what usual looks like from the stored packets…</p>'; return; }
      render(st);
    }).catch(function () {
      var body = document.getElementById('hBody');
      if (body) body.innerHTML = '<p class="h-empty">Could not load network health. It will try again shortly.</p>';
    });
  }

  function init(container) {
    root = container;
    root.innerHTML = shell();
    document.getElementById('hProbeForm').addEventListener('submit', function (e) {
      e.preventDefault();
      loadRepeaters().then(function () {
        var a = keyFor(document.getElementById('hFrom').value), b = keyFor(document.getElementById('hTo').value);
        if (!a || !b) { document.getElementById('hProbeVerdict').textContent = 'Pick both repeaters from the list.'; return; }
        probe(a, b);
      });
    });
    loadRepeaters();
    load().then(function () {
      var q = query().get('probe');
      if (q && q.indexOf(',') > 0) { var ks = q.split(','); probe(ks[0], ks[1]); }
    });
    refreshTimer = setInterval(load, 5 * 60 * 1000);
  }

  function destroy() {
    clearInterval(refreshTimer); refreshTimer = null;
    if (map) { map.remove(); map = null; }
    root = null;
  }

  registerPage('health', { init: init, destroy: destroy });
})();
