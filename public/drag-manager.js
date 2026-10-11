/* drag-manager.js — Free-form panel dragging (#608 M1)
 * State machine: IDLE → PENDING → DRAGGING → IDLE
 * Pointer events on .panel-header, transform: translate() during drag,
 * snap-to-edge on release, z-index on focus, viewport % persistence.
 *
 * Panels registered with { snap: true } drop into the nearest corner
 * instead (the quadrant their centre is in): the manager hands the corner
 * to onSnap(panel, corner), which anchors it there. A hint marks the corner
 * while dragging. Holding Shift on release keeps free placement.
 */
(function () {
  'use strict';

  var DEAD_ZONE = 5;      // px — disambiguate click vs drag
  var SNAP_THRESHOLD = 20; // px — snap to edge on release
  var SNAP_MARGIN = 12;    // px — margin when snapped

  // Shared drag-state cleaner (#1568 round-1 MAJOR 2).
  // Removes every side-effect _detachFromCorner/_finalizePosition/_persist
  // can leave on a panel element so subsequent corner CSS rules win the
  // cascade. Call sites: Escape revert, responsive gate, panel corner-
  // click, reset-to-defaults. Keep this list in sync with _detachFromCorner.
  //   - removes `data-dragged` attribute
  //   - removes `is-dragging` class (defensive: normally cleared on
  //     pointerup, but cheap symmetry for races)
  //   - clears inline top/left/right/bottom/transform/position/zIndex
  //     (zIndex is bumped on every DRAGGING transition; must reset)
  //   - removes `panel-drag-<id>` from localStorage when clearStorage !== false
  function clearPanel(el, panelId, opts) {
    if (!el) return;
    el.removeAttribute('data-dragged');
    el.classList.remove('is-dragging');
    ['top', 'left', 'right', 'bottom', 'transform', 'position', 'zIndex'].forEach(function (p) {
      el.style[p] = '';
    });
    if (!opts || opts.clearStorage !== false) {
      try { localStorage.removeItem('panel-drag-' + panelId); } catch (_) { /* ignore */ }
    }
  }

  function DragManager() {
    this.state = 'IDLE';
    this.activePanel = null;
    this.startX = 0;
    this.startY = 0;
    this.panelStartX = 0;
    this.panelStartY = 0;
    this.preTransform = '';
    this.enabled = true;
    this.zCounter = 1000;
    this._panels = [];
    this._onKeyDown = this._handleKeyDown.bind(this);
    this.onSnap = null; // function (panel, corner) — set by the page
    this._hint = null;
  }

  // nearestCorner names the quadrant of the viewport the rect's centre is in.
  function nearestCorner(rect) {
    var v = (rect.top + rect.height / 2) < window.innerHeight / 2 ? 't' : 'b';
    var h = (rect.left + rect.width / 2) < window.innerWidth / 2 ? 'l' : 'r';
    return v + h;
  }

  DragManager.prototype._showHint = function (panel, corner) {
    var hint = this._hint;
    if (!hint) {
      hint = document.getElementById('panelSnapHint');
      if (!hint) {
        hint = document.createElement('div');
        hint.id = 'panelSnapHint';
        hint.className = 'panel-snap-hint';
        hint.setAttribute('aria-hidden', 'true');
      }
      this._hint = hint;
    }
    if (hint.parentNode !== panel.parentNode) panel.parentNode.appendChild(hint);
    hint.style.width = panel.offsetWidth + 'px';
    hint.style.height = panel.offsetHeight + 'px';
    hint.dataset.corner = corner;
    hint.hidden = false;
  };

  DragManager.prototype._hideHint = function () {
    if (this._hint) this._hint.hidden = true;
  };

  DragManager.prototype.register = function (panel, opts) {
    if (!panel) return;
    var header = panel.querySelector('.panel-header');
    if (!header) return;
    this._panels.push(panel);
    panel._dragSnap = !!(opts && opts.snap);
    var self = this;

    // A drag may start on the panel's title (the fold button). The header
    // captures the pointer at once, which can retarget the click a plain
    // press makes, so a press that never leaves the dead zone clicks the
    // title itself if the browser did not; the click a drag ends with is
    // swallowed so the panel does not fold as it lands.
    header.addEventListener('click', function (e) {
      if (header._dragJustEnded) {
        header._dragJustEnded = false;
        e.stopPropagation();
        e.preventDefault();
        return;
      }
      if (e.target.closest && e.target.closest('.panel-fold')) header._sawFoldClick = true;
    }, true);

    header.addEventListener('pointerdown', function (e) {
      if (!self.enabled) return;
      if (e.button !== 0) return;
      var btn = e.target.closest('button');
      if (btn && !btn.classList.contains('panel-fold')) return;
      if (!btn) e.preventDefault();
      header.setPointerCapture(e.pointerId);
      self.pressedTitle = btn;

      self.state = 'PENDING';
      self.activePanel = panel;
      self.startX = e.clientX;
      self.startY = e.clientY;

      var rect = panel.getBoundingClientRect();
      self.panelStartX = rect.left;
      self.panelStartY = rect.top;
      self.preTransform = panel.style.transform || '';
      document.addEventListener('keydown', self._onKeyDown);
    });

    header.addEventListener('pointermove', function (e) {
      if (self.state === 'IDLE') return;
      if (self.activePanel !== panel) return;
      var dx = e.clientX - self.startX;
      var dy = e.clientY - self.startY;

      if (self.state === 'PENDING') {
        if (Math.hypot(dx, dy) < DEAD_ZONE) return;
        self.state = 'DRAGGING';
        panel.classList.add('is-dragging');
        panel.style.zIndex = ++self.zCounter;
        self._detachFromCorner(panel);
      }

      panel.style.transform = 'translate(' + dx + 'px, ' + dy + 'px)';
      if (panel._dragSnap && self.onSnap) {
        if (e.shiftKey) self._hideHint();
        else self._showHint(panel, nearestCorner(panel.getBoundingClientRect()));
      }
    });

    header.addEventListener('pointerup', function (e) {
      if (self.activePanel !== panel) return;
      header.releasePointerCapture(e.pointerId);
      if (self.state === 'DRAGGING') {
        panel.classList.remove('is-dragging');
        header._dragJustEnded = true;
        setTimeout(function () { header._dragJustEnded = false; }, 0);
        self._land(panel, e.shiftKey);
      } else if (self.state === 'PENDING' && self.pressedTitle) {
        var title = self.pressedTitle;
        header._sawFoldClick = false;
        setTimeout(function () { if (!header._sawFoldClick) title.click(); }, 0);
      }
      self._reset();
    });

    header.addEventListener('pointercancel', function () {
      if (self.activePanel !== panel) return;
      panel.classList.remove('is-dragging');
      if (self.state === 'DRAGGING') {
        self._land(panel, false);
      }
      self._reset();
    });
  };

  DragManager.prototype._handleKeyDown = function (e) {
    if (e.key === 'Escape' && this.state === 'DRAGGING' && this.activePanel) {
      var panel = this.activePanel;
      panel.style.transform = this.preTransform;
      // Revert: re-attach to corner if it was cornered before
      var saved = localStorage.getItem('panel-drag-' + panel.id);
      if (!saved) {
        // Was in corner mode — restore corner CSS via shared helper.
        // No storage key existed, so clearStorage is a no-op.
        DragManager.clearPanel(panel, panel.id);
        // Re-apply corner position from M0
        var corner = localStorage.getItem('panel-corner-' + panel.id);
        if (corner) panel.setAttribute('data-position', corner);
      } else {
        // Was already dragged — revert to pre-drag position
        panel.classList.remove('is-dragging');
        panel.style.transform = 'none';
      }
      this._reset();
    }
  };

  // _land ends a drag: into the nearest corner for a snapping panel, unless
  // Shift is held, else where it was dropped.
  DragManager.prototype._land = function (panel, free) {
    this._hideHint();
    if (panel._dragSnap && this.onSnap && !free) {
      var corner = nearestCorner(panel.getBoundingClientRect());
      clearPanel(panel, panel.id);
      this.onSnap(panel, corner);
      return;
    }
    this._finalizePosition(panel);
  };

  DragManager.prototype._reset = function () {
    this._hideHint();
    this.pressedTitle = null;
    document.removeEventListener('keydown', this._onKeyDown);
    this.state = 'IDLE';
    this.activePanel = null;
  };

  DragManager.prototype._detachFromCorner = function (panel) {
    var rect = panel.getBoundingClientRect();
    panel.removeAttribute('data-position');
    panel.dataset.dragged = 'true';
    panel.style.position = 'fixed';
    panel.style.top = rect.top + 'px';
    panel.style.left = rect.left + 'px';
    panel.style.right = 'auto';
    panel.style.bottom = 'auto';
    panel.style.transform = 'none';
  };

  DragManager.prototype._finalizePosition = function (panel) {
    var rect = panel.getBoundingClientRect();
    var vw = window.innerWidth;
    var vh = window.innerHeight;

    var x = Math.max(0, Math.min(rect.left, vw - 40));
    var y = Math.max(0, Math.min(rect.top, vh - 40));

    // Snap to edge
    if (x < SNAP_THRESHOLD) x = SNAP_MARGIN;
    if (y < SNAP_THRESHOLD) y = SNAP_MARGIN;
    if (x + rect.width > vw - SNAP_THRESHOLD) x = vw - rect.width - SNAP_MARGIN;
    if (y + rect.height > vh - SNAP_THRESHOLD) y = vh - rect.height - SNAP_MARGIN;

    panel.style.top = y + 'px';
    panel.style.left = x + 'px';
    panel.style.transform = 'none';

    this._persist(panel.id, x / vw, y / vh);
  };

  DragManager.prototype._persist = function (id, xPct, yPct) {
    try {
      localStorage.setItem('panel-drag-' + id,
        JSON.stringify({ xPct: xPct, yPct: yPct }));
    } catch (_) { /* quota exceeded — silent */ }
  };

  DragManager.prototype.enable = function () { this.enabled = true; };
  DragManager.prototype.disable = function () {
    this.enabled = false;
    if (this.state !== 'IDLE' && this.activePanel) {
      this.activePanel.classList.remove('is-dragging');
      this._reset();
    }
  };

  DragManager.prototype.restorePositions = function () {
    var panels = this._panels;
    for (var i = 0; i < panels.length; i++) {
      var panel = panels[i];
      var raw = localStorage.getItem('panel-drag-' + panel.id);
      if (!raw) continue;
      try {
        var pos = JSON.parse(raw);
        var x = pos.xPct * window.innerWidth;
        var y = pos.yPct * window.innerHeight;
        panel.removeAttribute('data-position');
        panel.dataset.dragged = 'true';
        panel.style.position = 'fixed';
        panel.style.top = y + 'px';
        panel.style.left = x + 'px';
        panel.style.right = 'auto';
        panel.style.bottom = 'auto';
        panel.style.transform = 'none';
      } catch (_) {
        localStorage.removeItem('panel-drag-' + panel.id);
      }
    }
  };

  DragManager.prototype.handleResize = function () {
    var panels = document.querySelectorAll('.live-overlay[data-dragged="true"]');
    for (var i = 0; i < panels.length; i++) {
      var panel = panels[i];
      var rect = panel.getBoundingClientRect();
      var vw = window.innerWidth;
      var vh = window.innerHeight;
      var x = rect.left, y = rect.top, moved = false;
      if (rect.right > vw) { x = vw - rect.width - SNAP_MARGIN; moved = true; }
      if (rect.bottom > vh) { y = vh - rect.height - SNAP_MARGIN; moved = true; }
      if (x < 0) { x = SNAP_MARGIN; moved = true; }
      if (y < 0) { y = SNAP_MARGIN; moved = true; }
      if (moved) {
        panel.style.left = x + 'px';
        panel.style.top = y + 'px';
        this._persist(panel.id, x / vw, y / vh);
      }
    }
  };

  // Export
  window.DragManager = DragManager;
  DragManager.clearPanel = clearPanel;
})();
