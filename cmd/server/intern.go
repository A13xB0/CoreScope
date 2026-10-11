package main

import "sync"

// observerStrings holds one copy of each string that repeats across many
// observations: observer IDs, names, IATA codes and directions. There are a
// few hundred observers, so the table stays small; internMaxEntries only
// guards against a feed that sends a new value on every row.
var observerStrings = struct {
	sync.Mutex
	m map[string]string
}{m: make(map[string]string)}

const internMaxEntries = 1 << 16

// internStr returns the shared copy of s, so a store of hundreds of
// thousands of observations keeps one copy of each observer string rather
// than the one each row scan allocates.
func internStr(s string) string {
	if s == "" {
		return ""
	}
	observerStrings.Lock()
	defer observerStrings.Unlock()
	if v, ok := observerStrings.m[s]; ok {
		return v
	}
	if len(observerStrings.m) < internMaxEntries {
		observerStrings.m[s] = s
	}
	return s
}
