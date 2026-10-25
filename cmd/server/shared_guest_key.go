package main

import "net/http"

// The shared guest key: one fixed, public identity that observers poll
// repeaters under, so any observer hearing a reply can feed CoreScope the
// reading. Its private half is public by design — anyone polling with the key
// must hold it — so the whole keypair is committed here in the clear, the same
// value the Neighbour Battery plugin compiles in. The Phase 2 decoder uses
// these bytes to decrypt replies addressed to the key; serving them lets the
// plugin fetch the key instead of pasting it.
const (
	sharedGuestKeyName    = "CoreScope shared guest"
	sharedGuestKeyVersion = 1
	sharedGuestKeyPublic  = "f0fbaae3284350f9ae353e617d7fb1ae143341dd1b540477c74be758066d7dd2"
	sharedGuestKeyPrivate = "8dbaa94fbcff9dddc124533648f27adb42ddd30c264ab0e4a3f4a24505f533d4"
)

// handleSharedGuestKey serves the shared guest key so a client can poll under
// it without pasting the key. Everything returned is public.
func (s *Server) handleSharedGuestKey(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]interface{}{
		"name":        sharedGuestKeyName,
		"version":     sharedGuestKeyVersion,
		"public_key":  sharedGuestKeyPublic,
		"private_key": sharedGuestKeyPrivate,
	})
}
