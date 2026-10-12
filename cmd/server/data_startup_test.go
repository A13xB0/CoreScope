package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The Data tab's 7-day fill must not run on top of the packet store's
// startup load: together they pushed a 1.5 GB container over its limit
// every time the background fill reached its subpath index, about 50 s
// after each start, and the server was OOM-killed in a loop. The fill waits
// for the store's startup-load-done signal; until then the tab says it is
// filling (503) rather than "not enabled" (404).
func TestDataFillWaitsForTheStartupLoad(t *testing.T) {
	f := newDataFixture(t, true)
	loaded := make(chan struct{})
	f.svc.waitFor = loaded
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.svc.run(ctx)

	time.Sleep(150 * time.Millisecond)
	if f.svc.index() != nil {
		t.Fatal("the fill started before the startup load finished")
	}
	var cfg struct {
		Filling bool `json:"filling"`
	}
	r := dataRouter(f)
	getJSON(t, r, "/api/data/config", &cfg)
	if !cfg.Filling {
		t.Error("while waiting, /api/data/config should report filling")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/data/cube?window=24h", nil))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "filling") {
		t.Errorf("cube while waiting = %d %q, want 503 saying it is filling", w.Code, w.Body.String())
	}

	close(loaded)
	deadline := time.Now().Add(5 * time.Second)
	for f.svc.index() == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if f.svc.index() == nil {
		t.Fatal("the fill did not start once the startup load finished")
	}
}

// Without a signal (tests, or no packet store) the fill starts at once.
func TestDataFillStartsAtOnceWithoutASignal(t *testing.T) {
	f := newDataFixture(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.svc.run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for f.svc.index() == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if f.svc.index() == nil {
		t.Fatal("the fill never started")
	}
}
