package main

import (
	"crypto/aes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"math/big"
)

// Shared-guest-key crypto: the ECDH half MeshCore uses for anything addressed
// to a specific key (as opposed to a hashtag channel, which is symmetric and
// handled in decoder.go). A reply under the shared guest key is encrypted to
// the shared key, so CoreScope reproduces the same secret the repeater used —
// ECDH between the shared key's private scalar and the repeater's public key —
// then AES-128-ECB with a 2-byte HMAC-SHA256 tag, exactly as decoder.go does
// for channels.
//
// Standard library only: X25519 is the RFC 7748 Montgomery ladder over
// 2^255-19 with math/big, and the Ed25519 public key is mapped to its
// Montgomery u-coordinate by the birational map u = (1+y)/(1-y). The scalar is
// public by design here (the shared key is public), so constant-time is not a
// concern. Cross-checked against openhop_core in the test.

var x25519P, _ = new(big.Int).SetString(
	"57896044618658097711785492504343953926634992332820282019728792003956564819949", 10) // 2^255 - 19
var x25519A24 = big.NewInt(121665)

func leToInt(b []byte) *big.Int {
	r := make([]byte, len(b))
	for i := range b {
		r[i] = b[len(b)-1-i]
	}
	return new(big.Int).SetBytes(r)
}

func intToLE32(n *big.Int) []byte {
	b := n.Bytes() // big-endian
	out := make([]byte, 32)
	for i := 0; i < len(b) && i < 32; i++ {
		out[i] = b[len(b)-1-i]
	}
	return out
}

// x25519 multiplies the clamped scalar by the Montgomery u-coordinate.
func x25519(scalar, uBytes []byte) []byte {
	k := leToInt(scalar)
	u := new(big.Int).Mod(leToInt(uBytes), x25519P)

	mod := func(x *big.Int) *big.Int { return x.Mod(x, x25519P) }
	x1 := new(big.Int).Set(u)
	x2 := big.NewInt(1)
	z2 := big.NewInt(0)
	x3 := new(big.Int).Set(u)
	z3 := big.NewInt(1)
	swap := uint(0)

	for t := 254; t >= 0; t-- {
		kt := k.Bit(t)
		swap ^= kt
		if swap == 1 {
			x2, x3 = x3, x2
			z2, z3 = z3, z2
		}
		swap = kt

		A := mod(new(big.Int).Add(x2, z2))
		AA := mod(new(big.Int).Mul(A, A))
		B := mod(new(big.Int).Sub(x2, z2))
		BB := mod(new(big.Int).Mul(B, B))
		E := mod(new(big.Int).Sub(AA, BB))
		C := mod(new(big.Int).Add(x3, z3))
		D := mod(new(big.Int).Sub(x3, z3))
		DA := mod(new(big.Int).Mul(D, A))
		CB := mod(new(big.Int).Mul(C, B))
		sum := mod(new(big.Int).Add(DA, CB))
		diff := mod(new(big.Int).Sub(DA, CB))
		x3 = mod(new(big.Int).Mul(sum, sum))
		z3 = mod(new(big.Int).Mul(x1, mod(new(big.Int).Mul(diff, diff))))
		x2 = mod(new(big.Int).Mul(AA, BB))
		t2 := mod(new(big.Int).Add(AA, mod(new(big.Int).Mul(x25519A24, E))))
		z2 = mod(new(big.Int).Mul(E, t2))
	}
	if swap == 1 {
		x2, x3 = x3, x2
		z2, z3 = z3, z2
	}
	zinv := new(big.Int).Exp(z2, new(big.Int).Sub(x25519P, big.NewInt(2)), x25519P)
	return intToLE32(new(big.Int).Mod(new(big.Int).Mul(x2, zinv), x25519P))
}

// ed25519PubToX25519 maps an Ed25519 public key to its Montgomery u-coordinate.
func ed25519PubToX25519(edPub []byte) []byte {
	yb := make([]byte, 32)
	copy(yb, edPub)
	yb[31] &= 0x7f // drop the sign bit
	y := leToInt(yb)
	one := big.NewInt(1)
	num := new(big.Int).Mod(new(big.Int).Add(one, y), x25519P)
	den := new(big.Int).Mod(new(big.Int).Sub(one, y), x25519P)
	denInv := new(big.Int).Exp(den, new(big.Int).Sub(x25519P, big.NewInt(2)), x25519P)
	return intToLE32(new(big.Int).Mod(new(big.Int).Mul(num, denInv), x25519P))
}

// sharedGuestSecret is the 32-byte ECDH secret between the shared key (a
// 32-byte Ed25519 seed) and a repeater's Ed25519 public key. Matches
// libsodium's crypto_sign_ed25519_sk_to_curve25519: the X25519 scalar is the
// clamped first half of SHA-512(seed).
func sharedGuestSecret(seed, repeaterEdPub []byte) []byte {
	h := sha512.Sum512(seed)
	scalar := make([]byte, 32)
	copy(scalar, h[:32])
	scalar[0] &= 248
	scalar[31] &= 127
	scalar[31] |= 64
	return x25519(scalar, ed25519PubToX25519(repeaterEdPub))
}

// sharedGuestDecrypt verifies the 2-byte MAC and AES-128-ECB-decrypts an
// encrypted payload (MAC(2) + ciphertext), returning the plaintext and whether
// it was authentic. AES key is the first 16 bytes of the secret; the HMAC key
// is the full 32, as in the firmware.
func sharedGuestDecrypt(secret, data []byte) ([]byte, bool) {
	if len(secret) != 32 || len(data) <= 2 {
		return nil, false
	}
	mac, ct := data[:2], data[2:]
	h := hmac.New(sha256.New, secret)
	h.Write(ct)
	sum := h.Sum(nil)
	if sum[0] != mac[0] || sum[1] != mac[1] {
		return nil, false
	}
	padded := ct
	if r := len(ct) % aes.BlockSize; r != 0 {
		padded = append(append([]byte{}, ct...), make([]byte, aes.BlockSize-r)...)
	}
	block, err := aes.NewCipher(secret[:16])
	if err != nil {
		return nil, false
	}
	out := make([]byte, len(padded))
	for i := 0; i < len(padded); i += aes.BlockSize {
		block.Decrypt(out[i:i+aes.BlockSize], padded[i:i+aes.BlockSize])
	}
	return out, true
}
