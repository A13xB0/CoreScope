package main

import (
	"database/sql"
	"reflect"
	"testing"
)

// Every pointer field on StoreObs is a separate heap object per
// observation, and the store holds hundreds of thousands of them. Signal
// values used to be *float64 / *int (three extra objects per observation);
// they are held inline now. A new optional field should follow setSignal's
// pattern instead of adding a pointer.
func TestStoreObsHasNoPointerFields(t *testing.T) {
	typ := reflect.TypeOf(StoreObs{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Type.Kind() == reflect.Pointer {
			t.Errorf("StoreObs.%s is %s: a heap object per observation", f.Name, f.Type)
		}
	}
}

// withSignal sets an observation's signal values for a test; nil leaves a
// value unset, the way a NULL column does.
func withSignal(o *StoreObs, snr, rssi *float64) *StoreObs {
	o.setSignalFrom(snr, rssi, nil)
	return o
}

func TestStoreObsSignalFromColumns(t *testing.T) {
	o := &StoreObs{}
	o.setSignal(sql.NullFloat64{Float64: -7.25, Valid: true}, sql.NullFloat64{}, sql.NullInt64{Int64: 3, Valid: true})
	if p := o.SNRPtr(); p == nil || *p != -7.25 {
		t.Errorf("SNRPtr = %v, want -7.25", p)
	}
	if p := o.RSSIPtr(); p != nil {
		t.Errorf("RSSIPtr = %v for a NULL column, want nil", *p)
	}
	if got := o.scoreOrNil(); got != 3 {
		t.Errorf("scoreOrNil = %v, want 3", got)
	}

	// A second set replaces everything, including clearing what is now NULL.
	o.setSignal(sql.NullFloat64{}, sql.NullFloat64{Float64: -101, Valid: true}, sql.NullInt64{})
	if o.SNRPtr() != nil {
		t.Error("SNR still set after a NULL column")
	}
	if p := o.RSSIPtr(); p == nil || *p != -101 {
		t.Errorf("RSSIPtr = %v, want -101", p)
	}
	if got := o.scoreOrNil(); got != nil {
		t.Errorf("scoreOrNil = %v, want nil", got)
	}
}

func TestStoreObsSignalFromPointers(t *testing.T) {
	snr, score := 0.0, 0
	o := &StoreObs{}
	o.setSignalFrom(&snr, nil, &score)
	// Zero is a real reading, distinct from unset.
	if p := o.SNRPtr(); p == nil || *p != 0 {
		t.Errorf("SNRPtr = %v, want 0", p)
	}
	if o.RSSIPtr() != nil {
		t.Error("RSSIPtr set from a nil pointer")
	}
	if got := o.scoreOrNil(); got != 0 {
		t.Errorf("scoreOrNil = %v, want 0", got)
	}
	// The stored value is a copy, not the caller's variable.
	snr = 9
	if *o.SNRPtr() != 0 {
		t.Error("SNR follows the caller's variable")
	}
}
