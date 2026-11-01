package pollpolicy

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	keyA = strings.Repeat("a", 64)
	keyB = strings.Repeat("b", 64)
	t0   = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
)

func TestEffectiveDefaults(t *testing.T) {
	for _, c := range []*Config{nil, {}, {DirectMinutes: -5, MaxRequestsPerWindow: 0}} {
		p := c.Effective(t0)
		if p.DirectMinutes != 60 || p.OneHopMinutes != 120 || p.StandbyGraceMinutes != 30 ||
			p.MaxRequestsPerWindow != 2 || p.TakeoverAfter != 2 {
			t.Errorf("%+v: defaults = %+v", c, p)
		}
		if p.Paused || p.HasMinVersion() || len(p.ExcludedNodes) != 0 || p.ExcludedNodes == nil {
			t.Errorf("%+v: want nothing paused/excluded/min, non-nil lists: %+v", c, p)
		}
		if len(p.Version) != 8 {
			t.Errorf("version %q", p.Version)
		}
	}
	if (*Config)(nil).Effective(t0).Version != (&Config{}).Effective(t0).Version {
		t.Error("nil and empty config hash differently")
	}
}

func TestEffectiveParsing(t *testing.T) {
	c := &Config{
		DirectMinutes: 45, OneHopMinutes: 90, StandbyGraceMinutes: 15, MaxRequestsPerWindow: 3, TakeoverAfterMissedWindows: 4,
		ExcludedNodes:    []string{strings.ToUpper(keyA), "nonsense", keyA, " " + keyB + " "},
		BlockedObservers: []string{keyB, "12ab"},
		MinPluginVersion: "v0.8", MinVersionReason: " login storm ",
		NodeMinutes: map[string]int{strings.ToUpper(keyA): 240, keyB: 0, "zz": 5},
	}
	p := c.Effective(t0)
	if p.DirectMinutes != 45 || p.OneHopMinutes != 90 || p.StandbyGraceMinutes != 15 || p.MaxRequestsPerWindow != 3 || p.TakeoverAfter != 4 {
		t.Errorf("limits %+v", p)
	}
	if len(p.ExcludedNodes) != 2 || p.ExcludedNodes[0] != keyA || p.ExcludedNodes[1] != keyB {
		t.Errorf("excluded %v", p.ExcludedNodes)
	}
	if !p.IsExcluded(strings.ToUpper(keyB)) || p.IsExcluded(strings.Repeat("c", 64)) {
		t.Error("IsExcluded")
	}
	if len(p.BlockedObservers) != 1 || !p.IsBlocked(strings.ToUpper(keyB)) || p.IsBlocked(keyA) {
		t.Errorf("blocked %v", p.BlockedObservers)
	}
	if p.MinPluginVersion != "0.8.0" || p.MinVersionReason != "login storm" {
		t.Errorf("min %q %q", p.MinPluginVersion, p.MinVersionReason)
	}
	if len(p.NodeMinutes) != 1 || p.NodeMinutes[keyA] != 240 {
		t.Errorf("node minutes %v", p.NodeMinutes)
	}
	cases := []struct {
		node string
		hops int
		want int
	}{{keyA, 0, 240}, {keyA, 1, 240}, {keyB, 0, 45}, {keyB, 1, 90}, {keyB, 3, 90}}
	for _, tc := range cases {
		if got := p.WindowMinutes(tc.node, tc.hops); got != tc.want {
			t.Errorf("window(%.4s,%d) = %d, want %d", tc.node, tc.hops, got, tc.want)
		}
	}
	if p.Window(keyB, 0) != 45*time.Minute || p.Grace() != 15*time.Minute {
		t.Error("durations")
	}
	// An unparsable minimum means none.
	if q := (&Config{MinPluginVersion: "latest"}).Effective(t0); q.HasMinVersion() || q.MinPluginVersion != "" {
		t.Errorf("bad min kept: %+v", q)
	}
}

func TestPausedUntil(t *testing.T) {
	cases := []struct {
		name       string
		cfg        Config
		paused     bool
		until      string
		wantReason string
	}{
		{"not paused", Config{PausedReason: "x", PausedUntil: "2026-11-02T00:00:00Z"}, false, "", ""},
		{"no end", Config{Paused: true, PausedReason: "survey"}, true, "", "survey"},
		{"future end", Config{Paused: true, PausedReason: "survey", PausedUntil: "2026-11-01T12:00:00+01:00"}, true, "2026-11-01T11:00:00Z", "survey"},
		{"end passed", Config{Paused: true, PausedReason: "survey", PausedUntil: "2026-11-01T09:59:59Z"}, false, "", ""},
		{"end now", Config{Paused: true, PausedUntil: "2026-11-01T10:00:00Z"}, false, "", ""},
		{"bad end holds", Config{Paused: true, PausedUntil: "monday"}, true, "", ""},
	}
	for _, tc := range cases {
		p := tc.cfg.Effective(t0)
		if p.Paused != tc.paused || p.PausedUntil != tc.until || p.PausedReason != tc.wantReason {
			t.Errorf("%s: paused=%v until=%q reason=%q", tc.name, p.Paused, p.PausedUntil, p.PausedReason)
		}
	}
	// The version changes when the pause lapses, so plugins notice.
	c := Config{Paused: true, PausedUntil: "2026-11-01T11:00:00Z"}
	if c.Effective(t0).Version == c.Effective(t0.Add(2*time.Hour)).Version {
		t.Error("version unchanged across pause expiry")
	}
	if c.Effective(t0.Add(2*time.Hour)).Version != (&Config{}).Effective(t0).Version {
		t.Error("lapsed pause should hash like no pause")
	}
}

func TestPolicyVersionHash(t *testing.T) {
	base := Config{ExcludedNodes: []string{keyA, keyB}, NodeMinutes: map[string]int{keyA: 30, keyB: 40}}
	same := Config{ExcludedNodes: []string{strings.ToUpper(keyB), keyA, keyA}, NodeMinutes: map[string]int{keyB: 40, strings.ToUpper(keyA): 30}}
	v := base.Effective(t0).Version
	if v != same.Effective(t0).Version {
		t.Error("equivalent configs hash differently")
	}
	if v != base.Effective(t0.Add(48*time.Hour)).Version {
		t.Error("version depends on the clock without a pause")
	}
	for name, c := range map[string]Config{
		"direct":  {ExcludedNodes: base.ExcludedNodes, NodeMinutes: base.NodeMinutes, DirectMinutes: 61},
		"exclude": {ExcludedNodes: []string{keyA}, NodeMinutes: base.NodeMinutes},
		"window":  {ExcludedNodes: base.ExcludedNodes, NodeMinutes: map[string]int{keyA: 31, keyB: 40}},
		"min":     {ExcludedNodes: base.ExcludedNodes, NodeMinutes: base.NodeMinutes, MinPluginVersion: "0.8.0"},
		"paused":  {ExcludedNodes: base.ExcludedNodes, NodeMinutes: base.NodeMinutes, Paused: true},
	} {
		if c.Effective(t0).Version == v {
			t.Errorf("%s: version unchanged", name)
		}
	}
	for _, r := range v {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("version %q not hex", v)
		}
	}
}

func TestVersionCompare(t *testing.T) {
	parse := []struct {
		in   string
		want string
		ok   bool
	}{
		{"0.8.0", "0.8.0", true}, {"v1.2.3", "1.2.3", true}, {"0.8", "0.8.0", true}, {"2", "2.0.0", true},
		{"0.8.1-rc1", "0.8.1", true}, {"0.10.0+build", "0.10.0", true},
		{"", "0.0.0", false}, {"x.y.z", "0.0.0", false}, {"1.2.3.4", "0.0.0", false}, {"1..2", "0.0.0", false}, {"-1.0.0", "0.0.0", false},
	}
	for _, tc := range parse {
		v, ok := ParseVersion(tc.in)
		if ok != tc.ok || v.String() != tc.want {
			t.Errorf("ParseVersion(%q) = %v %v", tc.in, v, ok)
		}
	}
	plugin := []struct{ in, want string }{
		{"openhop.neighbour_battery/0.8.0", "0.8.0"}, {"openhop.neighbour_battery/0.10.2", "0.10.2"},
		{"0.7.9", "0.7.9"}, {"openhop.neighbour_battery", ""}, {"", ""}, {"a/b/1.0", "1.0.0"},
	}
	for _, tc := range plugin {
		if got := PluginVersionString(tc.in); got != tc.want {
			t.Errorf("PluginVersionString(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	cmp := []struct {
		a, b string
		want int
	}{{"0.8.0", "0.8.0", 0}, {"0.8.1", "0.8.0", 1}, {"0.9.0", "0.10.0", -1}, {"1.0.0", "0.99.99", 1}, {"0.8", "0.8.0", 0}}
	for _, tc := range cmp {
		a, _ := ParseVersion(tc.a)
		b, _ := ParseVersion(tc.b)
		if got := a.Compare(b); got != tc.want {
			t.Errorf("%s vs %s = %d", tc.a, tc.b, got)
		}
	}
	p := (&Config{MinPluginVersion: "0.8.2"}).Effective(t0)
	ok := []struct {
		v    string
		want bool
	}{
		{"0.8.2", true}, {"openhop.neighbour_battery/0.9.0", true}, {"0.8.1", false},
		{"openhop.neighbour_battery/0.8.0", false}, {"", false}, {"garbage", false},
	}
	for _, tc := range ok {
		if got := p.VersionOK(tc.v); got != tc.want {
			t.Errorf("VersionOK(%q) = %v", tc.v, got)
		}
	}
	if !(&Config{}).Effective(t0).VersionOK("") {
		t.Error("no minimum must accept anything")
	}
}

func writeConfig(t *testing.T, path, body string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	writeConfig(t, p, `{"port":3000}`, t0)
	c, err := ReadFile(p)
	if err != nil || c.Effective(t0).DirectMinutes != 60 {
		t.Fatalf("missing block: %+v %v", c, err)
	}
	writeConfig(t, p, `{"neighborBattery":{"enabled":true,"polling":{"directMinutes":90,"paused":true,"excludedNodes":["`+keyA+`"]}}}`, t0)
	c, err = ReadFile(p)
	if err != nil || c.DirectMinutes != 90 || !c.Paused || len(c.ExcludedNodes) != 1 {
		t.Fatalf("block: %+v %v", c, err)
	}
	writeConfig(t, p, `{"neighborBattery":`, t0)
	if _, err := ReadFile(p); err == nil {
		t.Error("broken JSON accepted")
	}
}

func TestWatcherReload(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "config.json")
	p := filepath.Join(dir, "data", "config.json")
	os.MkdirAll(filepath.Dir(p), 0o755)
	writeConfig(t, p, `{"neighborBattery":{"polling":{"directMinutes":90}}}`, t0)
	c, _ := ReadFile(p)
	h := NewHolder(c)
	var logs []string
	var mu sync.Mutex
	w := NewWatcher(h, []string{missing, p}, func(f string, a ...interface{}) {
		mu.Lock()
		logs = append(logs, f)
		mu.Unlock()
	})

	// Unchanged file: nothing reloads.
	if changed, _ := w.Check(false); changed {
		t.Error("reloaded an unchanged file")
	}
	// New mtime: reloaded.
	writeConfig(t, p, `{"neighborBattery":{"polling":{"directMinutes":30,"paused":true}}}`, t0.Add(time.Minute))
	if changed, err := w.Check(false); !changed || err != nil {
		t.Fatalf("change not picked up: %v", err)
	}
	if pol := h.Policy(t0); pol.DirectMinutes != 30 || !pol.Paused {
		t.Errorf("after reload %+v", pol)
	}
	// A broken edit keeps the policy in force.
	writeConfig(t, p, `{"neighborBattery":{"polling":`, t0.Add(2*time.Minute))
	if changed, err := w.Check(false); changed || err == nil {
		t.Error("broken file installed")
	}
	if pol := h.Policy(t0); pol.DirectMinutes != 30 {
		t.Errorf("broken reload lost the policy: %+v", pol)
	}
	// Force (SIGHUP) re-reads even with the same stamp.
	writeConfig(t, p, `{"neighborBattery":{"polling":{"directMinutes":45}}}`, t0.Add(2*time.Minute))
	if changed, _ := w.Check(true); !changed || h.Policy(t0).DirectMinutes != 45 {
		t.Error("forced reload")
	}
	// The earlier path, once it exists, takes over (same order as LoadConfig).
	writeConfig(t, missing, `{"neighborBattery":{"polling":{"directMinutes":75}}}`, t0)
	if changed, _ := w.Check(false); !changed || h.Policy(t0).DirectMinutes != 75 {
		t.Error("switch to the first config path")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) == 0 {
		t.Error("no reload logged")
	}
}

// Readers racing a reload see either the old or the new block, never a mix
// (run with -race).
func TestHolderConcurrent(t *testing.T) {
	h := NewHolder(nil)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				p := h.Policy(t0)
				if p.DirectMinutes != 60 && p.DirectMinutes != 15 {
					t.Errorf("torn read %d", p.DirectMinutes)
					return
				}
				if (p.DirectMinutes == 15) != p.Paused {
					t.Errorf("mixed block: %+v", p)
					return
				}
			}
		}()
	}
	for i := 0; i < 500; i++ {
		if i%2 == 0 {
			h.Set(&Config{DirectMinutes: 15, Paused: true})
		} else {
			h.Set(nil)
		}
	}
	close(stop)
	wg.Wait()
	var nilH *Holder
	if nilH.Policy(t0).DirectMinutes != 60 {
		t.Error("nil holder")
	}
}
