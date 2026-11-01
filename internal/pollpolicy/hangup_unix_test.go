//go:build !windows

package pollpolicy

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestWatcherStartSIGHUP(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	writeConfig(t, p, `{"neighborBattery":{"polling":{"directMinutes":90}}}`, t0)
	h := NewHolder(nil)
	w := NewWatcher(h, []string{p}, nil)
	stop := w.Start(time.Hour)
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Skip("cannot signal self:", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for h.Policy(t0).DirectMinutes != 90 {
		if time.Now().After(deadline) {
			t.Fatal("SIGHUP did not reload")
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	stop() // idempotent
}
