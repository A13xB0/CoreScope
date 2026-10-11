package main

import "unique"

// internStr returns the canonical copy of s, so a store of hundreds of
// thousands of observations keeps one copy of each string that repeats
// across them (observer IDs, names, IATA codes, directions) rather than the
// one each row scan allocates.
//
// unique.Make holds canonical copies weakly: a value no observation uses
// any more is dropped by the GC, so the table needs no cap and never holds
// a retired observer's strings. Looking up an existing value takes no lock,
// so loaders and ingest interning on many cores do not contend.
func internStr(s string) string {
	if s == "" {
		return ""
	}
	return unique.Make(s).Value()
}
