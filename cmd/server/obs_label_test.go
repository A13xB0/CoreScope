package main

import (
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
