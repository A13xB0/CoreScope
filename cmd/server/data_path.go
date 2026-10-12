package main

// Path watch: the Data tab's "watch" and "watchlist" panels follow the path
// between two repeaters. The packet store already indexes, for every
// repeater, the packets whose resolved path names it (byPathHop), so a watch
// is the packets that passed A, and among those that also name B, the ones
// whose path reaches B after A. Only those have their resolved paths read
// (batched, through the store's own loader); the rest is counted in memory.
// internal/pathwatch turns that into days, routes and route changes.
// Read-only; answers are cached for a few minutes per pair.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/meshcore-analyzer/pathwatch"
)

const (
	pathCacheFor  = 5 * time.Minute
	pathCacheMax  = 256
	pathMaxPairs  = 24
	pathBaseDays  = 14 // a share's usual comes from this many days before
	advertPayload = 4
)

var pubkeyRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// DataPathNode is a repeater by key and name.
type DataPathNode struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// DataPathDay is one 24-hour bucket, the last 24 hours last.
type DataPathDay struct {
	Day     string  `json:"day"` // its start, RFC3339
	Passed  int     `json:"passed"`
	Reached int     `json:"reached"`
	Share   float64 `json:"share"` // -1 when too few passed to say
}

// DataPathRoute is one way the packets went between the two.
type DataPathRoute struct {
	Via   []DataPathNode `json:"via"` // empty: straight from one to the other
	Share float64        `json:"share"`
	Usual float64        `json:"usual"`
}

// DataPathChange is a change of route, said in words.
type DataPathChange struct {
	Day      string         `json:"day"`
	Kind     string         `json:"kind"` // repeater-quiet, rerouted, new-repeater, sharing, route-changed
	Node     *DataPathNode  `json:"node,omitempty"`
	Was      float64        `json:"was"`
	Now      float64        `json:"now"`
	From     []DataPathNode `json:"from,omitempty"`
	To       []DataPathNode `json:"to,omitempty"`
	ShareWas float64        `json:"shareWas"`
	ShareNow float64        `json:"shareNow"`
	Text     string         `json:"text"`
}

// DataPathResponse is GET /api/data/path.
type DataPathResponse struct {
	From    DataPathNode     `json:"from"`
	To      DataPathNode     `json:"to"`
	Window  string           `json:"window"`
	Basis   string           `json:"basis"`
	Days    []DataPathDay    `json:"days"`
	Share   float64          `json:"share"`
	Usual   float64          `json:"usual"`
	Low     float64          `json:"low"`
	High    float64          `json:"high"`
	Verdict string           `json:"verdict"`
	Hops    pathwatch.Hops   `json:"hops"`
	Routes  []DataPathRoute  `json:"routes"`
	Changes []DataPathChange `json:"changes"`
	Flag    *DataPathChange  `json:"flag,omitempty"`
}

// DataPathPair is one pair in a batch.
type DataPathPair struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// DataPathsRequest is the body of POST /api/data/paths.
type DataPathsRequest struct {
	Window string         `json:"window"`
	Basis  string         `json:"basis"`
	Pairs  []DataPathPair `json:"pairs"`
}

// DataPathsResponse answers it, in the same order.
type DataPathsResponse struct {
	Paths []DataPathResponse `json:"paths"`
}

type pathCached struct {
	at   time.Time
	resp DataPathResponse
}

type pathCache struct {
	mu sync.Mutex
	m  map[string]pathCached
}

var pathCaches sync.Map // *PacketStore -> *pathCache

func (s *Server) pathCacheFor() *pathCache {
	c, _ := pathCaches.LoadOrStore(s.store, &pathCache{m: map[string]pathCached{}})
	return c.(*pathCache)
}

func (s *Server) pathOr404(w http.ResponseWriter) bool {
	if !s.cfg.DataEnabled() || s.store == nil {
		http.Error(w, "path watch is part of the Data tab (data.enabled in config.json)", http.StatusNotFound)
		return false
	}
	return true
}

func pathWindowDays(window string) (int, bool) {
	switch window {
	case "", "7d", "24h":
		return 7, true
	case "30d":
		return 30, true
	}
	return 0, false
}

func checkPair(from, to string) (string, string, error) {
	from, to = strings.ToLower(strings.TrimSpace(from)), strings.ToLower(strings.TrimSpace(to))
	if !pubkeyRe.MatchString(from) || !pubkeyRe.MatchString(to) {
		return "", "", fmt.Errorf("from and to must both be repeater public keys (64 hex characters)")
	}
	if from == to {
		return "", "", fmt.Errorf("from and to must be two different repeaters")
	}
	return from, to, nil
}

// handleDataPath is GET /api/data/path?from=&to=&window=&basis=.
func (s *Server) handleDataPath(w http.ResponseWriter, r *http.Request) {
	if !s.pathOr404(w) {
		return
	}
	q := r.URL.Query()
	from, to, err := checkPair(q.Get("from"), q.Get("to"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	days, ok := pathWindowDays(q.Get("window"))
	basis := q.Get("basis")
	if !ok || (basis != "" && basis != "all" && basis != "adverts") {
		http.Error(w, "window must be 24h, 7d or 30d and basis all or adverts", http.StatusBadRequest)
		return
	}
	writeJSON(w, s.pathWatch(from, to, q.Get("window"), days, basis == "adverts"))
}

// handleDataPaths is POST /api/data/paths: several pairs in one go, for a
// watch list.
func (s *Server) handleDataPaths(w http.ResponseWriter, r *http.Request) {
	if !s.pathOr404(w) {
		return
	}
	var req DataPathsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "the body must be {window, basis, pairs: [{from, to}]}", http.StatusBadRequest)
		return
	}
	days, ok := pathWindowDays(req.Window)
	if !ok || (req.Basis != "" && req.Basis != "all" && req.Basis != "adverts") || len(req.Pairs) == 0 || len(req.Pairs) > pathMaxPairs {
		http.Error(w, fmt.Sprintf("window must be 24h, 7d or 30d, basis all or adverts, and 1 to %d pairs", pathMaxPairs), http.StatusBadRequest)
		return
	}
	out := DataPathsResponse{Paths: []DataPathResponse{}}
	for _, p := range req.Pairs {
		from, to, err := checkPair(p.From, p.To)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out.Paths = append(out.Paths, s.pathWatch(from, to, req.Window, days, req.Basis == "adverts"))
	}
	writeJSON(w, out)
}

// pathWatch answers one pair, from the cache when it is fresh.
func (s *Server) pathWatch(from, to, window string, days int, adverts bool) DataPathResponse {
	key := fmt.Sprint(from, to, days, adverts)
	c := s.pathCacheFor()
	c.mu.Lock()
	if hit, ok := c.m[key]; ok && time.Since(hit.at) < pathCacheFor {
		c.mu.Unlock()
		return hit.resp
	}
	c.mu.Unlock()

	now := time.Now().UTC()
	since := now.Add(-time.Duration(days+pathBaseDays+1) * 24 * time.Hour)
	passed, reached := s.store.pathPackets(from, to, since, adverts)
	act := &storeActivity{s: s.store, db: s.db, relays: map[string][]time.Time{}}
	res := pathwatch.Analyse(pathwatch.Input{Now: now, Passed: passed, Reached: reached}, act, pathwatch.Options{WindowDays: days, BaseDays: pathBaseDays})
	resp := s.pathResponse(from, to, window, adverts, res)

	c.mu.Lock()
	if len(c.m) >= pathCacheMax {
		var oldest string
		for k, v := range c.m {
			if oldest == "" || v.at.Before(c.m[oldest].at) {
				oldest = k
			}
		}
		delete(c.m, oldest)
	}
	c.m[key] = pathCached{at: time.Now(), resp: resp}
	c.mu.Unlock()
	return resp
}

func parseTxTime(v string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// pathPackets returns when each packet that passed from (or was an advert
// from it) since since was first heard, and the ones whose resolved path then
// reached to, with the repeaters between the two.
func (s *PacketStore) pathPackets(from, to string, since time.Time, advertsOnly bool) ([]time.Time, []pathwatch.Crossing) {
	var passed []time.Time
	snaps := map[int][]rpObs{}
	at := map[int]time.Time{}
	s.mu.RLock()
	inTo := make(map[int]bool, len(s.byPathHop[to]))
	for _, tx := range s.byPathHop[to] {
		inTo[tx.ID] = true
	}
	seen := map[int]bool{}
	consider := func(tx *StoreTx) {
		if seen[tx.ID] {
			return
		}
		seen[tx.ID] = true
		if advertsOnly && (tx.PayloadType == nil || *tx.PayloadType != advertPayload) {
			return
		}
		t, ok := parseTxTime(tx.FirstSeen)
		if !ok || t.Before(since) {
			return
		}
		passed = append(passed, t)
		if inTo[tx.ID] {
			snaps[tx.ID] = snapshotRPObs(tx)
			at[tx.ID] = t
		}
	}
	for _, tx := range s.byPathHop[from] {
		consider(tx)
	}
	for _, tx := range s.byNode[from] { // its own adverts
		if tx.PayloadType != nil && *tx.PayloadType == advertPayload && strings.Contains(strings.ToLower(tx.DecodedJSON), from) {
			consider(tx)
		}
	}
	s.mu.RUnlock()

	paths := s.loadCanonicalResolvedPaths(snaps)
	var reached []pathwatch.Crossing
	for id, rp := range paths {
		path := make([]string, len(rp))
		for i, p := range rp {
			if p != nil {
				path[i] = strings.ToLower(*p)
			}
		}
		ia := -1 // an advert from `from` starts before its path
		for i, pk := range path {
			if pk == from {
				ia = i
				break
			}
		}
		for ib := ia + 1; ib < len(path); ib++ {
			if path[ib] != to {
				continue
			}
			route := []string{}
			for _, pk := range path[ia+1 : ib] {
				if pk != "" {
					route = append(route, pk)
				}
			}
			reached = append(reached, pathwatch.Crossing{At: at[id], Route: route})
			break
		}
	}
	return passed, reached
}

// storeActivity tells pathwatch what a repeater does beyond the path.
type storeActivity struct {
	s      *PacketStore
	db     *DB
	relays map[string][]time.Time // sorted, per repeater, read once per request
}

func (a *storeActivity) Relays(pk string, from, to time.Time) int {
	ts, ok := a.relays[pk]
	if !ok {
		a.s.mu.RLock()
		for _, tx := range a.s.byPathHop[pk] {
			if t, ok := parseTxTime(tx.FirstSeen); ok {
				ts = append(ts, t)
			}
		}
		a.s.mu.RUnlock()
		sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
		a.relays[pk] = ts
	}
	lo := sort.Search(len(ts), func(i int) bool { return !ts[i].Before(from) })
	hi := sort.Search(len(ts), func(i int) bool { return !ts[i].Before(to) })
	return hi - lo
}

func (a *storeActivity) FirstHeard(pk string) (time.Time, bool) {
	if a.db == nil || a.db.conn == nil {
		return time.Time{}, false
	}
	var v string
	if a.db.conn.QueryRow(`SELECT COALESCE(first_seen,'') FROM nodes WHERE LOWER(public_key) = ?`, pk).Scan(&v) != nil {
		return time.Time{}, false
	}
	return parseTxTime(v)
}

// nodeNames names repeaters by key; unknown ones are shown by their key's start.
func (s *Server) nodeNames(keys []string) map[string]string {
	out := map[string]string{}
	if len(keys) == 0 || s.db == nil || s.db.conn == nil {
		return out
	}
	args := make([]interface{}, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	rows, err := s.db.conn.Query(`SELECT LOWER(public_key), COALESCE(name,'') FROM nodes WHERE LOWER(public_key) IN (?`+strings.Repeat(",?", len(keys)-1)+`)`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k, n string
		if rows.Scan(&k, &n) == nil && n != "" {
			out[k] = n
		}
	}
	return out
}

func (s *Server) pathResponse(from, to, window string, adverts bool, res pathwatch.Result) DataPathResponse {
	keys := map[string]bool{from: true, to: true}
	for _, r := range res.Routes {
		for _, pk := range r.Via {
			keys[pk] = true
		}
	}
	for _, c := range res.Changes {
		keys[c.Node] = true
		for _, pk := range append(append([]string{}, c.From...), c.To...) {
			keys[pk] = true
		}
	}
	list := make([]string, 0, len(keys))
	for k := range keys {
		if k != "" {
			list = append(list, k)
		}
	}
	names := s.nodeNames(list)
	node := func(pk string) DataPathNode {
		n := names[pk]
		if n == "" {
			n = pk[:min(8, len(pk))]
		}
		return DataPathNode{Key: pk, Name: n}
	}
	nodes := func(pks []string) []DataPathNode {
		out := make([]DataPathNode, len(pks))
		for i, pk := range pks {
			out[i] = node(pk)
		}
		return out
	}
	basis := "all"
	if adverts {
		basis = "adverts"
	}
	resp := DataPathResponse{From: node(from), To: node(to), Window: window, Basis: basis, Days: []DataPathDay{}, Share: res.Share, Usual: res.Usual,
		Low: res.Low, High: res.High, Verdict: res.Verdict, Hops: res.Hops, Routes: []DataPathRoute{}, Changes: []DataPathChange{}}
	for _, d := range res.Days {
		resp.Days = append(resp.Days, DataPathDay{Day: d.Day.Format(time.RFC3339), Passed: d.Passed, Reached: d.Reached, Share: d.Share})
	}
	for _, r := range res.Routes {
		resp.Routes = append(resp.Routes, DataPathRoute{Via: nodes(r.Via), Share: r.Share, Usual: r.Usual})
	}
	change := func(c pathwatch.Change) DataPathChange {
		out := DataPathChange{Day: c.Day.Format(time.RFC3339), Kind: c.Kind, Was: c.Was, Now: c.Now, From: nodes(c.From), To: nodes(c.To), ShareWas: c.ShareWas, ShareNow: c.ShareNow}
		if c.Node != "" {
			n := node(c.Node)
			out.Node = &n
		}
		out.Text = changeText(out)
		return out
	}
	for _, c := range res.Changes {
		resp.Changes = append(resp.Changes, change(c))
	}
	if res.Flag != nil {
		f := change(*res.Flag)
		resp.Flag = &f
	}
	return resp
}

func pct(v float64) string { return fmt.Sprintf("%.0f%%", v*100) }

func viaText(ns []DataPathNode) string {
	if len(ns) == 0 {
		return "straight across"
	}
	names := make([]string, len(ns))
	for i, n := range ns {
		names[i] = n.Name
	}
	return "via " + strings.Join(names, ", ")
}

// changeText says a route change in a sentence.
func changeText(c DataPathChange) string {
	through := ""
	if d := c.ShareNow - c.ShareWas; d <= -0.1 || d >= 0.1 {
		through = fmt.Sprintf(" The share getting through went from %s to %s.", pct(c.ShareWas), pct(c.ShareNow))
	}
	name := ""
	if c.Node != nil {
		name = c.Node.Name
	}
	switch c.Kind {
	case pathwatch.KindRepeaterQuiet:
		return fmt.Sprintf("%s dropped out of this path (it carried %s) and has gone quiet everywhere.%s", name, pct(c.Was), through)
	case pathwatch.KindRerouted:
		return fmt.Sprintf("%s dropped out of this path (it carried %s) but is still relaying elsewhere, so the traffic was rerouted.%s", name, pct(c.Was), through)
	case pathwatch.KindNewRepeater:
		return fmt.Sprintf("%s now carries %s, up from %s. It was first heard recently: a new repeater sharing the load.%s", name, pct(c.Now), pct(c.Was), through)
	case pathwatch.KindSharing:
		return fmt.Sprintf("%s now carries %s, up from %s, sharing the load.%s", name, pct(c.Now), pct(c.Was), through)
	case pathwatch.KindRouteChanged:
		return fmt.Sprintf("The main route moved from %s to %s.%s", viaText(c.From), viaText(c.To), through)
	}
	return c.Kind
}
