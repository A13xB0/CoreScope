package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/origin"
)

// readPacketsReference is readPackets as it was before reading the JSON
// columns as raw bytes: the equivalence test compares the two.
func (d *dataService) readPacketsReference(ctx context.Context, where string, args ...any) ([]origin.Packet, error) {
	scope := "NULL"
	if d.db.hasScopeName {
		scope = "t.scope_name"
	}
	rows, err := d.db.conn.QueryContext(ctx, `SELECT t.id, t.hash, t.first_seen, COALESCE(t.payload_type, -1), COALESCE(t.route_type, -1), `+scope+`, COALESCE(t.decoded_json, '')
		FROM transmissions t WHERE `+where+` ORDER BY t.id`, args...)
	if err != nil {
		return nil, err
	}
	byID := map[int64]int{}
	var out []origin.Packet
	var lo, hi int64 = math.MaxInt64, 0
	for rows.Next() {
		var id int64
		var p origin.Packet
		var first, decoded string
		var sc sql.NullString
		if err := rows.Scan(&id, &p.Hash, &first, &p.Type, &p.Route, &sc, &decoded); err != nil {
			rows.Close()
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339, first); err == nil {
			p.At = t.Unix()
		}
		p.Scope, p.Scoped = sc.String, sc.Valid
		if decoded != "" && (p.Type == 4 || p.Type == 5 || p.Type == 6) {
			var a dataAdvert
			if json.Unmarshal([]byte(decoded), &a) == nil {
				if p.Type == 4 && a.PubKey != "" {
					p.Advert = &origin.Advert{PublicKey: a.PubKey}
					if a.Flags.HasLocation {
						p.Advert.Lat, p.Advert.Lon = a.Lat, a.Lon
					}
				}
				if p.Type != 4 {
					switch {
					case a.Channel != "":
						p.Channel = a.Channel
					case a.Hash != "":
						p.Channel = "hash:" + strings.ToUpper(a.Hash)
					}
				}
			}
		}
		byID[id] = len(out)
		out = append(out, p)
		if id < lo {
			lo = id
		}
		if id > hi {
			hi = id
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}
	observer, join := "o.observer_id", ""
	if d.db.isV3 {
		observer, join = "obs.id", " JOIN observers obs ON obs.rowid = o.observer_idx"
	}
	resolved := "''"
	if d.db.hasResolvedPath {
		resolved = "COALESCE(o.resolved_path, '')"
	}
	orows, err := d.db.conn.QueryContext(ctx, `SELECT o.transmission_id, `+observer+`, COALESCE(o.direction, ''), COALESCE(o.path_json, ''), `+resolved+`
		FROM observations o`+join+` WHERE o.transmission_id BETWEEN ? AND ?`, lo, hi)
	if err != nil {
		return nil, err
	}
	defer orows.Close()
	for orows.Next() {
		var tid int64
		var o origin.Observation
		var dir, path, res string
		if err := orows.Scan(&tid, &o.ObserverID, &dir, &path, &res); err != nil {
			return nil, err
		}
		i, ok := byID[tid]
		if !ok {
			continue
		}
		o.Relayed = strings.EqualFold(dir, "tx")
		if path != "" && path != "[]" {
			_ = json.Unmarshal([]byte(path), &o.Path)
		}
		if res != "" {
			var rp []*string
			if json.Unmarshal([]byte(res), &rp) == nil {
				o.Resolved = make([]string, len(rp))
				for k, v := range rp {
					if v != nil {
						o.Resolved[k] = *v
					}
				}
			}
		}
		out[i].Observations = append(out[i].Observations, o)
	}
	return out, orows.Err()
}

func addReadPacketsEdgeRows(t *testing.T, f *dataFixture) {
	t.Helper()
	at := f.now.Add(-30 * time.Minute)
	rows := []struct{ path, resolved string }{
		{`[]`, `[null]`},
		{`["AA11","CC33"]`, `[null,"CC33330000000000000000000000000000000000000000000000000000000000"]`},
		{`["AA11"]`, `not json`},
		{`not json`, `["AA11110000000000000000000000000000000000000000000000000000000000",1]`},
		{``, `[]`},
		{`["a\"b"]`, `["x\u00e9"]`},
	}
	for i, r := range rows {
		id := 100 + i
		if _, err := f.db.conn.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, decoded_json) VALUES (?, '00', ?, ?, 1, 4, ?)`,
			id, fmt.Sprintf("edge%012d", i), dataTS(at), `{"pubKey":"AB","lat":1.5,"lon":2.5,"flags":{"hasLocation":true}}`); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, direction, path_json, resolved_path, timestamp) VALUES (?, 1, 'TX', ?, ?, ?)`,
			id, r.path, r.resolved, at.Unix()); err != nil {
			t.Fatal(err)
		}
	}
}

// readPackets gives exactly what it gave before it read the JSON columns as
// raw bytes and decoded resolved paths straight into strings.
func TestDataReadPacketsMatchesReference(t *testing.T) {
	f := newDataFixture(t, true)
	addReadPacketsEdgeRows(t, f)
	since := f.now.Add(-30 * 24 * time.Hour).UTC().Format(dataSQLTimeFormat)
	got, err := f.svc.readPackets(context.Background(), "t.first_seen >= ?", since)
	if err != nil {
		t.Fatal(err)
	}
	want, err := f.svc.readPacketsReference(context.Background(), "t.first_seen >= ?", since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 9 || !reflect.DeepEqual(got, want) {
		gj, _ := json.Marshal(got)
		wj, _ := json.Marshal(want)
		t.Fatalf("readPackets differs from the reference:\n got %s\nwant %s", gj, wj)
	}
}

// The fill reads every packet of the window with every observation. On a
// busy week it allocated about 720MB per fill: each observation's ~420-char
// resolved_path was scanned into a string, copied into a []byte for the
// decoder, and decoded into a pointer per hop before being copied again.
func TestDataReadPacketsAllocationsPerObservation(t *testing.T) {
	f := newDataFixture(t, true)
	at := f.now.Add(-20 * time.Minute)
	pk := func(i int) string { return fmt.Sprintf("%064X", i) }
	const n = 400
	for i := 0; i < n; i++ {
		id := 1000 + i
		res := `["` + pk(i%7) + `",null,"` + pk(i%5+10) + `",null,"` + pk(i%3+20) + `",null]`
		if _, err := f.db.conn.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, decoded_json) VALUES (?, '00', ?, ?, 1, 5, '{"channel":"Public"}')`,
			id, fmt.Sprintf("bulk%012d", i), dataTS(at)); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, direction, path_json, resolved_path, timestamp) VALUES (?, 1, 'rx', '["AA","BB","CC","DD","EE","FF"]', ?, ?)`,
			id, res, at.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	since := at.Add(-time.Second).UTC().Format(dataSQLTimeFormat)
	var a, b runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&a)
	pkts, err := f.svc.readPackets(context.Background(), "t.first_seen >= ?", since)
	runtime.ReadMemStats(&b)
	if err != nil || len(pkts) != n {
		t.Fatalf("read %d packets (%v), want %d", len(pkts), err, n)
	}
	per := float64(b.TotalAlloc-a.TotalAlloc) / n
	t.Logf("readPackets allocates %.0f bytes per packet with one observation", per)
	const limit = 2000 // 2390 before, ~1735 after
	if per > limit {
		t.Fatalf("readPackets allocates %.0f bytes per packet, limit %d", per, limit)
	}
}
