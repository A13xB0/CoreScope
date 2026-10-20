package main

import (
	"log"
	"math"
	"strings"
	"sync"
	"sync/atomic"
)

// internMax bounds the intern table. Observer IDs, names, IATA codes and
// directions number in the hundreds on a busy instance, so the cap is only a
// guard against an unexpected flood of distinct values.
const internMax = 1 << 16

var (
	internMu    sync.RWMutex
	internTable = make(map[string]string)
)

// internStr returns the canonical copy of s, so a store of hundreds of
// thousands of observations keeps one copy of each string that repeats
// across them (observer IDs, names, IATA codes, directions) rather than the
// one each row scan allocates.
//
// The table holds its copies for the life of the process: they are few, and
// a strongly held copy is what makes the sharing survive garbage collections
// (unique.Make holds its copies weakly, so a value interned again after a GC
// came back as a new copy). Past internMax entries a string is returned as
// it came: nothing is shared, nothing breaks. A hit takes only the read lock
// and does not allocate.
func internStr(s string) string {
	if s == "" {
		return ""
	}
	internMu.RLock()
	c, ok := internTable[s]
	internMu.RUnlock()
	if ok {
		return c
	}
	internMu.Lock()
	defer internMu.Unlock()
	if c, ok := internTable[s]; ok {
		return c
	}
	if len(internTable) >= internMax {
		return s
	}
	c = strings.Clone(s) // do not pin a larger buffer s may be a slice of
	internTable[c] = c
	return c
}

// observerLabel is the observer name and IATA code an observation was
// stored with. Every observation from one observer carries the same pair, so
// StoreObs keeps a uint16 slot in a shared table instead of two string
// headers (32 bytes per observation).
type observerLabel struct{ name, iata string }

var (
	labelMu    sync.RWMutex
	labelIndex = map[observerLabel]uint16{{}: 0}
	// labelTab is only ever appended to under labelMu and republished
	// whole, so a reader indexes the slice it loaded without a lock: the
	// slots it can see are never written again.
	labelTab      atomic.Pointer[[]observerLabel]
	labelFullOnce sync.Once
)

func init() {
	tab := []observerLabel{{}} // slot 0 is the empty label
	labelTab.Store(&tab)
}

// internObserverLabel returns the slot holding (name, iata), adding it if
// it is new. The pairs number in the hundreds; should the table ever fill,
// further new pairs are stored as the empty label and logged once.
func internObserverLabel(name, iata string) uint16 {
	k := observerLabel{name, iata}
	labelMu.RLock()
	i, ok := labelIndex[k]
	labelMu.RUnlock()
	if ok {
		return i
	}
	labelMu.Lock()
	defer labelMu.Unlock()
	if i, ok := labelIndex[k]; ok {
		return i
	}
	tab := *labelTab.Load()
	if len(tab) > math.MaxUint16 {
		labelFullOnce.Do(func() {
			log.Printf("[store] observer label table full (%d name/IATA pairs); new pairs are stored without a name", len(tab))
		})
		return 0
	}
	k = observerLabel{internStr(name), internStr(iata)}
	tab = append(tab, k)
	labelTab.Store(&tab)
	i = uint16(len(tab) - 1)
	labelIndex[k] = i
	return i
}

func observerLabelAt(i uint16) observerLabel { return (*labelTab.Load())[i] }
