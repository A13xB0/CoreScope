//go:build windows

package pollpolicy

import "os"

// notifyHangup: Windows has no SIGHUP; only the mtime check reloads.
func notifyHangup() (<-chan os.Signal, func()) {
	return make(chan os.Signal), func() {}
}
