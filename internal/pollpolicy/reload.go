package pollpolicy

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// ReloadInterval is how often Watch checks the config file's mtime.
const ReloadInterval = 30 * time.Second

// Holder is the polling block in force, swapped whole on reload so readers
// never see a half-applied change. The zero value and a nil Holder both
// serve the defaults.
type Holder struct {
	cfg atomic.Pointer[Config]
}

// NewHolder returns a Holder serving c (nil = defaults).
func NewHolder(c *Config) *Holder {
	h := &Holder{}
	h.Set(c)
	return h
}

// Set installs c (nil = defaults).
func (h *Holder) Set(c *Config) {
	if c == nil {
		c = &Config{}
	}
	h.cfg.Store(c)
}

// Config is the raw block in force; never nil.
func (h *Holder) Config() *Config {
	if h == nil {
		return &Config{}
	}
	if c := h.cfg.Load(); c != nil {
		return c
	}
	return &Config{}
}

// Policy is the effective policy at now.
func (h *Holder) Policy(now time.Time) *Policy {
	return h.Config().Effective(now)
}

// ReadFile reads neighborBattery.polling from a config.json. A file without
// the block yields an empty Config (the defaults).
func ReadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		NeighborBattery *struct {
			Polling *Config `json:"polling"`
		} `json:"neighborBattery"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if doc.NeighborBattery == nil || doc.NeighborBattery.Polling == nil {
		return &Config{}, nil
	}
	return doc.NeighborBattery.Polling, nil
}

// Watcher re-reads the polling block into a Holder when the config file
// changes. paths are tried in order, like the binaries' own config lookup;
// the first that exists is the one watched.
type Watcher struct {
	holder *Holder
	paths  []string
	logf   func(format string, args ...interface{})

	mu      sync.Mutex
	path    string
	modTime time.Time
	size    int64
}

// NewWatcher returns a Watcher for holder. The current file's stamp is
// recorded, so the first Check reloads only if the file changed since.
func NewWatcher(holder *Holder, paths []string, logf func(string, ...interface{})) *Watcher {
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	w := &Watcher{holder: holder, paths: paths, logf: logf}
	if p, fi := w.resolve(); fi != nil {
		w.path, w.modTime, w.size = p, fi.ModTime(), fi.Size()
	}
	return w
}

func (w *Watcher) resolve() (string, os.FileInfo) {
	for _, p := range w.paths {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, fi
		}
	}
	return "", nil
}

// Check reloads the block when the file's mtime or size changed (or always,
// with force). A file that fails to read or parse leaves the policy in force.
// It reports whether a new block was installed.
func (w *Watcher) Check(force bool) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, fi := w.resolve()
	if fi == nil {
		return false, nil
	}
	if !force && p == w.path && fi.ModTime().Equal(w.modTime) && fi.Size() == w.size {
		return false, nil
	}
	c, err := ReadFile(p)
	if err != nil {
		w.logf("[polling-policy] reload of %s failed, keeping the policy in force: %v", p, err)
		return false, err
	}
	w.path, w.modTime, w.size = p, fi.ModTime(), fi.Size()
	before := w.holder.Policy(time.Now()).Version
	w.holder.Set(c)
	after := w.holder.Policy(time.Now()).Version
	if before != after {
		w.logf("[polling-policy] reloaded from %s: policy %s -> %s", p, before, after)
	}
	return true, nil
}

// Start checks every interval and on SIGHUP (where the platform has it)
// until the returned stop is called.
func (w *Watcher) Start(interval time.Duration) (stop func()) {
	if interval <= 0 {
		interval = ReloadInterval
	}
	hup, unhup := notifyHangup()
	done := make(chan struct{})
	var once sync.Once
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		defer unhup()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				w.Check(false)
			case <-hup:
				w.logf("[polling-policy] SIGHUP: re-reading the polling policy")
				w.Check(true)
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}
