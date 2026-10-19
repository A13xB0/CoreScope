package main

import (
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
