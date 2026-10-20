package main

import (
	"reflect"
	"testing"
	"unsafe"
)

// The store holds hundreds of thousands of observations, so StoreObs's size
// class is the cost of each one. Observer name and IATA repeat across every
// observation from the same observer and the per-observation raw frame is
// never retained (#881), so neither needs its own string header here.
func TestStoreObsFitsSmallerSizeClass(t *testing.T) {
	if got := unsafe.Sizeof(StoreObs{}); got > 112 {
		t.Fatalf("unsafe.Sizeof(StoreObs{}) = %d, want <= 112 (the 160-byte size class costs ~15 MB per 300k observations)", got)
	}
}
func TestStoreObsObserverLabel(t *testing.T) {
	var a, b, c StoreObs
	a.setObserverLabel("Hill Top", "EDI")
	b.setObserverLabel("Hill Top", "EDI")
	c.setObserverLabel("Valley", "")

	if a.ObserverName() != "Hill Top" || a.ObserverIATA() != "EDI" {
		t.Errorf("a = %q/%q, want Hill Top/EDI", a.ObserverName(), a.ObserverIATA())
	}
	if c.ObserverName() != "Valley" || c.ObserverIATA() != "" {
		t.Errorf("c = %q/%q, want Valley/\"\"", c.ObserverName(), c.ObserverIATA())
	}
	if a.label != b.label {
		t.Errorf("equal labels got different slots %d and %d", a.label, b.label)
	}
	var zero StoreObs
	if zero.ObserverName() != "" || zero.ObserverIATA() != "" {
		t.Errorf("zero StoreObs label = %q/%q, want empty", zero.ObserverName(), zero.ObserverIATA())
	}
	zero.setObserverLabel("", "")
	if zero.label != 0 {
		t.Errorf("empty label took slot %d, want 0", zero.label)
	}
}

// #881 / #1999: raw frames are read from SQLite on demand, never held per
// observation.
func TestStoreObsHoldsNoRawFrame(t *testing.T) {
	if _, ok := reflect.TypeOf(StoreObs{}).FieldByName("RawHex"); ok {
		t.Fatal("StoreObs has a RawHex field again")
	}
}

// withLabel sets an observation's observer name and IATA for a test fixture.
func withLabel(o *StoreObs, name, iata string) *StoreObs {
	o.setObserverLabel(name, iata)
	return o
}
