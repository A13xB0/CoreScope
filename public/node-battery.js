/* === CoreScope — node-battery.js ===
 * The Battery and Temperature lines on a node's page and side panel, from
 * /api/nodes/{pk}/battery: the latest voltage, its status, and who reported
 * it (the node itself or the observer that polled it directly), with a link
 * to the history chart; and the latest chip temperature. Each line is hidden
 * when there is no reading, or when the API marks it stale (older than
 * neighborBattery.hideAfterHours).
 */
'use strict';

(function () {
  const STATUS = {
    ok: { text: 'OK', color: 'var(--status-green-text, #51cf66)' },
    low: { text: 'Low', color: 'var(--status-yellow, #fcc419)' },
    critical: { text: 'Critical', color: 'var(--status-red, #ff6b6b)' },
    no_battery: { text: 'No battery fitted', color: 'var(--text-muted)' },
  };

  function route(s) {
    if (s.source !== 'neighbor' || s.reply_hops == null) return '';
    if (s.reply_hops === 0) return 'direct';
    if (s.via) {
      const name = escapeHtml(s.via_name || String(s.via).slice(0, 8));
      return 'via <a href="#/nodes/' + encodeURIComponent(s.via) + '">' + name + '</a>';
    }
    return 'via ' + s.reply_hops + ' hop' + (s.reply_hops === 1 ? '' : 's');
  }

  function observerLink(id, name) {
    const label = escapeHtml(name || String(id || '').slice(0, 8));
    return '<a href="#/observers/' + encodeURIComponent(String(id || '').toUpperCase()) + '">' + label + '</a>';
  }

  function reporter(s) {
    if (s.source !== 'neighbor') return 'own report';
    // A shared-key reading is the repeater's own value, decoded from whichever
    // observers heard it, so it is credited to the shared key, not an observer.
    if (s.key_mode === 'shared') return 'via the shared key';
    return 'by ' + observerLink(s.observer_id, s.observer_name);
  }

  // html returns the line for a battery response, or '' when there is none
  // or it is stale.
  function html(pubkey, data) {
    const sel = data && data.selected;
    if (!sel || data.stale) return '';
    const st = STATUS[data.status] || { text: '', color: 'inherit' };
    const parts = [];
    if (data.status === 'no_battery') {
      parts.push('<span style="color:' + st.color + '">' + st.text + '</span>');
    } else {
      parts.push('<strong style="color:' + st.color + '">' + (Number(data.latest_mv) / 1000).toFixed(2) + ' V</strong> ' + st.text);
    }
    parts.push(reporter(sel));
    const r = route(sel);
    if (r) parts.push(r);
    if (sel.latest_ts) parts.push(escapeHtml(timeAgo(sel.latest_ts)));
    const others = (data.sources || []).length - 1;
    if (others > 0) parts.push('+' + others + ' other' + (others === 1 ? '' : 's'));
    parts.push('<a href="#/nodes/' + encodeURIComponent(pubkey) + '/analytics">history</a>');
    return parts.join(' · ');
  }

  // tempHtml returns the Temperature line, or '' when there is no reading or
  // it is stale. It is the repeater's chip temperature, so it says so.
  function tempHtml(data) {
    const t = data && data.temperature;
    if (!t || t.stale || typeof t.celsius !== 'number') return '';
    const parts = ['<strong>' + t.celsius.toFixed(1) + ' °C</strong> chip'];
    if (t.source === 'advert') parts.push('from its adverts');
    else parts.push('by ' + observerLink(t.observer_id, t.observer_name));
    if (t.timestamp) parts.push(escapeHtml(timeAgo(t.timestamp)));
    return parts.join(' · ');
  }

  function show(el, line) {
    if (!el || !line || !el.isConnected) return;
    const val = el.querySelector('.nb-val');
    if (!val) return;
    val.innerHTML = line;
    el.hidden = false;
    if (el.tagName === 'DIV') el.style.display = 'contents';
  }

  // mount fills el with the Battery line and tempEl (optional) with the
  // Temperature line, each hidden until there is something to show. Each
  // holds a .nb-val element for the value; a <div> inside a <dl> is shown
  // with display:contents so it doesn't break the dt/dd grid.
  async function mount(el, pubkey, tempEl) {
    if (!el || !pubkey) return;
    let data;
    try {
      data = await api('/nodes/' + encodeURIComponent(pubkey) + '/battery?days=7', { ttl: 60000 });
    } catch (_e) {
      return; // no line rather than an error on the node page
    }
    show(el, html(pubkey, data));
    show(tempEl, tempHtml(data));
  }

  window.NodeBattery = { mount: mount, html: html, tempHtml: tempHtml };
})();
