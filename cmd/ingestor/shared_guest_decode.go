package main

import (
	"encoding/binary"
	"encoding/hex"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Shared-guest-key decoder: turn a repeater's reply, overheard by any observer
// and uploaded as a raw packet, into a battery reading — no plugin needed on
// that observer's side. A reply sent under the shared guest key is encrypted
// to the shared key, whose private half CoreScope holds (sharedGuestKey*), so
// CoreScope reproduces the secret and decrypts it the same way the poller does.
//
// Only RESPONSE packets addressed to the shared key's 1-byte hash are looked
// at — roughly 1 in 256 — so the ingest hot path is barely touched. The reply
// kind is told from its shape: a CayenneLPP telemetry reply (channel 1, a
// known type) or a RepeaterStats status reply (a plausible battery voltage).
// A login reply fits neither and is left alone.

// The shared guest key, mirrored from cmd/server/shared_guest_key.go (the
// server serves it at /api/shared-guest/key; the ingestor decodes with it).
// It is public by design; keep both copies identical.
const (
	sharedGuestKeyPublic  = "f0fbaae3284350f9ae353e617d7fb1ae143341dd1b540477c74be758066d7dd2"
	sharedGuestKeyPrivate = "8dbaa94fbcff9dddc124533648f27adb42ddd30c264ab0e4a3f4a24505f533d4"
)

const (
	sgLPPVoltage     = 116 // ×10 mV, big-endian unsigned
	sgLPPTemperature = 103 // ÷10 °C, big-endian signed
	sgTelemChannel   = 1   // channel 1 is the node itself
	sgBattMinMv      = 1000
	sgBattMaxMv      = 10000
	sgCandRefresh    = 5 * time.Minute
	sgTagTTL         = 0 // shape-detected; no tag correlation state needed
)

// sgStatusFields is the RepeaterStats struct order on the wire (little-endian),
// with the width of each field and the neighbor_status column it maps to.
var sgStatusFields = []struct {
	Key    string
	Width  int // 2 or 4
	Signed bool
}{
	{"battery_mv", 2, false}, {"tx_queue", 2, false}, {"noise_floor", 2, true}, {"last_rssi", 2, true},
	{"packets_recv", 4, false}, {"packets_sent", 4, false}, {"tx_air_secs", 4, false}, {"uptime_secs", 4, false},
	{"sent_flood", 4, false}, {"sent_direct", 4, false}, {"recv_flood", 4, false}, {"recv_direct", 4, false},
	{"err_events", 2, false}, {"last_snr_x4", 2, true}, {"direct_dups", 2, false}, {"flood_dups", 2, false},
	{"rx_air_secs", 4, false}, {"recv_errors", 4, false},
}

// sharedGuest is the process-wide decoder, set up in main once store and cfg
// exist. nil-safe: Handle does nothing when it is nil.
var sharedGuest *sharedGuestDecoder

// sgKey is one polling key CoreScope can decrypt replies under: the shared
// guest key, or a key an observer registered for itself (guest_keys.db, see
// guest_keys.go). owner is the observer that registered it, "" for the shared
// key, whose replies are credited to whoever claims the node.
type sgKey struct {
	pub   []byte
	seed  []byte
	owner string
}

// sgClaim records that an observer polls a node, by what route, so a decoded
// reply is credited to that poller ("requested by …") with the right hops —
// neither of which can be read from the overheard reply. It lapses after
// sgClaimTTL.
type sgClaim struct {
	observer string
	hops     int
	via      string
	at       time.Time
	// key is how the poller logs in ("shared", "own", "password"); only a
	// "shared" claimant can have sent a shared-key request.
	key string
	// pluginVersion is the claiming plugin's version ("" when unknown), for
	// the policy's minimum when the reply is credited.
	pluginVersion string
}

const (
	sgClaimTTL        = 3 * time.Hour
	sgMaxClaims       = 4096          // a bound so a flood of bogus claims can't grow the map without end
	sgMaxPast         = 2 * time.Hour // reject a reply tag older than this (matches the /battery path)
	sgMaxFuture       = 5 * time.Minute
	sgClaimPruneEvery = time.Hour // guest_polling_claims is pruned at most this often
)

type sharedGuestDecoder struct {
	store     *Store
	shared    sgKey
	sharedObs string // the shared key's pubkey, the fallback observer id when no one claims the node
	// keys indexes every key by the first byte of its public key, which is
	// the dest hash a reply to it carries. Swapped whole on each reload.
	keys   atomic.Pointer[map[byte][]sgKey]
	guest  *guestKeyFile // per-observer keys; nil when not configured
	mu     sync.Mutex
	cand   map[byte][][]byte // repeater pubkeys by first byte
	candAt time.Time
	// claims: node -> observer -> that observer's claim over it. Per
	// observer so a reply under an observer's own key takes that observer's
	// route; for the shared key the most recent claimant wins.
	claims      map[string]map[string]sgClaim
	nClaims     int
	claimPruned time.Time
	secrets     sync.Map // key pub + repeater pub (string bytes) -> ECDH secret
}

// secretFor is the ECDH secret between a polling key and a repeater, computed
// once and cached: both keys are fixed, so the secret is constant. This keeps
// the X25519 cost off the hot path after the first reply from each repeater.
func (d *sharedGuestDecoder) secretFor(k sgKey, pub []byte) []byte {
	id := string(k.pub) + string(pub)
	if v, ok := d.secrets.Load(id); ok {
		return v.([]byte)
	}
	s := sharedGuestSecret(k.seed, pub)
	d.secrets.Store(id, s)
	return s
}

func newSharedGuestDecoder(store *Store) *sharedGuestDecoder {
	seed, err := hex.DecodeString(sharedGuestKeyPrivate)
	if err != nil || len(seed) != 32 {
		log.Printf("[shared-guest] disabled: bad private key constant")
		return nil
	}
	pub, err := hex.DecodeString(sharedGuestKeyPublic)
	if err != nil || len(pub) != 32 {
		log.Printf("[shared-guest] disabled: bad public key constant")
		return nil
	}
	d := &sharedGuestDecoder{store: store, shared: sgKey{pub: pub, seed: seed},
		sharedObs: strings.ToLower(sharedGuestKeyPublic), claims: map[string]map[string]sgClaim{}}
	d.setKeys(nil)
	return d
}

// setKeys installs the shared key plus the given per-observer keys, and drops
// cached secrets of keys no longer in force.
func (d *sharedGuestDecoder) setKeys(registered []sgKey) {
	idx := map[byte][]sgKey{d.shared.pub[0]: {d.shared}}
	live := map[string]bool{string(d.shared.pub): true}
	for _, k := range registered {
		if live[string(k.pub)] {
			continue // the shared key, or a duplicate
		}
		live[string(k.pub)] = true
		idx[k.pub[0]] = append(idx[k.pub[0]], k)
	}
	d.keys.Store(&idx)
	d.secrets.Range(func(id, _ interface{}) bool {
		if s := id.(string); !live[s[:32]] {
			d.secrets.Delete(id)
		}
		return true
	})
}

// keysFor returns the keys whose public key starts with b.
func (d *sharedGuestDecoder) keysFor(b byte) []sgKey {
	if m := d.keys.Load(); m != nil {
		return (*m)[b]
	}
	return nil
}

// UseGuestKeys makes the decoder also decode replies under the per-observer
// keys in the guest_keys.db at path, read now and on every RefreshGuestKeys.
func (d *sharedGuestDecoder) UseGuestKeys(path string) {
	if d == nil {
		return
	}
	d.guest = &guestKeyFile{path: path}
	d.RefreshGuestKeys(time.Now())
}

// RefreshGuestKeys re-reads the per-observer keys. A failed read (the file
// not there yet, or locked) keeps the keys in force.
func (d *sharedGuestDecoder) RefreshGuestKeys(now time.Time) {
	if d == nil || d.guest == nil {
		return
	}
	keys, ok := d.guest.read(now)
	if !ok {
		return
	}
	d.setKeys(keys)
}

// HandleClaim records a poller's claim over the repeaters it polls:
// meshcore/<region>/<observer>/polling (claim v1 or v2, see
// parsePollingClaims). Claims are kept in memory for crediting decoded
// replies and in guest_polling_claims for the server's community polling
// election. Every claim is stored, whatever the policy says of the observer:
// the server decides eligibility.
func (d *sharedGuestDecoder) HandleClaim(observerID string, msg map[string]interface{}, now time.Time) {
	if d == nil {
		return
	}
	observerID, _, parsed, ok := parsePollingClaims(observerID, msg)
	if !ok {
		return
	}
	var accepted []pollingClaim
	d.mu.Lock()
	d.evictStaleClaims(now)
	for _, pc := range parsed {
		byObs := d.claims[pc.node]
		if _, exists := byObs[observerID]; !exists && d.nClaims >= sgMaxClaims {
			continue // full of fresh claims; don't grow further
		}
		c := sgClaim{observer: observerID, at: now, hops: pc.hops, key: pc.key, pluginVersion: pc.pluginVersion}
		// Only a full key can be credited as a reading's relay.
		if targetPubkeyRe.MatchString(pc.via) {
			c.via = pc.via
		}
		if byObs == nil {
			byObs = map[string]sgClaim{}
			d.claims[pc.node] = byObs
		}
		if _, exists := byObs[observerID]; !exists {
			d.nClaims++
		}
		byObs[observerID] = c
		accepted = append(accepted, pc)
	}
	prune := now.Sub(d.claimPruned) >= sgClaimPruneEvery
	if prune {
		d.claimPruned = now
	}
	d.mu.Unlock()
	if len(accepted) == 0 {
		return
	}
	log.Printf("[shared-guest] claim from %.8s for %d node(s)", observerID, len(accepted))
	if err := d.store.UpsertPollingClaims(observerID, accepted, now); err != nil {
		log.Printf("[shared-guest] store claims from %.8s: %v", observerID, err)
	}
	if prune {
		if _, err := d.store.PrunePollingClaims(now); err != nil {
			log.Printf("[shared-guest] prune claims: %v", err)
		}
	}
}

// evictStaleClaims drops lapsed claims. Caller holds d.mu.
func (d *sharedGuestDecoder) evictStaleClaims(now time.Time) {
	for node, byObs := range d.claims {
		for obs, c := range byObs {
			if now.Sub(c.at) >= sgClaimTTL {
				delete(byObs, obs)
				d.nClaims--
			}
		}
		if len(byObs) == 0 {
			delete(d.claims, node)
		}
	}
}

// claimOf is the fresh claim over a node by observer, or — with observer ""
// — the most recent fresh shared-key claim by anyone (a claimant that logs
// in with its own key or a password cannot have sent a shared-key request);
// ok=false when none exists.
func (d *sharedGuestDecoder) claimOf(node, observer string, now time.Time) (sgClaim, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var best sgClaim
	found := false
	for obs, c := range d.claims[node] {
		if now.Sub(c.at) >= sgClaimTTL || (observer != "" && obs != observer) {
			continue
		}
		if observer == "" && c.key != "" && c.key != claimKeyShared {
			continue
		}
		if !found || c.at.After(best.at) || (c.at.Equal(best.at) && c.observer < best.observer) {
			best, found = c, true
		}
	}
	return best, found
}

// candidates returns the known repeater/room pubkeys whose first byte is b,
// refreshed from the nodes table every few minutes.
func (d *sharedGuestDecoder) candidates(b byte, now time.Time) [][]byte {
	d.mu.Lock()
	cand, stale := d.cand, d.cand == nil || now.Sub(d.candAt) > sgCandRefresh
	d.mu.Unlock()
	if stale {
		// Scan the nodes table without holding the lock, so a 5-minute refresh
		// doesn't block HandleClaim or other decodes on the hot path.
		loaded := d.loadCandidates()
		d.mu.Lock()
		d.cand, d.candAt, cand = loaded, now, loaded
		d.mu.Unlock()
	}
	return cand[b]
}

func (d *sharedGuestDecoder) loadCandidates() map[byte][][]byte {
	out := map[byte][][]byte{}
	rows, err := d.store.db.Query(`SELECT public_key FROM nodes WHERE role IN ('repeater','room')`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var pkHex string
		if rows.Scan(&pkHex) != nil {
			continue
		}
		pk, err := hex.DecodeString(strings.ToLower(pkHex))
		if err != nil || len(pk) != 32 {
			continue
		}
		out[pk[0]] = append(out[pk[0]], pk)
	}
	return out
}

// Handle decodes a decoded packet if it is a reply to a key CoreScope holds
// (the shared key or an observer's registered key) and stores the reading.
// Cheap and silent for everything else.
func (d *sharedGuestDecoder) Handle(observerID string, p *DecodedPacket, now time.Time) {
	if d == nil || p == nil || p.Header.PayloadType != PayloadRESPONSE {
		return
	}
	dest, err1 := hex.DecodeString(p.Payload.DestHash)
	src, err2 := hex.DecodeString(p.Payload.SrcHash)
	if err1 != nil || err2 != nil || len(dest) != 1 || len(src) != 1 {
		return
	}
	keys := d.keysFor(dest[0])
	if len(keys) == 0 {
		return // not addressed to a key CoreScope holds
	}
	encHex := p.Payload.MAC + p.Payload.EncryptedData
	enc, err := hex.DecodeString(encHex)
	if err != nil || len(enc) <= 2 {
		return
	}
	observerID = strings.ToLower(strings.TrimSpace(observerID))
	if !targetPubkeyRe.MatchString(observerID) {
		return
	}
	cands := d.candidates(src[0], now)
	for _, k := range keys {
		for _, pub := range cands {
			plain, ok := sharedGuestDecrypt(d.secretFor(k, pub), enc)
			if !ok || len(plain) < 4 {
				continue
			}
			// The tag is the request timestamp the repeater reflects; it is the
			// same in every observer's copy of this one reply, so using it as
			// the reading's time collapses all those copies to a single row —
			// the reading is the repeater's own, not each observer's.
			tag := binary.LittleEndian.Uint32(plain[:4])
			// Reject a reply whose tag is outside the window the /battery path
			// accepts, so a poller with a bad clock can't write a far-future
			// "latest" row. With several keys and candidates a wrong pair can
			// slip past the 2-byte MAC, so try the rest rather than give up.
			tagTime := time.Unix(int64(tag), 0).UTC()
			if tag < 1_600_000_000 || tagTime.Before(now.Add(-sgMaxPast)) || tagTime.After(now.Add(sgMaxFuture)) {
				continue
			}
			nodeHex := hex.EncodeToString(pub)
			requester, claim := d.creditFor(k, nodeHex, now)
			r := d.reading(nodeHex, requester, claim, tag, now, plain[4:])
			if r == nil {
				continue // authentic reply, but not a battery/telemetry answer (e.g. a login) — leave it
			}
			// Every observer's copy of this reply folds into one reading, so note
			// who heard it separately — that is the only record of the stations the
			// reply actually reached ("heard by …").
			// The packet's content hash (as stored for the packets page) lets the
			// reading link to the reply it was decoded from.
			if err := d.store.RecordSharedHeard(r.NodeID, r.SampledAt, observerID, ComputeContentHash(p.Raw), now); err != nil {
				log.Printf("[shared-guest] record heard for %.8s: %v", r.NodeID, err)
			}
			stored, err := d.store.InsertNeighborBattery(r, 0)
			if err != nil {
				log.Printf("[shared-guest] insert for %.8s: %v", r.NodeID, err)
			} else if stored {
				log.Printf("[shared-guest] decoded %s for %.8s requested by %.8s under key %.8s (%d mV, heard by %.8s)",
					r.Method, r.NodeID, requester, hex.EncodeToString(k.pub), r.BatteryMv, observerID)
			}
			return
		}
	}
}

// creditFor names the poller a decoded reply under key k is credited to, and
// the claim its route comes from. A registered key belongs to one observer,
// so it is that observer, with its own claim's route when it has one. The
// shared key is credited to the most recent claimant of the node, or to the
// shared key itself, route unknown, when no one has claimed it.
//
// Under the community polling policy a requester that is blocked, or whose
// plugin (from its claim) is below the minimum version, is not credited: the
// reading is kept, credited to nobody (the shared key, route unknown).
func (d *sharedGuestDecoder) creditFor(k sgKey, node string, now time.Time) (string, sgClaim) {
	requester, claim := d.sharedObs, sgClaim{}
	if k.owner != "" {
		requester, claim = k.owner, sgClaim{}
		if c, ok := d.claimOf(node, k.owner, now); ok {
			claim = c
		}
	} else if c, ok := d.claimOf(node, "", now); ok {
		requester, claim = c.observer, c
	}
	if requester != d.sharedObs {
		pol := currentPollingPolicy(now)
		if pol.IsBlocked(requester) || !pol.VersionOK(claim.pluginVersion) {
			pollingDrops.logf(requester, "credit", now,
				"[shared-guest] reply for %.8s requested by %.8s kept uncredited (blocked or plugin below %s)",
				node, requester, pol.MinPluginVersion)
			return d.sharedObs, sgClaim{}
		}
	}
	return requester, claim
}

// reading builds a NeighborBatteryReading from a decrypted reply body, or nil
// when the body is not a status or telemetry answer. It is timed by the reply's
// tag (so every observer's copy of one reply is the same row), credited to the
// claimant with its route (hops/via), and stamped received at the real arrival
// time.
func (d *sharedGuestDecoder) reading(nodeHex, requester string, claim sgClaim, tag uint32, now time.Time, body []byte) *NeighborBatteryReading {
	at := time.Unix(int64(tag), 0).UTC().Format(batteryTimeLayout)
	base := func(mv int, method string) *NeighborBatteryReading {
		r := &NeighborBatteryReading{
			NodeID: nodeHex, ObserverID: requester, BatteryMv: mv, Method: method,
			Source: "poll", KeyMode: "shared", ReplyHops: claim.hops, Via: claim.via,
			SampledAt: at, ReceivedAt: now.UTC().Format(batteryTimeLayout),
		}
		return r
	}
	// Telemetry first: CayenneLPP for the node itself (channel 1).
	if mv, temp, ok := sgParseTelemetry(body); ok {
		r := base(mv, "telemetry")
		r.TemperatureC = temp
		return r
	}
	// Status: RepeaterStats with a plausible battery voltage.
	if mv, stats, ok := sgParseStatus(body); ok {
		r := base(mv, "status")
		r.Stats = stats
		return r
	}
	return nil
}

// sgParseStatus reads a RepeaterStats struct; ok only when the battery voltage
// is in a plausible range, which a login reply's bytes are not.
func sgParseStatus(b []byte) (int, map[string]float64, bool) {
	stats := map[string]float64{}
	off := 0
	var batt int
	for _, f := range sgStatusFields {
		if off+f.Width > len(b) {
			break
		}
		var v float64
		if f.Width == 2 {
			u := binary.LittleEndian.Uint16(b[off:])
			if f.Signed {
				v = float64(int16(u))
			} else {
				v = float64(u)
			}
		} else {
			u := binary.LittleEndian.Uint32(b[off:])
			v = float64(u)
		}
		off += f.Width
		switch f.Key {
		case "battery_mv":
			batt = int(v)
		case "last_snr_x4":
			stats["last_snr"] = v / 4
		default:
			stats[f.Key] = v
		}
	}
	if batt < sgBattMinMv || batt > sgBattMaxMv {
		return 0, nil, false
	}
	if len(stats) == 0 {
		stats = nil
	}
	return batt, stats, true
}

// sgParseTelemetry reads the node's own voltage (and temperature) from a
// CayenneLPP reply. ok only when a channel-1 voltage is present.
func sgParseTelemetry(b []byte) (int, *float64, bool) {
	var mv int
	var temp *float64
	found := false
	i := 0
	for i+2 <= len(b) {
		ch, typ := b[i], b[i+1]
		size := sgLPPSize(typ)
		if ch == 0 || size == 0 || i+2+size > len(b) {
			break
		}
		val := b[i+2 : i+2+size]
		if ch == sgTelemChannel && typ == sgLPPVoltage {
			mv = int(binary.BigEndian.Uint16(val)) * 10
			found = true
		} else if ch == sgTelemChannel && typ == sgLPPTemperature {
			c := float64(int16(binary.BigEndian.Uint16(val))) / 10
			temp = &c
		}
		i += 2 + size
	}
	// Require a real voltage: 0 mV is almost always a non-telemetry reply that
	// happened to start with a channel-1/voltage-shaped byte pair.
	if !found || mv <= 0 || mv > sgBattMaxMv {
		return 0, nil, false
	}
	return mv, temp, true
}

func sgLPPSize(typ byte) int {
	switch typ {
	case 0, 1, 102, 120, 142:
		return 1
	case 2, 3, 101, 104, 115, sgLPPVoltage, 117, 121, 125, 128, 132, sgLPPTemperature:
		return 2
	case 100, 118, 130, 131, 133:
		return 4
	case 113, 134:
		return 6
	case 136:
		return 9
	case 135:
		return 3
	}
	return 0
}
