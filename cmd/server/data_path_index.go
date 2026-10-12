package main

// The path watch's own history: for every packet of the last data.pathDays
// days, when it was first heard and each distinct resolved path its
// observers saw, with repeaters as small numbers. It is independent of the
// packet store, whose window is often shorter (a 14-day flag needs three
// weeks: the fortnight and the week before each change), and it keeps every
// observation's path, not only the longest, so a packet one observer saw
// reach the second repeater counts even when another heard a longer path.
//
// Filled from SQLite after the Data tab's own index (which waits for the
// packet store), topped up every two minutes from new observations, and
// rebuilt once a day so old packets fall away. Read-only.

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	pathIndexBatch   = 5000
	pathIndexRefresh = 2 * time.Minute
	pathIndexRebuild = 24 * time.Hour
)

// pathTx is one packet. Its paths are stored flat, each ended by pathEnd,
// keeping only the longest: a path that is an unbroken stretch of another
// the packet already has says nothing new about where it went.
type pathTx struct {
	at     uint32 // first heard, unix seconds
	from   nodeID // the repeater an advert came from; noNode for none
	advert bool   // payload type 4
	hops   []nodeID
}

// nodeID numbers repeaters in two bytes: a mesh has thousands, not tens of
// thousands. Past maxNodes new repeaters are not named (they count as an
// unresolved hop) rather than wrapping.
type nodeID uint16

const (
	noNode   nodeID = 0xFFFF // an unresolved hop
	pathEnd  nodeID = 0xFFFE // ends each path in pathTx.hops
	maxNodes        = 0xFFFD
)

type pathIndex struct {
	db      *DB
	days    int
	waitFor []<-chan struct{}
	now     func() time.Time

	mu      sync.RWMutex
	keys    []string          // id -> lower-case public key
	ids     map[string]nodeID // public key -> id
	txs     []pathTx
	recent  map[int64]int32     // transmission id -> index into txs, for the last two days (late observations)
	byNode  map[nodeID][]uint32 // repeater -> packets (indexes into txs) with it on a path, or adverts from it
	lastObs int64
	built   time.Time
	ready   bool
}

func newPathIndex(db *DB, days int) *pathIndex {
	return &pathIndex{db: db, days: days, now: time.Now, ids: map[string]nodeID{}, recent: map[int64]int32{}, byNode: map[nodeID][]uint32{}}
}

func (p *pathIndex) isReady() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.ready
}

func (p *pathIndex) run(ctx context.Context) {
	for _, ch := range p.waitFor {
		select {
		case <-ch:
		case <-ctx.Done():
			return
		}
	}
	start := time.Now()
	if err := p.fill(ctx); err != nil {
		log.Printf("[path] fill failed: %v", err)
	} else {
		p.mu.RLock()
		n := len(p.txs)
		p.mu.RUnlock()
		log.Printf("[path] %d packets over %d days in %s", n, p.days, time.Since(start).Round(time.Millisecond))
	}
	tick := time.NewTicker(pathIndexRefresh)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		var err error
		if time.Since(p.built) >= pathIndexRebuild {
			err = p.fill(ctx)
		} else {
			err = p.read(ctx, p, true)
		}
		if err != nil {
			log.Printf("[path] refresh failed: %v", err)
		}
	}
}

// fill builds a fresh index of the window and swaps it in.
func (p *pathIndex) fill(ctx context.Context) error {
	fresh := newPathIndex(p.db, p.days)
	fresh.now = p.now
	if err := p.read(ctx, fresh, false); err != nil {
		return err
	}
	fresh.trim()
	p.mu.Lock()
	p.keys, p.ids, p.txs, p.recent, p.byNode, p.lastObs = fresh.keys, fresh.ids, fresh.txs, fresh.recent, fresh.byNode, fresh.lastObs
	p.built, p.ready = time.Now(), true
	p.mu.Unlock()
	return nil
}

// read adds the observations into dst: the whole window when it is fresh,
// else those newer than the last read (locked, as dst is then live).
func (p *pathIndex) read(ctx context.Context, dst *pathIndex, live bool) error {
	cutoff := p.now().Add(-time.Duration(p.days) * 24 * time.Hour).Unix()
	last := dst.lastObs
	if live {
		dst.mu.RLock()
		last = dst.lastObs
		dst.mu.RUnlock()
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows, err := p.db.conn.QueryContext(ctx, `SELECT o.id, o.transmission_id, o.timestamp, COALESCE(o.resolved_path,''), t.first_seen,
			COALESCE(t.payload_type,-1), COALESCE(t.from_pubkey,'')
			FROM observations o JOIN transmissions t ON t.id = o.transmission_id
			WHERE o.id > ? AND o.timestamp >= ? ORDER BY o.id LIMIT ?`, last, cutoff, pathIndexBatch)
		if err != nil {
			return err
		}
		type row struct {
			tx, ts    int64
			rp, first string
			typ       int
			from      string
		}
		var batch []row
		for rows.Next() {
			var id int64
			var r row
			var rp sql.NullString
			if err := rows.Scan(&id, &r.tx, &r.ts, &rp, &r.first, &r.typ, &r.from); err != nil {
				rows.Close()
				return err
			}
			r.rp = rp.String
			last = id
			batch = append(batch, r)
		}
		rows.Close()
		if live {
			dst.mu.Lock()
		}
		for _, r := range batch {
			dst.add(r.tx, r.ts, r.first, r.typ, r.from, r.rp)
		}
		dst.lastObs = last
		done := len(batch) < pathIndexBatch
		if done {
			dst.forgetOld()
		}
		if live {
			dst.mu.Unlock()
		}
		if done {
			return nil
		}
		time.Sleep(5 * time.Millisecond) // leave room for requests while filling
	}
}

func (p *pathIndex) id(pk string) nodeID {
	if id, ok := p.ids[pk]; ok {
		return id
	}
	if len(p.keys) >= maxNodes {
		return noNode
	}
	id := nodeID(len(p.keys))
	p.keys = append(p.keys, pk)
	p.ids[pk] = id
	return id
}

// trim gives back the spare capacity slices grew while filling.
func (p *pathIndex) trim() {
	p.txs = append([]pathTx(nil), p.txs...)
	for n, l := range p.byNode {
		p.byNode[n] = append([]uint32(nil), l...)
	}
}

// forgetOld drops packets more than two days old from the recent map:
// observations of them no longer arrive.
func (p *pathIndex) forgetOld() {
	cut := uint32(p.now().Add(-48 * time.Hour).Unix())
	for id, i := range p.recent {
		if p.txs[i].at < cut {
			delete(p.recent, id)
		}
	}
}

// add records one observation. Caller holds the lock on a live index.
func (p *pathIndex) add(txID, ts int64, firstSeen string, typ int, from, rp string) {
	i, ok := p.recent[txID]
	if !ok {
		at := ts
		if t, ok := parseTxTime(firstSeen); ok {
			at = t.Unix()
		}
		p.txs = append(p.txs, pathTx{at: uint32(at), from: noNode, advert: typ == advertPayload})
		i = int32(len(p.txs) - 1)
		p.recent[txID] = i // trimmed to the last two days once a fill or refresh is read
		if p.txs[i].advert && from != "" {
			if f := p.id(strings.ToLower(from)); f != noNode {
				p.txs[i].from = f
				p.byNode[f] = append(p.byNode[f], uint32(i))
			}
		}
	}
	if rp == "" {
		return
	}
	var raw []*string
	if json.Unmarshal([]byte(rp), &raw) != nil || len(raw) == 0 {
		return
	}
	path := make([]nodeID, len(raw))
	for k, s := range raw {
		path[k] = noNode
		if s != nil && *s != "" {
			path[k] = p.id(strings.ToLower(*s))
		}
	}
	tx := &p.txs[i]
	on := map[nodeID]bool{}
	if tx.from != noNode {
		on[tx.from] = true
	}
	var keep [][]nodeID
	covered := false
	eachPath(tx.hops, func(q []nodeID) {
		for _, n := range q {
			on[n] = true
		}
		switch {
		case covered:
			keep = append(keep, q)
		case stretchOf(path, q):
			covered = true // already have a path that holds this one
			keep = append(keep, q)
		case stretchOf(q, path):
			// the new path holds this one: drop it
		default:
			keep = append(keep, q)
		}
	})
	if covered {
		return
	}
	keep = append(keep, path)
	flat := make([]nodeID, 0, len(tx.hops)+len(path)+1)
	for _, q := range keep {
		flat = append(append(flat, q...), pathEnd)
	}
	tx.hops = flat
	for _, n := range path {
		if n != noNode && !on[n] {
			on[n] = true
			p.byNode[n] = append(p.byNode[n], uint32(i))
		}
	}
}

// eachPath calls fn with each path stored flat in hops.
func eachPath(hops []nodeID, fn func([]nodeID)) {
	start := 0
	for k, n := range hops {
		if n == pathEnd {
			fn(hops[start:k])
			start = k + 1
		}
	}
}

// stretchOf says whether a is an unbroken stretch of b.
func stretchOf(a, b []nodeID) bool {
	if len(a) > len(b) {
		return false
	}
	for off := 0; off+len(a) <= len(b); off++ {
		if equalPath(a, b[off:off+len(a)]) {
			return true
		}
	}
	return false
}

func equalPath(a, b []nodeID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// packets returns when each packet that passed from (or was an advert from
// it) since since was first heard, and the ones some observer saw reach to
// after from, with the repeaters between (from the longest such path).
func (p *pathIndex) packets(from, to string, since time.Time, advertsOnly bool) ([]time.Time, []pathCrossing) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	a, okA := p.ids[from]
	b, okB := p.ids[to]
	if !okA {
		return nil, nil
	}
	var passed []time.Time
	var reached []pathCrossing
	lo := uint32(since.Unix())
	for _, ti := range p.byNode[a] {
		tx := &p.txs[ti]
		if tx.at < lo || (advertsOnly && !tx.advert) {
			continue
		}
		at := time.Unix(int64(tx.at), 0).UTC()
		passed = append(passed, at)
		if !okB {
			continue
		}
		var best []nodeID
		found := false
		eachPath(tx.hops, func(path []nodeID) {
			ia := -1
			if tx.from != a {
				ia = indexOf(path, a, 0)
				if ia < 0 {
					return
				}
			}
			if ib := indexOf(path, b, ia+1); ib >= 0 {
				if seg := path[ia+1 : ib]; !found || len(seg) > len(best) {
					best, found = seg, true
				}
			}
		})
		if found {
			route := make([]string, 0, len(best))
			for _, n := range best {
				if n != noNode {
					route = append(route, p.keys[n])
				}
			}
			reached = append(reached, pathCrossing{at: at, route: route})
		}
	}
	return passed, reached
}

func indexOf(path []nodeID, n nodeID, from int) int {
	for i := from; i < len(path); i++ {
		if path[i] == n {
			return i
		}
	}
	return -1
}

// relays is when each packet with pk on a path was first heard, sorted.
func (p *pathIndex) relays(pk string) []time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()
	id, ok := p.ids[pk]
	if !ok {
		return nil
	}
	out := make([]time.Time, 0, len(p.byNode[id]))
	for _, ti := range p.byNode[id] {
		out = append(out, time.Unix(int64(p.txs[ti].at), 0))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

type pathCrossing struct {
	at    time.Time
	route []string
}
