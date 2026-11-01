/* fake-companion.js — a MeshCore companion radio, as far as contact
 * management goes, for tests. It speaks the companion protocol from
 * MeshCore's examples/companion_radio/MyMesh.cpp: frames in, frames out
 * (payloads only — transports add their own framing).
 *
 * Supported: CMD_APP_START (1), CMD_GET_CONTACTS (4), CMD_ADD_UPDATE_CONTACT
 * (9), CMD_REMOVE_CONTACT (15), CMD_DEVICE_QUERY (22). Anything else gets
 * ERR_CODE_UNSUPPORTED_CMD.
 *
 * Node: module.exports = FakeCompanion. Browser: window.FakeCompanion, plus
 * window.installFakeSerial(fake) which replaces navigator.serial with a port
 * that talks to it (USB framing: '<' len16 payload in, '>' len16 payload out).
 */
(function (root) {
  'use strict';

  var PUB = 32, PATH = 64, NAME = 32;
  var CONTACT_LEN = PUB + 3 + PATH + NAME + 16; // 147, after the code byte

  function hex(bytes) {
    var s = '';
    for (var i = 0; i < bytes.length; i++) s += (bytes[i] < 16 ? '0' : '') + bytes[i].toString(16);
    return s;
  }
  function u32(v) { return [v & 255, (v >>> 8) & 255, (v >>> 16) & 255, (v >>> 24) & 255]; }
  function readU32(b, o) { return (b[o] | (b[o + 1] << 8) | (b[o + 2] << 16) | (b[o + 3] << 24)) >>> 0; }

  function FakeCompanion(opts) {
    opts = opts || {};
    this.name = opts.name || 'Test Companion';
    this.maxContacts = opts.maxContacts || 100;
    this.contacts = []; // { key: hex, raw: Uint8Array(147) } in insertion order
    this.log = [];      // command codes received, in order
    this.failAddKeys = opts.failAddKeys || []; // hex keys the device refuses (ERR table full)
    (opts.contacts || []).forEach(function (c) { this.seed(c); }, this);
  }

  // seed adds a contact as if the device already had it.
  FakeCompanion.prototype.seed = function (c) {
    var raw = new Uint8Array(CONTACT_LEN);
    var key = c.key;
    for (var i = 0; i < PUB; i++) raw[i] = parseInt(key.substr(i * 2, 2), 16);
    raw[PUB] = c.type || 1;
    raw[PUB + 1] = c.flags || 0;
    raw[PUB + 2] = c.outPathLen == null ? 0xff : c.outPathLen;
    var nm = new TextEncoder().encode(c.name || '');
    raw.set(nm.subarray(0, NAME - 1), PUB + 3 + PATH);
    var o = PUB + 3 + PATH + NAME;
    raw.set(u32(c.lastAdvert || 0), o);
    raw.set(u32(Math.round((c.lat || 0) * 1e6)), o + 4);
    raw.set(u32(Math.round((c.lon || 0) * 1e6)), o + 8);
    raw.set(u32(c.lastmod || 0), o + 12);
    this.contacts.push({ key: key.toLowerCase(), raw: raw });
  };

  FakeCompanion.prototype.find = function (key) {
    for (var i = 0; i < this.contacts.length; i++) if (this.contacts[i].key === key) return i;
    return -1;
  };

  function ok() { return Uint8Array.of(0); }
  function err(code) { return Uint8Array.of(1, code); }

  // handle takes one command payload and returns the reply payloads.
  FakeCompanion.prototype.handle = function (cmd) {
    var code = cmd[0];
    this.log.push(code);
    switch (code) {
      case 1: { // CMD_APP_START -> RESP_CODE_SELF_INFO
        var nm = new TextEncoder().encode(this.name);
        var f = new Uint8Array(58 + nm.length);
        f[0] = 5; f[1] = 1;
        f.set(nm, 58);
        return [f];
      }
      case 22: { // CMD_DEVICE_QUERY -> RESP_CODE_DEVICE_INFO
        var d = new Uint8Array(80);
        d[0] = 13; d[1] = 10; d[2] = Math.floor(this.maxContacts / 2); d[3] = 40;
        var date = new TextEncoder().encode('11 Oct 2026');
        d.set(date, 8);
        var mfr = new TextEncoder().encode('Fake Board');
        d.set(mfr, 20);
        var ver = new TextEncoder().encode('v1.9.1');
        d.set(ver, 60);
        return [d];
      }
      case 4: { // CMD_GET_CONTACTS -> START, CONTACT*, END
        var out = [Uint8Array.from([2].concat(u32(this.contacts.length)))];
        var latest = 0;
        this.contacts.forEach(function (c) {
          var f = new Uint8Array(1 + CONTACT_LEN);
          f[0] = 3; f.set(c.raw, 1);
          out.push(f);
          latest = Math.max(latest, readU32(c.raw, CONTACT_LEN - 4));
        });
        out.push(Uint8Array.from([4].concat(u32(latest))));
        return out;
      }
      case 9: { // CMD_ADD_UPDATE_CONTACT
        if (cmd.length < 1 + PUB + 2 + 1) return [err(6)];
        var key = hex(cmd.subarray(1, 1 + PUB));
        var raw = new Uint8Array(CONTACT_LEN);
        raw.set(cmd.subarray(1, Math.min(cmd.length, 1 + CONTACT_LEN - 4)));
        var lastmod = cmd.length >= 1 + CONTACT_LEN ? readU32(cmd, 1 + CONTACT_LEN - 4) : 1760000000;
        raw.set(u32(lastmod), CONTACT_LEN - 4);
        var at = this.find(key);
        if (at >= 0) { this.contacts[at].raw = raw; return [ok()]; }
        if (this.failAddKeys.indexOf(key) >= 0 || this.contacts.length >= this.maxContacts) return [err(3)];
        this.contacts.push({ key: key, raw: raw });
        return [ok()];
      }
      case 15: { // CMD_REMOVE_CONTACT
        var k = hex(cmd.subarray(1, 1 + PUB));
        var i = this.find(k);
        if (i < 0) return [err(2)];
        this.contacts.splice(i, 1);
        return [ok()];
      }
      default:
        return [err(1)];
    }
  };

  // contactView decodes a stored contact for assertions.
  FakeCompanion.prototype.contactView = function (key) {
    var i = this.find(key);
    if (i < 0) return null;
    var r = this.contacts[i].raw;
    var nameBytes = r.subarray(PUB + 3 + PATH, PUB + 3 + PATH + NAME);
    var end = nameBytes.indexOf(0);
    var o = PUB + 3 + PATH + NAME;
    return {
      key: key, type: r[PUB], flags: r[PUB + 1], outPathLen: r[PUB + 2],
      outPath: hex(r.subarray(PUB + 3, PUB + 3 + PATH)),
      name: new TextDecoder().decode(end < 0 ? nameBytes : nameBytes.subarray(0, end)),
      lastAdvert: readU32(r, o), lat: (readU32(r, o + 4) | 0) / 1e6, lon: (readU32(r, o + 8) | 0) / 1e6,
    };
  };

  // installFakeSerial makes navigator.serial hand out one port wired to fake.
  function installFakeSerial(fake, opts) {
    opts = opts || {};
    var replies = [];
    var waiting = null;
    var rxBuf = [];
    function deliver(bytes) {
      if (waiting) { var w = waiting; waiting = null; w({ value: bytes, done: false }); }
      else replies.push(bytes);
    }
    function frameOut(payload) {
      var f = new Uint8Array(3 + payload.length);
      f[0] = 0x3e; f[1] = payload.length & 255; f[2] = payload.length >> 8;
      f.set(payload, 3);
      // Split into two chunks to exercise reassembly.
      var cut = Math.max(1, Math.floor(f.length / 2));
      deliver(f.subarray(0, cut));
      deliver(f.subarray(cut));
    }
    var port = {
      readable: null, writable: null,
      open: function () {
        port.readable = { getReader: function () { return {
          read: function () {
            if (replies.length) return Promise.resolve({ value: replies.shift(), done: false });
            return new Promise(function (r) { waiting = r; });
          },
          cancel: function () { if (waiting) { var w = waiting; waiting = null; w({ value: undefined, done: true }); } return Promise.resolve(); },
          releaseLock: function () {},
        }; } };
        port.writable = { getWriter: function () { return {
          write: function (chunk) {
            for (var i = 0; i < chunk.length; i++) rxBuf.push(chunk[i]);
            while (rxBuf.length >= 3 && rxBuf[0] === 0x3c) {
              var len = rxBuf[1] | (rxBuf[2] << 8);
              if (rxBuf.length < 3 + len) break;
              var payload = Uint8Array.from(rxBuf.slice(3, 3 + len));
              rxBuf = rxBuf.slice(3 + len);
              var delay = opts.delayMs || 0;
              var outs = fake.handle(payload);
              setTimeout(function (o) { o.forEach(frameOut); }, delay, outs);
            }
            return Promise.resolve();
          },
          releaseLock: function () {},
        }; } };
        return Promise.resolve();
      },
      close: function () { return Promise.resolve(); },
      getInfo: function () { return { usbVendorId: 0x303a, usbProductId: 0x1001 }; },
    };
    var serial = { requestPort: function () { return Promise.resolve(port); }, getPorts: function () { return Promise.resolve([]); } };
    try { Object.defineProperty(navigator, 'serial', { value: serial, configurable: true }); }
    catch (e) { navigator.serial = serial; }
    return port;
  }

  root.FakeCompanion = FakeCompanion;
  root.installFakeSerial = installFakeSerial;
  if (typeof module !== 'undefined' && module.exports) module.exports = FakeCompanion;
})(typeof window !== 'undefined' ? window : globalThis);
