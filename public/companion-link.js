/* companion-link.js — the MeshCore companion radio protocol, as far as
 * Tools › Companion Contacts needs it, over Web Serial (USB) or Web
 * Bluetooth. Source of truth: MeshCore examples/companion_radio/MyMesh.cpp
 * and docs/companion_protocol.md.
 *
 *   USB:  host → device  '<' len16(LE) payload
 *         device → host  '>' len16(LE) payload     (ArduinoSerialInterface)
 *   BLE:  Nordic UART service; one notification / write = one payload.
 *
 * Commands used: APP_START (1), GET_CONTACTS (4), ADD_UPDATE_CONTACT (9),
 * REMOVE_CONTACT (15), DEVICE_QUERY (22). Replies: OK (0), ERR (1, code),
 * CONTACTS_START (2), CONTACT (3), END_OF_CONTACTS (4), SELF_INFO (5),
 * DEVICE_INFO (13). Codes >= 0x80 are unsolicited pushes and are ignored.
 *
 * Everything runs in the browser: CoreScope's server is never involved in
 * talking to the radio. window.CompanionLink.
 */
(function (root) {
  'use strict';

  var CMD_APP_START = 1, CMD_GET_CONTACTS = 4, CMD_ADD_UPDATE_CONTACT = 9;
  var CMD_REMOVE_CONTACT = 15, CMD_DEVICE_QUERY = 22;
  var RESP_OK = 0, RESP_ERR = 1, RESP_CONTACTS_START = 2, RESP_CONTACT = 3;
  var RESP_END_OF_CONTACTS = 4, RESP_SELF_INFO = 5, RESP_DEVICE_INFO = 13;
  var ERR_NOT_FOUND = 2, ERR_TABLE_FULL = 3;
  var PUB = 32, PATH = 64, NAME = 32;
  var OUT_PATH_UNKNOWN = 0xff;
  // After the code byte: key, type, flags, path len, path, name, last advert,
  // lat, lon, lastmod (writeContactRespFrame).
  var CONTACT_LEN = PUB + 3 + PATH + NAME + 16;

  var NUS_SERVICE = '6e400001-b5a3-f393-e0a9-e50e24dcca9e';
  var NUS_RX = '6e400002-b5a3-f393-e0a9-e50e24dcca9e'; // app → firmware
  var NUS_TX = '6e400003-b5a3-f393-e0a9-e50e24dcca9e'; // firmware → app

  var enc = new TextEncoder();
  var dec = new TextDecoder();

  // ---- framing --------------------------------------------------------

  function encodeSerial(payload) {
    var f = new Uint8Array(3 + payload.length);
    f[0] = 0x3c; f[1] = payload.length & 255; f[2] = (payload.length >> 8) & 255;
    f.set(payload, 3);
    return f;
  }

  // SerialDecoder turns the USB byte stream back into payloads.
  function SerialDecoder() { this.buf = new Uint8Array(0); }
  SerialDecoder.prototype.push = function (chunk) {
    var b = new Uint8Array(this.buf.length + chunk.length);
    b.set(this.buf); b.set(chunk, this.buf.length);
    var out = [], i = 0;
    for (;;) {
      while (i < b.length && b[i] !== 0x3e) i++; // skip to a frame start
      if (b.length - i < 3) break;
      var len = b[i + 1] | (b[i + 2] << 8);
      if (b.length - i < 3 + len) break;
      out.push(b.slice(i + 3, i + 3 + len));
      i += 3 + len;
    }
    this.buf = b.slice(i);
    return out;
  };

  // ---- builders ---------------------------------------------------------

  function keyBytes(hex) {
    var b = new Uint8Array(PUB);
    for (var i = 0; i < PUB; i++) b[i] = parseInt(hex.substr(i * 2, 2), 16);
    return b;
  }
  function hexOf(bytes) {
    var s = '';
    for (var i = 0; i < bytes.length; i++) s += (bytes[i] < 16 ? '0' : '') + bytes[i].toString(16);
    return s;
  }

  // utf8Name encodes name into at most 31 bytes, never splitting a character.
  function utf8Name(name) {
    var out = new Uint8Array(NAME);
    var used = 0;
    var chars = Array.from(String(name || ''));
    for (var i = 0; i < chars.length; i++) {
      var b = enc.encode(chars[i]);
      if (used + b.length > NAME - 1) break;
      out.set(b, used); used += b.length;
    }
    return out;
  }

  function appStart(appName) {
    var n = enc.encode(appName || 'CoreScope');
    var f = new Uint8Array(8 + n.length);
    f[0] = CMD_APP_START;
    f.set(n, 8);
    return f;
  }
  function deviceQuery() { return Uint8Array.of(CMD_DEVICE_QUERY, 3); }
  function getContacts() { return Uint8Array.of(CMD_GET_CONTACTS); }
  function removeContact(keyHex) {
    var f = new Uint8Array(1 + PUB);
    f[0] = CMD_REMOVE_CONTACT; f.set(keyBytes(keyHex), 1);
    return f;
  }

  // addUpdateContact lays out CMD_ADD_UPDATE_CONTACT as updateContactFromFrame
  // reads it. A new contact gets OUT_PATH_UNKNOWN, so the first message to it
  // floods; c.outPathLen/outPath/flags carry an existing contact's own. lastmod
  // is sent only when known (a restore), else the device stamps its own clock.
  function addUpdateContact(c) {
    var withLastmod = c.lastmod != null;
    var f = new Uint8Array(1 + CONTACT_LEN - (withLastmod ? 0 : 4));
    var dv = new DataView(f.buffer);
    var i = 0;
    f[i++] = CMD_ADD_UPDATE_CONTACT;
    f.set(keyBytes(c.key), i); i += PUB;
    f[i++] = c.type || 1;
    f[i++] = c.flags || 0;
    f[i++] = c.outPathLen == null ? OUT_PATH_UNKNOWN : c.outPathLen;
    if (c.outPath) f.set(c.outPath.subarray(0, PATH), i);
    i += PATH;
    f.set(utf8Name(c.name), i); i += NAME;
    dv.setUint32(i, (c.lastAdvert || 0) >>> 0, true); i += 4;
    dv.setInt32(i, Math.round((c.lat || 0) * 1e6), true); i += 4;
    dv.setInt32(i, Math.round((c.lon || 0) * 1e6), true); i += 4;
    if (withLastmod) dv.setUint32(i, c.lastmod >>> 0, true);
    return f;
  }

  // ---- parsing ----------------------------------------------------------

  function cstr(bytes) {
    var end = bytes.indexOf(0);
    return dec.decode(end < 0 ? bytes : bytes.subarray(0, end));
  }

  function parseContact(p) {
    var dv = new DataView(p.buffer, p.byteOffset, p.byteLength);
    var i = 1;
    var key = hexOf(p.subarray(i, i + PUB)); i += PUB;
    var type = p[i++], flags = p[i++], outPathLen = p[i++];
    var outPath = p.slice(i, i + PATH); i += PATH;
    var name = cstr(p.subarray(i, i + NAME)); i += NAME;
    var lastAdvert = dv.getUint32(i, true); i += 4;
    var lat = dv.getInt32(i, true) / 1e6; i += 4;
    var lon = dv.getInt32(i, true) / 1e6; i += 4;
    var lastmod = dv.getUint32(i, true);
    return { key: key, type: type, flags: flags, outPathLen: outPathLen, outPath: outPath, name: name,
      lastAdvert: lastAdvert, lat: lat, lon: lon, lastmod: lastmod, hasPosition: lat !== 0 || lon !== 0 };
  }

  function parse(p) {
    var code = p[0];
    switch (code) {
      case RESP_OK: return { code: code };
      case RESP_ERR: return { code: code, err: p.length > 1 ? p[1] : 0 };
      case RESP_CONTACTS_START: return { code: code, count: new DataView(p.buffer, p.byteOffset).getUint32(1, true) };
      case RESP_CONTACT: return { code: code, contact: parseContact(p) };
      case RESP_END_OF_CONTACTS: return { code: code };
      case RESP_SELF_INFO: return { code: code, name: cstr(p.subarray(58)) };
      case RESP_DEVICE_INFO: return {
        code: code, firmwareCode: p[1], maxContacts: p[2] * 2, maxChannels: p[3],
        buildDate: cstr(p.subarray(8, 20)), manufacturer: cstr(p.subarray(20, 60)), firmware: cstr(p.subarray(60, 80)),
      };
      default: return { code: code, push: code >= 0x80 };
    }
  }

  // ---- roles ------------------------------------------------------------

  // ADV_TYPE_CHAT 1, REPEATER 2, ROOM 3, SENSOR 4 (AdvertDataHelpers.h).
  function roleToType(role) {
    var r = String(role || '').toLowerCase();
    if (r.indexOf('repeater') >= 0) return 2;
    if (r.indexOf('room') >= 0) return 3;
    if (r.indexOf('sensor') >= 0) return 4;
    return 1;
  }
  function typeToRole(type) { return ({ 1: 'companion', 2: 'repeater', 3: 'room', 4: 'sensor' })[type] || 'companion'; }

  // contactFromNode turns a /api/nodes row into a contact to add, or null.
  function contactFromNode(n) {
    var key = String(n && n.public_key || '').toLowerCase();
    if (!/^[0-9a-f]{64}$/.test(key)) return null;
    var seen = Date.parse(n.last_seen || n.last_heard || '');
    return {
      key: key, type: roleToType(n.role), name: n.name || key.slice(0, 8),
      lat: typeof n.lat === 'number' ? n.lat : 0, lon: typeof n.lon === 'number' ? n.lon : 0,
      lastAdvert: isNaN(seen) ? 0 : Math.floor(seen / 1000),
    };
  }

  // ---- client -----------------------------------------------------------

  // Client sends one command at a time and matches the next non-push reply
  // to it (docs/companion_protocol.md: "Send one command at a time").
  function Client(transport, opts) {
    this.t = transport;
    this.timeoutMs = (opts && opts.timeoutMs) || 5000;
    this.queue = [];   // replies not yet claimed
    this.waiter = null;
    var self = this;
    transport.onFrame = function (p) {
      var m = parse(p);
      if (m.push) return;
      if (self.waiter) { var w = self.waiter; self.waiter = null; w(m); }
      else self.queue.push(m);
    };
  }

  Client.prototype.next = function () {
    var self = this;
    if (this.queue.length) return Promise.resolve(this.queue.shift());
    return new Promise(function (resolve, reject) {
      var timer = setTimeout(function () { self.waiter = null; reject(new Error('the companion did not answer (timed out)')); }, self.timeoutMs);
      self.waiter = function (m) { clearTimeout(timer); resolve(m); };
    });
  };

  Client.prototype.call = function (payload) {
    var self = this;
    return this.t.send(payload).then(function () { return self.next(); });
  };

  Client.prototype.start = function () {
    var self = this, info = {};
    return this.call(appStart('CoreScope')).then(function (m) {
      if (m.code !== RESP_SELF_INFO) throw new Error('unexpected reply to APP_START (' + m.code + ')');
      info.name = m.name;
      return self.call(deviceQuery());
    }).then(function (m) {
      if (m.code === RESP_DEVICE_INFO) {
        info.maxContacts = m.maxContacts; info.firmware = m.firmware;
        info.manufacturer = m.manufacturer; info.buildDate = m.buildDate;
      }
      self.info = info;
      return info;
    });
  };

  Client.prototype.listContacts = function () {
    var self = this, out = [];
    return this.call(getContacts()).then(function (m) {
      if (m.code !== RESP_CONTACTS_START) throw new Error('unexpected reply to GET_CONTACTS (' + m.code + ')');
      function more() {
        return self.next().then(function (r) {
          if (r.code === RESP_CONTACT) { out.push(r.contact); return more(); }
          if (r.code === RESP_END_OF_CONTACTS) return out;
          throw new Error('unexpected frame while listing contacts (' + r.code + ')');
        });
      }
      return more();
    });
  };

  // addContacts sends each contact and reports {key, status}: 'added',
  // 'updated' (already on the device), 'full' or 'error'. With opts.existing
  // (the device's list), an update keeps the device's own path and flags.
  Client.prototype.addContacts = function (items, opts) {
    opts = opts || {};
    var self = this, results = [];
    var have = {};
    (opts.existing || []).forEach(function (c) { have[c.key] = c; });
    var i = 0;
    function step() {
      if (i >= items.length || (opts.shouldStop && opts.shouldStop())) return Promise.resolve(results);
      var c = items[i++];
      var prior = have[c.key];
      var send = c;
      if (prior && c.outPathLen == null) {
        send = Object.assign({}, c, { flags: prior.flags, outPathLen: prior.outPathLen, outPath: prior.outPath });
      }
      return self.call(addUpdateContact(send)).then(function (m) {
        var status = m.code === RESP_OK ? (prior ? 'updated' : 'added')
          : (m.code === RESP_ERR && m.err === ERR_TABLE_FULL ? 'full' : 'error');
        var r = { key: c.key, name: c.name, status: status };
        results.push(r);
        if (opts.onProgress) opts.onProgress(r, results.length, items.length);
        return step();
      });
    }
    return step();
  };

  // removeContacts reports {key, status}: 'removed', 'missing' or 'error'.
  Client.prototype.removeContacts = function (keys, opts) {
    opts = opts || {};
    var self = this, results = [], i = 0;
    function step() {
      if (i >= keys.length || (opts.shouldStop && opts.shouldStop())) return Promise.resolve(results);
      var k = keys[i++];
      return self.call(removeContact(k)).then(function (m) {
        var status = m.code === RESP_OK ? 'removed' : (m.code === RESP_ERR && m.err === ERR_NOT_FOUND ? 'missing' : 'error');
        var r = { key: k, status: status };
        results.push(r);
        if (opts.onProgress) opts.onProgress(r, results.length, keys.length);
        return step();
      });
    }
    return step();
  };

  Client.prototype.close = function () { return this.t.close(); };

  // ---- backup -----------------------------------------------------------

  function toBackup(contacts, meta) {
    return JSON.stringify({
      format: 'corescope-companion-contacts', version: 1, savedAt: new Date().toISOString(),
      device: meta && meta.device || null,
      contacts: contacts.map(function (c) {
        return { key: c.key, type: c.type, flags: c.flags, outPathLen: c.outPathLen, outPath: hexOf(c.outPath || new Uint8Array(0)),
          name: c.name, lastAdvert: c.lastAdvert, lat: c.lat, lon: c.lon, lastmod: c.lastmod };
      }),
    }, null, 1);
  }

  function fromBackup(text) {
    var d = JSON.parse(text);
    if (!d || d.format !== 'corescope-companion-contacts' || !Array.isArray(d.contacts)) throw new Error('not a Companion Contacts backup');
    return d.contacts.filter(function (c) { return /^[0-9a-f]{64}$/.test(c.key || ''); }).map(function (c) {
      var path = new Uint8Array(PATH);
      for (var i = 0; i < Math.min(PATH, (c.outPath || '').length / 2); i++) path[i] = parseInt(c.outPath.substr(i * 2, 2), 16);
      return { key: c.key, type: c.type, flags: c.flags, outPathLen: c.outPathLen, outPath: path, name: c.name,
        lastAdvert: c.lastAdvert, lat: c.lat, lon: c.lon, lastmod: c.lastmod };
    });
  }

  // ---- transports ---------------------------------------------------------

  function supported() {
    var n = root.navigator || {};
    return { serial: !!n.serial, bluetooth: !!n.bluetooth };
  }

  // connectSerial asks the user for a USB serial port (a USB companion build).
  function connectSerial() {
    var port, reader, writer, closed = false;
    var t = { kind: 'usb', onFrame: null, onClose: null };
    return root.navigator.serial.requestPort().then(function (p) {
      port = p;
      return port.open({ baudRate: 115200 });
    }).then(function () {
      writer = port.writable.getWriter();
      reader = port.readable.getReader();
      var decoder = new SerialDecoder();
      (function pump() {
        reader.read().then(function (r) {
          if (r.done) { if (!closed && t.onClose) t.onClose(); return; }
          decoder.push(r.value).forEach(function (f) { if (t.onFrame) t.onFrame(f); });
          pump();
        }, function () { if (!closed && t.onClose) t.onClose(); });
      })();
      t.send = function (payload) { return writer.write(encodeSerial(payload)); };
      t.close = function () {
        closed = true;
        return Promise.resolve().then(function () { return reader.cancel(); }).catch(function () {})
          .then(function () { try { reader.releaseLock(); writer.releaseLock(); } catch (e) { /* already released */ } return port.close(); })
          .catch(function () {});
      };
      return t;
    });
  }

  // connectBluetooth asks the user for a MeshCore companion over BLE.
  function connectBluetooth() {
    var device, rx, tx;
    var t = { kind: 'bluetooth', onFrame: null, onClose: null };
    return root.navigator.bluetooth.requestDevice({ filters: [{ services: [NUS_SERVICE] }] }).then(function (d) {
      device = d;
      device.addEventListener('gattserverdisconnected', function () { if (t.onClose) t.onClose(); });
      return device.gatt.connect();
    }).then(function (server) { return server.getPrimaryService(NUS_SERVICE); })
      .then(function (svc) { return Promise.all([svc.getCharacteristic(NUS_RX), svc.getCharacteristic(NUS_TX)]); })
      .then(function (chars) {
        rx = chars[0]; tx = chars[1];
        tx.addEventListener('characteristicvaluechanged', function (e) {
          var v = e.target.value;
          if (t.onFrame) t.onFrame(new Uint8Array(v.buffer.slice(v.byteOffset, v.byteOffset + v.byteLength)));
        });
        return tx.startNotifications();
      }).then(function () {
        t.name = device.name;
        // Write with response, as the protocol doc recommends; Chrome
        // negotiates the MTU and splits long writes itself.
        t.send = function (payload) { return rx.writeValueWithResponse(payload); };
        t.close = function () { try { device.gatt.disconnect(); } catch (e) { /* already gone */ } return Promise.resolve(); };
        return t;
      });
  }

  root.CompanionLink = {
    encodeSerial: encodeSerial, SerialDecoder: SerialDecoder,
    appStart: appStart, deviceQuery: deviceQuery, getContacts: getContacts,
    addUpdateContact: addUpdateContact, removeContact: removeContact, parse: parse,
    roleToType: roleToType, typeToRole: typeToRole, contactFromNode: contactFromNode,
    Client: Client, toBackup: toBackup, fromBackup: fromBackup,
    supported: supported, connectSerial: connectSerial, connectBluetooth: connectBluetooth,
    SAVE_DELAY_MS: 5000, // LAZY_CONTACTS_WRITE_DELAY: the device writes contacts this long after a change
  };
})(typeof window !== 'undefined' ? window : globalThis);
