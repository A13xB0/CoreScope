package dataindex

import (
	"fmt"
	"reflect"
	"testing"
	"time"
	"unsafe"

	"github.com/meshcore-analyzer/origin"
)

const hour = int64(3600)

var now = time.Unix(1_800_000_000, 0).UTC()

func at(ago time.Duration) int64 { return now.Add(-ago).Unix() }

// rec builds a record heard in the given areas. Each heard entry is
// "AREA:how" with how r (relayed), a (across) or u (unclear).
func rec(hash string, t int64, typ, route int, scope string, scoped bool, originArea string, via []string, heard ...string) origin.Record {
	r := origin.Record{Hash: hash, At: t, Type: typ, Route: route, Scope: scope, Scoped: scoped, Origin: originArea, Basis: origin.BasisRelay, Via: via}
	for _, h := range heard {
		var e origin.Heard
		e.Observers = 1
		fmt.Sscanf(h, "%3s", &e.Area)
		switch h[len(h)-1] {
		case 'r':
			e.Relayed = true
		case 'a':
			e.Across = true
		}
		if len(h) > 2 && h[:2] == "--" {
			e.Area = origin.Unknown
		}
		r.Heard = append(r.Heard, e)
	}
	return r
}

func testIndex() *Index {
	ix := New(Options{Areas: []string{"SCO", "IOI", "NEE"}, Home: "SCO"})
	ix.Replace(0, []origin.Record{
		// 3 days ago: a Scottish advert heard at home and in Ireland.
		rec("00000000000000a1", at(72*time.Hour), 4, 1, "", false, "SCO", []string{"SCO"}, "SCO:r", "IOI:a"),
		// 2 hours ago: an Irish channel message on #ioi, carried through
		// Scotland into the North East.
		withChannel(rec("00000000000000a2", at(2*time.Hour), 5, 0, "ioi", true, "IOI", []string{"IOI", "SCO"}, "SCO:r", "NEE:u"), "Public"),
		// 1 hour ago: a direct message with no known start.
		rec("00000000000000a3", at(time.Hour), 2, 2, "", false, origin.Unknown, nil, "SCO:u"),
		// 10 minutes ago: a directly routed advert heard only by an observer
		// nobody can place.
		rec("not-a-hex-hash", at(10*time.Minute), 4, 2, "", false, "NEE", nil, "--:u"),
	})
	return ix
}

func withChannel(r origin.Record, ch string) origin.Record { r.Channel = ch; return r }

func cubeTotal(c Cube) int {
	n := 0
	for _, r := range c.Rows {
		n += r[9]
	}
	return n
}

func TestRowsArePointerFreeAndSmall(t *testing.T) {
	var r row
	if s := unsafe.Sizeof(r); s > 56 {
		t.Errorf("row is %d bytes, want at most 56", s)
	}
	typ := reflect.TypeOf(r)
	for i := 0; i < typ.NumField(); i++ {
		switch typ.Field(i).Type.Kind() {
		case reflect.Pointer, reflect.String, reflect.Slice, reflect.Map, reflect.Interface:
			t.Errorf("row field %s holds a pointer; the collector would scan every row", typ.Field(i).Name)
		}
	}
}

func TestCubeCountsEveryPlacedReceivingAreaOnce(t *testing.T) {
	ix := testIndex()
	c := ix.Cube(Windows[1], now) // 7d
	// a1 twice, a2 twice, a3 once; the unplaced observer is not counted.
	if got := cubeTotal(c); got != 5 {
		t.Fatalf("7d cube total = %d, want 5", got)
	}
	day := ix.Cube(Windows[0], now)
	if got := cubeTotal(day); got != 3 {
		t.Errorf("24h cube total = %d, want 3 (the 3-day-old advert is outside)", got)
	}
	if fmt.Sprint(c.Heard) != "[SCO IOI NEE]" {
		t.Errorf("heard = %v, want areas in configured order", c.Heard)
	}
	if fmt.Sprint(c.Origins) != "[SCO IOI unknown]" {
		t.Errorf("origins = %v, want configured order then unknown", c.Origins)
	}
	if len(c.Buckets) == 0 || c.BucketSeconds != 3600 {
		t.Errorf("7d buckets %d of %ds, want hourly buckets", len(c.Buckets), c.BucketSeconds)
	}
	// a2 heard in the North East went through Scotland.
	found := false
	for _, r := range c.Rows {
		if c.Heard[r[2]] == "NEE" {
			found = true
			if c.Through[r[7]] != "SCO" || c.Channels[r[8]] != "Public" || c.Scopes[r[4]].Key != "#ioi" || c.How[r[5]] != HowUnclear {
				t.Errorf("NEE row = %v through %q channel %q scope %q how %q", r, c.Through[r[7]], c.Channels[r[8]], c.Scopes[r[4]].Key, c.How[r[5]])
			}
		}
	}
	if !found {
		t.Error("no row for the packet heard in the North East")
	}
}

func TestScopesAreClassifiedGenerically(t *testing.T) {
	cases := []struct {
		route  int
		scope  string
		scoped bool
		key    string
		class  string
	}{
		{2, "", false, "direct", ClassDirect},
		{3, "sco", true, "direct", ClassDirect},
		{1, "", false, "unscoped", ClassUnscoped},
		{0, "", false, "unrecognised", ClassUnrecognised},
		{0, "", true, "unrecognised", ClassUnrecognised},
		{0, "#SCO-EDI", true, "#sco-edi", ClassScoped},
	}
	for _, c := range cases {
		s := ScopeOf(c.route, c.scope, c.scoped)
		if s.Key != c.key || s.Class != c.class {
			t.Errorf("ScopeOf(%d, %q, %v) = %+v, want %s/%s", c.route, c.scope, c.scoped, s, c.key, c.class)
		}
	}
}

func TestEveryCubeFigureMatchesItsPacketList(t *testing.T) {
	ix := testIndex()
	filters := [][]Filter{
		nil,
		{{D: "heard", V: []string{"SCO"}}},
		{{D: "border", V: []string{"crossed"}}},
		{{D: "kind", V: []string{"adverts"}}},
		{{D: "kind", V: []string{"adverts"}, N: 1}},
		{{D: "through", V: []string{"SCO"}}},
		{{D: "through", V: []string{"straight"}}},
		{{D: "origin", V: []string{"unknown"}}},
		{{D: "channel", V: []string{"Public"}}, {D: "how", V: []string{HowUnclear}}},
		{{D: "routing", V: []string{"direct"}}},
		{{D: "scope", V: []string{"#ioi"}}, {D: "sclass", V: []string{ClassScoped}}},
	}
	for _, w := range Windows[:2] {
		c := ix.Cube(w, now)
		for _, fs := range filters {
			want := 0
			for _, r := range c.Rows {
				if cubeRowPasses(c, r, fs) {
					want += r[9]
				}
			}
			got := ix.Packets(w, now, 0, 0, fs, nil)
			if got.Total != want || len(got.Rows) != want {
				t.Errorf("%s %+v: list has %d (%d rows), cube says %d", w.Key, fs, got.Total, len(got.Rows), want)
			}
		}
	}
}

// cubeRowPasses applies filters to a cube row the way the page does.
func cubeRowPasses(c Cube, r [10]int, fs []Filter) bool {
	for _, f := range fs {
		var v string
		switch f.D {
		case "heard":
			v = c.Heard[r[2]]
		case "origin":
			v = c.Origins[r[1]]
		case "border":
			o, h := c.Origins[r[1]], c.Heard[r[2]]
			v = "crossed"
			if o == "unknown" {
				v = "unknown"
			} else if o == h {
				v = "local"
			}
		case "kind":
			v = KindOf(c.Types[r[3]], c.Routes[r[6]])
		case "through":
			v = c.Through[r[7]]
			if v == "" {
				v = "straight"
			}
		case "channel":
			v = c.Channels[r[8]]
		case "how":
			v = c.How[r[5]]
		case "routing":
			v = "direct"
			if rt := c.Routes[r[6]]; rt == 0 || rt == 1 {
				v = "flood"
			}
		case "scope":
			v = c.Scopes[r[4]].Key
		case "sclass":
			v = c.Scopes[r[4]].Class
		}
		if !f.matches(v) {
			return false
		}
	}
	return true
}

func TestPacketsAreNewestFirstWithinBounds(t *testing.T) {
	ix := testIndex()
	res := ix.Packets(Windows[1], now, 0, 0, nil, func(k string) (string, string) { return "relay " + k, "SCO" })
	if len(res.Rows) != 5 {
		t.Fatalf("rows = %d, want 5", len(res.Rows))
	}
	for i := 1; i < len(res.Rows); i++ {
		if res.Rows[i].At > res.Rows[i-1].At {
			t.Fatalf("rows not newest first: %v then %v", res.Rows[i-1].At, res.Rows[i].At)
		}
	}
	if res.Rows[0].Hash != "00000000000000a3" || res.Rows[0].TypeName != "Direct message" {
		t.Errorf("newest = %+v", res.Rows[0])
	}
	last := res.Rows[len(res.Rows)-1]
	if last.Hash != "00000000000000a1" || last.Kind != "adverts" || last.Border == "" {
		t.Errorf("oldest = %+v", last)
	}
	// from/to narrow to one bucket.
	b := ix.Packets(Windows[1], now, at(2*time.Hour)-1, at(2*time.Hour)+1, nil, nil)
	if b.Total != 2 {
		t.Errorf("one-bucket list = %d, want the 2 rows of a2", b.Total)
	}
}

func TestHashesSurviveTheIndex(t *testing.T) {
	ix := New(Options{Areas: []string{"SCO"}})
	ix.Replace(0, []origin.Record{
		rec("ABCDEF0123456789", at(time.Minute), 2, 1, "", false, "SCO", nil, "SCO:u"),
		rec("short", at(time.Minute), 2, 1, "", false, "SCO", nil, "SCO:u"),
	})
	got := map[string]bool{}
	for _, r := range ix.Packets(Windows[0], now, 0, 0, nil, nil).Rows {
		got[r.Hash] = true
	}
	if !got["abcdef0123456789"] || !got["short"] {
		t.Errorf("hashes = %v, want both back (hex ones lower case)", got)
	}
}

func TestReplaceSwapsTheTailAndPruneDropsTheHead(t *testing.T) {
	ix := testIndex()
	// Re-derive the last 30 minutes: the unplaced advert goes, a new packet arrives.
	ix.Replace(at(30*time.Minute), []origin.Record{
		rec("00000000000000b1", at(5*time.Minute), 2, 1, "", false, "SCO", nil, "SCO:u", "IOI:a"),
	})
	if n := ix.Packets(Windows[0], now, 0, 0, nil, nil).Total; n != 5 {
		t.Errorf("24h after replace = %d, want 5 (a2 x2, a3, b1 x2)", n)
	}
	ix.Prune(at(48 * time.Hour))
	if n := ix.Packets(Windows[1], now, 0, 0, nil, nil).Total; n != 5 {
		t.Errorf("7d after prune = %d, want 5 (the 3-day-old advert dropped)", n)
	}
	if ix.Len() != 5 {
		t.Errorf("Len = %d, want 5", ix.Len())
	}
	// Records arriving out of order are kept sorted by time.
	ix.Replace(at(20*time.Minute), []origin.Record{
		rec("00000000000000c2", at(1*time.Minute), 2, 1, "", false, "SCO", nil, "SCO:u"),
		rec("00000000000000c1", at(15*time.Minute), 2, 1, "", false, "SCO", nil, "SCO:u"),
	})
	rows := ix.Packets(Windows[0], now, 0, 0, nil, nil).Rows
	if len(rows) < 2 {
		t.Fatalf("rows after an unsorted replace = %d, want at least 2", len(rows))
	}
	if rows[0].Hash != "00000000000000c2" || rows[1].Hash != "00000000000000c1" {
		t.Errorf("order after an unsorted replace: %s, %s", rows[0].Hash, rows[1].Hash)
	}
}

func TestSummary(t *testing.T) {
	ix := testIndex()
	s := ix.Summary(Windows[1], now, func(k string) (string, string) { return "R-" + k, "SCO" })
	if s.Packets != 4 || s.CrossBorder != 2 {
		t.Errorf("packets %d crossBorder %d, want 4 and 2", s.Packets, s.CrossBorder)
	}
	if s.Bases[origin.BasisRelay] != 4 {
		t.Errorf("bases = %v", s.Bases)
	}
	cell := func(cells []Cell, o, h string) Cell {
		for _, c := range cells {
			if c.Origin == o && c.Heard == h {
				return c
			}
		}
		return Cell{}
	}
	if c := cell(s.Matrix, "SCO", "IOI"); c.Packets != 1 || c.Across != 1 {
		t.Errorf("SCO->IOI = %+v", c)
	}
	if c := cell(s.Matrix, "IOI", "SCO"); c.Packets != 1 || c.Relayed != 1 {
		t.Errorf("IOI->SCO = %+v", c)
	}
	if c := cell(s.MatrixByGroup["adverts"], "SCO", "SCO"); c.Packets != 1 {
		t.Errorf("adverts SCO->SCO = %+v", c)
	}
	if c := cell(s.MatrixByGroup["messages"], "IOI", "NEE"); c.Packets != 1 {
		t.Errorf("messages IOI->NEE = %+v", c)
	}
	var areas []string
	for _, a := range s.Areas {
		areas = append(areas, fmt.Sprintf("%s:%v:%d", a.Code, a.Observed, a.Originated))
	}
	if fmt.Sprint(areas) != "[SCO:true:1 IOI:true:1 NEE:true:1]" {
		t.Errorf("areas = %v", areas)
	}
	var flow *Flow
	for i := range s.Flows {
		if s.Flows[i].Origin == "IOI" && s.Flows[i].Heard == "NEE" {
			flow = &s.Flows[i]
		}
	}
	if flow == nil || flow.ViaHome != 1 {
		t.Errorf("IOI->NEE flow = %+v, want one packet via home", flow)
	}
}

func TestSummaryNamesRelays(t *testing.T) {
	ix := New(Options{Areas: []string{"SCO", "IOI"}, Home: "SCO"})
	r := rec("00000000000000d1", at(time.Minute), 2, 1, "", false, "SCO", []string{"SCO", "IOI"}, "IOI:r")
	r.Heard[0].From, r.Heard[0].Gate = "cc33", "ab22"
	ix.Replace(0, []origin.Record{r})
	s := ix.Summary(Windows[0], now, func(k string) (string, string) { return "R-" + k, "X" })
	if len(s.Flows) != 1 || len(s.Flows[0].From) != 1 || s.Flows[0].From[0].Name != "R-cc33" || s.Flows[0].Gates[0].Key != "ab22" {
		t.Errorf("flows = %+v", s.Flows)
	}
	p := ix.Packets(Windows[0], now, 0, 0, nil, func(k string) (string, string) { return "R-" + k, "X" })
	if len(p.Rows) != 1 {
		t.Fatalf("packet rows = %d, want 1", len(p.Rows))
	}
	if p.Rows[0].HeardFrom != "R-cc33" || p.Rows[0].Boundary != "R-ab22" {
		t.Errorf("packet row = %+v", p.Rows[0])
	}
}

func TestCoverage(t *testing.T) {
	ix := New(Options{Areas: []string{"SCO"}})
	if c := ix.Cube(Windows[0], now); c.Complete {
		t.Error("an index that was never filled is not complete")
	}
	ix.SetCovered(at(25 * time.Hour))
	if c := ix.Cube(Windows[0], now); !c.Complete {
		t.Error("covered past the window start should be complete")
	}
	if s := ix.Summary(Windows[1], now, nil); s.Complete || s.CoveredFrom.Unix() != at(25*time.Hour) {
		t.Errorf("7d with 25h covered: complete %v from %v", s.Complete, s.CoveredFrom)
	}
}

// bigIndex fills an index with n packets over 7 days, two areas each.
func bigIndex(n int) *Index {
	ix := New(Options{Areas: []string{"SCO", "IOI", "NEE", "CUM", "WLS"}, Home: "SCO"})
	areas := []string{"SCO", "IOI", "NEE", "CUM", "WLS"}
	recs := make([]origin.Record, 0, n)
	step := int64(7*24*3600) / int64(n)
	for i := 0; i < n; i++ {
		o := areas[i%5]
		h := areas[(i/5)%5]
		r := rec(fmt.Sprintf("%016x", i), now.Unix()-int64(7*24*3600)+int64(i)*step, i%12, i%4, fmt.Sprintf("s%d", i%7), i%3 == 0, o, []string{o, h}, h+":r", o+":a")
		r.Channel = fmt.Sprintf("ch%d", i%9)
		recs = append(recs, r)
	}
	ix.Replace(0, recs)
	return ix
}

func TestSevenDayCubeIsFast(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	ix := bigIndex(60_000) // about ScotMesh's 7-day volume
	start := time.Now()
	c := ix.Cube(Windows[1], now)
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("7-day cube over %d rows took %v, want under 100 ms", ix.Len(), d)
	}
	if cubeTotal(c) != ix.Len() {
		t.Errorf("cube total %d, index rows %d", cubeTotal(c), ix.Len())
	}
}

func BenchmarkCube7d(b *testing.B) {
	ix := bigIndex(60_000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ix.Cube(Windows[1], now)
	}
}

func BenchmarkReplace(b *testing.B) {
	ix := bigIndex(60_000)
	tail := make([]origin.Record, 0, 300)
	for i := 0; i < 300; i++ {
		tail = append(tail, rec(fmt.Sprintf("%016x", 1_000_000+i), now.Unix()-int64(i), 5, 1, "sco", true, "SCO", []string{"SCO"}, "SCO:r", "IOI:a"))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ix.Replace(now.Unix()-1800, tail)
	}
}
