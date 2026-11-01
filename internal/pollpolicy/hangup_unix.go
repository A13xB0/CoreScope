//go:build !windows

package pollpolicy

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyHangup delivers SIGHUP on the returned channel until stop is called.
func notifyHangup() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	return ch, func() { signal.Stop(ch) }
}
