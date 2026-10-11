package main

import (
	"context"
	"sort"
	"testing"

	"github.com/gorilla/mux"
)

// packetOriginRouter serves /api/packets from a loaded packet store, with the
// Data tab's index beside it when the fixture has one.
func packetOriginRouter(t *testing.T, f *dataFixture) *mux.Router {
	t.Helper()
	store := NewPacketStore(f.db, nil)
	f.cfg.applyListLimitsDefaults()
	store.config = f.cfg
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if f.svc != nil {
		if err := f.svc.fill(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	srv := &Server{db: f.db, cfg: f.cfg, store: store, data: f.svc}
	r := mux.NewRouter()
	r.HandleFunc("/api/packets/{id}", srv.handlePacketDetail).Methods("GET")
	r.HandleFunc("/api/packets", srv.handlePackets).Methods("GET")
	return r
}

type packetListWithOrigins struct {
	Packets []struct {
		Hash string `json:"hash"`
	} `json:"packets"`
	Origins map[string]struct {
		Area  string `json:"area"`
		Basis string `json:"basis"`
	} `json:"origins"`
}

func (p packetListWithOrigins) hashes() []string {
	out := []string{}
	for _, x := range p.Packets {
		out = append(out, x.Hash)
	}
	sort.Strings(out)
	return out
}

// An area keeps the packets that started there, whatever their type: the
// Scottish channel message p1 is not an advert but started in SCO (its first
// relay stands in Edinburgh), and the Irish advert a2 did not.
func TestPacketsAreaFilterKeepsPacketsByOrigin(t *testing.T) {
	f := newDataFixture(t, true)
	r := packetOriginRouter(t, f)
	for _, path := range []string{"/api/packets?area=SCO", "/api/packets?area=SCO&groupByHash=true"} {
		var res packetListWithOrigins
		if code := getJSON(t, r, path, &res); code != 200 {
			t.Fatalf("%s = %d", path, code)
		}
		if got := res.hashes(); len(got) != 1 || got[0] != "00000000000000p1" {
			t.Errorf("%s = %v, want just the Scottish channel message", path, got)
		}
	}
	var ioi packetListWithOrigins
	getJSON(t, r, "/api/packets?area=IOI", &ioi)
	if got := ioi.hashes(); len(got) != 1 || got[0] != "00000000000000a2" {
		t.Errorf("area=IOI = %v, want the Irish advert", got)
	}
}

// With areas the list says where each packet started and how that was decided.
func TestPacketsListCarriesOrigins(t *testing.T) {
	f := newDataFixture(t, true)
	r := packetOriginRouter(t, f)
	for _, path := range []string{"/api/packets", "/api/packets?groupByHash=true"} {
		var res packetListWithOrigins
		getJSON(t, r, path, &res)
		if res.Origins == nil {
			t.Fatalf("%s: no origins", path)
		}
		if o := res.Origins["00000000000000p1"]; o.Area != "SCO" || o.Basis != "first-relay" {
			t.Errorf("%s: p1 origin = %+v, want SCO by first relay", path, o)
		}
		if o := res.Origins["00000000000000a2"]; o.Area != "IOI" || o.Basis != "advert" {
			t.Errorf("%s: a2 origin = %+v, want IOI by its advert", path, o)
		}
	}
	var detail struct {
		Origin *struct {
			Area  string `json:"area"`
			Basis string `json:"basis"`
		} `json:"origin"`
	}
	getJSON(t, r, "/api/packets/00000000000000p1", &detail)
	if detail.Origin == nil || detail.Origin.Area != "SCO" {
		t.Errorf("packet detail origin = %+v, want SCO", detail.Origin)
	}
}

// Without the Data tab there is no origin to give, and the list says nothing.
func TestPacketsListWithoutDataHasNoOrigins(t *testing.T) {
	f := newDataFixture(t, false)
	r := packetOriginRouter(t, f)
	var res packetListWithOrigins
	getJSON(t, r, "/api/packets", &res)
	if res.Origins != nil {
		t.Errorf("origins = %v, want none without the Data tab", res.Origins)
	}
}
