package main

import (
	"encoding/hex"
	"math/bits"
	"time"
)

// Damaged re-floods. A repeater cannot check the MAC of a packet it holds no
// key for, and it de-duplicates on a hash of the payload, so a copy that had a
// few bits changed in flight looks like a new packet and floods again. The
// copy can no longer be decrypted, so no app shows it; only the airtime is
// spent. (https://meshcore.co.uk/blog/damaged-packets.html)
//
// The test is the one MeshOS Repeater 1.6.2 uses, needing no keys: an
// encrypted payload of the same type and length as one heard in the last three
// minutes, within 24 bits of it. Two genuine payloads of the same length are
// AES output and differ in about half their bits. A copy one to eight bytes
// longer or shorter whose common part matches is a length change, the third
// kind in the post.

const (
	damageWindowSec   = 180
	damageMaxBits     = 24
	damageMinPayload  = 16
	damageMaxLenDelta = 8
	damageMaxLenBits  = 8
)

// damageTypes are the encrypted payload types: REQ, RESPONSE, TXT_MSG,
// GRP_TXT, GRP_DATA, ANON_REQ and PATH. Adverts, ACKs, traces and control
// packets are plain or short, so unrelated ones can sit within 24 bits.
var damageTypes = map[int]bool{
	PayloadREQ: true, PayloadRESPONSE: true, PayloadTXT_MSG: true,
	PayloadGRP_TXT: true, PayloadGRP_DATA: true, PayloadANON_REQ: true, PayloadPATH: true,
}

// Damage marks a transmission as a damaged copy of another.
type Damage struct {
	Of      string // hash of the packet it is a copy of
	Kind    string // "tail" (last two bytes only), "body", "longer" or "shorter"
	Bits    int    // bits that differ (over the common part for a length change)
	Offsets []int  // payload byte offsets that differ, same-length copies only
}

func (d *Damage) toMap() map[string]interface{} {
	if d == nil {
		return nil
	}
	return map[string]interface{}{"of": d.Of, "kind": d.Kind, "bits": d.Bits, "offsets": d.Offsets}
}

type damageKey struct {
	pt, n int
	min   int64
}

type damageEntry struct {
	tx      *StoreTx
	at      int64
	payload []byte
}

// damageIndex holds the encrypted payloads of every transmission in the
// store, bucketed by type, length and minute. Callers hold s.mu for writing.
type damageIndex struct {
	buckets map[damageKey][]*damageEntry
	byTx    map[*StoreTx]*damageEntry
}

func newDamageIndex() *damageIndex {
	return &damageIndex{buckets: map[damageKey][]*damageEntry{}, byTx: map[*StoreTx]*damageEntry{}}
}

// txPayload returns the payload bytes of a raw packet, after the header,
// transport codes and path.
func txPayload(rawHex string) []byte {
	b, err := hex.DecodeString(rawHex)
	if err != nil || len(b) < 2 {
		return nil
	}
	off := 1
	if isTransportRoute(int(b[0] & 0x03)) {
		off += 4
	}
	if off >= len(b) {
		return nil
	}
	pb := b[off]
	off++
	off += (int(pb>>6&0x3) + 1) * int(pb&0x3F)
	if off > len(b) {
		return nil
	}
	return b[off:]
}

func parseTxTime(s string) (int64, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, false
	}
	return t.Unix(), true
}

// compareDamage says whether b is a damaged copy of a, and how.
func compareDamage(a, b []byte) (*Damage, bool) {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	diff := 0
	var offs []int
	for i := 0; i < n; i++ {
		if x := a[i] ^ b[i]; x != 0 {
			diff += bits.OnesCount8(x)
			if len(a) == len(b) {
				offs = append(offs, i)
			}
		}
	}
	switch {
	case len(a) == len(b):
		if diff == 0 || diff > damageMaxBits {
			return nil, false
		}
		kind := "body"
		if offs[0] >= len(b)-2 {
			kind = "tail"
		}
		return &Damage{Kind: kind, Bits: diff, Offsets: offs}, true
	case diff <= damageMaxLenBits:
		kind := "longer"
		if len(b) < len(a) {
			kind = "shorter"
		}
		return &Damage{Kind: kind, Bits: diff}, true
	}
	return nil, false
}

// add indexes tx and marks whichever of it and its closest match came later
// as the damaged copy. A copy already marked damaged is never a parent.
func (d *damageIndex) add(tx *StoreTx) {
	if tx == nil || tx.PayloadType == nil || !damageTypes[*tx.PayloadType] || d.byTx[tx] != nil {
		return
	}
	at, ok := parseTxTime(tx.FirstSeen)
	if !ok {
		return
	}
	p := txPayload(tx.RawHex)
	if len(p) < damageMinPayload {
		return
	}
	e := &damageEntry{tx: tx, at: at, payload: append([]byte(nil), p...)}
	pt, min := *tx.PayloadType, at/60

	var best *damageEntry
	var bestDmg *Damage
	for m := min - damageWindowSec/60 - 1; m <= min+damageWindowSec/60+1; m++ {
		for n := len(p) - damageMaxLenDelta; n <= len(p)+damageMaxLenDelta; n++ {
			for _, c := range d.buckets[damageKey{pt, n, m}] {
				if c.at-at > damageWindowSec || at-c.at > damageWindowSec {
					continue
				}
				older, newer := c, e
				if e.at < c.at || (e.at == c.at && e.tx.ID < c.tx.ID) {
					older, newer = e, c
				}
				if older.tx.Damage != nil || newer.tx.Damage != nil {
					continue
				}
				dmg, ok := compareDamage(older.payload, newer.payload)
				if !ok || (bestDmg != nil && dmg.Bits >= bestDmg.Bits) {
					continue
				}
				best, bestDmg = c, dmg
			}
		}
	}
	if best != nil {
		older, newer := best, e
		if e.at < best.at || (e.at == best.at && e.tx.ID < best.tx.ID) {
			older, newer = e, best
		}
		bestDmg.Of = older.tx.Hash
		newer.tx.Damage = bestDmg
	}
	k := damageKey{pt, len(p), min}
	d.buckets[k] = append(d.buckets[k], e)
	d.byTx[tx] = e
}

// damageIdx returns the store's index, creating it for stores built
// without NewPacketStore. Caller holds s.mu for writing.
func (s *PacketStore) damageIdx() *damageIndex {
	if s.damage == nil {
		s.damage = newDamageIndex()
	}
	return s.damage
}

// remove drops an evicted transmission from the index.
func (d *damageIndex) remove(tx *StoreTx) {
	if d == nil {
		return
	}
	e := d.byTx[tx]
	if e == nil {
		return
	}
	delete(d.byTx, tx)
	k := damageKey{*tx.PayloadType, len(e.payload), e.at / 60}
	list := d.buckets[k]
	for i, c := range list {
		if c == e {
			list[i] = list[len(list)-1]
			list[len(list)-1] = nil
			list = list[:len(list)-1]
			break
		}
	}
	if len(list) == 0 {
		delete(d.buckets, k)
	} else {
		d.buckets[k] = list
	}
}
