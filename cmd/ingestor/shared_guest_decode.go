package main

import (
	"encoding/binary"
	"encoding/hex"
	"log"
	"strings"
	"sync"
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
	Key   string
	Width int // 2 or 4
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

// sgClaim records that an observer polls a node under the shared key, so a
// decoded reply is credited to that poller ("requested by …") and not to the
// incidental observer that overheard it. It lapses after sgClaimTTL.
type sgClaim struct {
	observer string
	at       time.Time
}

const sgClaimTTL = 3 * time.Hour

type sharedGuestDecoder struct {
	store     *Store
	seed      []byte
	hash      byte   // first byte of the shared public key
	sharedObs string // the shared key's pubkey, the fallback observer id when no one claims the node
	mu        sync.Mutex
	cand      map[byte][][]byte // repeater pubkeys by first byte
	candAt    time.Time
	claims    map[string]sgClaim // node -> who polls it under the shared key
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
	return &sharedGuestDecoder{store: store, seed: seed, hash: pub[0],
		sharedObs: strings.ToLower(sharedGuestKeyPublic), claims: map[string]sgClaim{}}
}

// HandleClaim records a poller's claim over the nodes it polls under the shared
// key: meshcore/<region>/<observer>/sharedclaim with {"nodes":[<64-hex>,…]}.
func (d *sharedGuestDecoder) HandleClaim(observerID string, msg map[string]interface{}, now time.Time) {
	if d == nil {
		return
	}
	observerID = strings.ToLower(strings.TrimSpace(observerID))
	if !targetPubkeyRe.MatchString(observerID) {
		return
	}
	nodes, _ := msg["nodes"].([]interface{})
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, n := range nodes {
		node, _ := n.(string)
		node = strings.ToLower(strings.TrimSpace(node))
		if targetPubkeyRe.MatchString(node) {
			d.claims[node] = sgClaim{observer: observerID, at: now}
		}
	}
}

// claimantOf is the observer that polls a node under the shared key, or "" when
// no fresh claim exists.
func (d *sharedGuestDecoder) claimantOf(node string, now time.Time) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.claims[node]; ok && now.Sub(c.at) < sgClaimTTL {
		return c.observer
	}
	return ""
}

// candidates returns the known repeater/room pubkeys whose first byte is b,
// refreshed from the nodes table every few minutes.
func (d *sharedGuestDecoder) candidates(b byte, now time.Time) [][]byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cand == nil || now.Sub(d.candAt) > sgCandRefresh {
		d.cand = d.loadCandidates()
		d.candAt = now
	}
	return d.cand[b]
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

// Handle decodes a decoded packet if it is a shared-key reply and stores the
// reading credited to the observer that uploaded it. Cheap and silent for
// everything else.
func (d *sharedGuestDecoder) Handle(observerID string, p *DecodedPacket, now time.Time) {
	if d == nil || p == nil || p.Header.PayloadType != PayloadRESPONSE {
		return
	}
	dest, err1 := hex.DecodeString(p.Payload.DestHash)
	src, err2 := hex.DecodeString(p.Payload.SrcHash)
	if err1 != nil || err2 != nil || len(dest) != 1 || len(src) != 1 || dest[0] != d.hash {
		return // not addressed to the shared key
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
	for _, pub := range d.candidates(src[0], now) {
		plain, ok := sharedGuestDecrypt(sharedGuestSecret(d.seed, pub), enc)
		if !ok || len(plain) < 4 {
			continue
		}
		// The tag is the request timestamp the repeater reflects; it is the same
		// in every observer's copy of this one reply, so using it as the
		// reading's time (with a fixed "shared key" observer) collapses all those
		// copies to a single row — the reading is the repeater's own, not each
		// observer's.
		tag := binary.LittleEndian.Uint32(plain[:4])
		nodeHex := hex.EncodeToString(pub)
		// Credit the poller that claimed this node ("requested by …"); fall
		// back to the shared key itself when no one has claimed it.
		requester := d.claimantOf(nodeHex, now)
		if requester == "" {
			requester = d.sharedObs
		}
		r := d.reading(nodeHex, requester, tag, plain[4:])
		if r == nil {
			return // authentic reply, but not a battery/telemetry answer (e.g. a login) — leave it
		}
		stored, err := d.store.InsertNeighborBattery(r, 0)
		if err != nil {
			log.Printf("[shared-guest] insert for %.8s: %v", r.NodeID, err)
		} else if stored {
			log.Printf("[shared-guest] decoded %s for %.8s requested by %.8s (%d mV, heard by %.8s)",
				r.Method, r.NodeID, requester, r.BatteryMv, observerID)
		}
		return
	}
}

// reading builds a NeighborBatteryReading from a decrypted reply body, or nil
// when the body is not a status or telemetry answer. The reading is credited to
// the shared key (not the observer that happened to hear it) and timed by the
// reply's tag, so every observer's copy of one reply is the same row.
func (d *sharedGuestDecoder) reading(nodeHex, requester string, tag uint32, body []byte) *NeighborBatteryReading {
	// Guard a nonsense tag (a misparse would date the row to 1970); authentic
	// replies carry the request's epoch-second timestamp.
	if tag < 1_600_000_000 {
		return nil
	}
	at := time.Unix(int64(tag), 0).UTC().Format(batteryTimeLayout)
	base := func(mv int, method string) *NeighborBatteryReading {
		return &NeighborBatteryReading{
			NodeID: nodeHex, ObserverID: requester, BatteryMv: mv, Method: method,
			Source: "poll", KeyMode: "shared", ReplyHops: 0,
			SampledAt: at, ReceivedAt: at,
		}
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
	if !found || mv < 0 || mv > sgBattMaxMv {
		return 0, nil, false
	}
	return mv, temp, true
}

func sgLPPSize(typ byte) int {
	switch typ {
	case 0, 102, 120, 142:
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
