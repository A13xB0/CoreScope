package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSharedGuestKeyEndpoint(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/shared-guest/key", nil)
	w := httptest.NewRecorder()
	s.handleSharedGuestKey(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["public_key"] != sharedGuestKeyPublic || body["private_key"] != sharedGuestKeyPrivate {
		t.Errorf("key mismatch: %v", body)
	}
	if body["name"] != sharedGuestKeyName {
		t.Errorf("name = %v", body["name"])
	}
}
