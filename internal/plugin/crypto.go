package plugin

import (
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"math/big"

	lua "github.com/yuin/gopher-lua"
	"golang.org/x/crypto/blowfish"
)

// stugan.crypto exposes a few cryptographic primitives to Lua. The goal is
// to give plugin authors enough to implement IRC encryption schemes (FiSH
// Blowfish ECB/CBC and the DH1080 key exchange they use) without having to
// rebuild crypto in pure Lua, where modexp of 1080-bit ints is impractical
// in float64 arithmetic and there is no secure RNG. These bindings are
// primitives only: padding, framing, ciphertext layout, and key derivation
// are the plugin's job — Go just provides the algorithms.
//
// All byte arguments and return values are encoded as Lua strings; gopher-lua
// strings are 8-bit clean so arbitrary bytes round-trip cleanly.

// maxRandomBytes caps stugan.crypto.random to a sensible size so a runaway
// script can't drain entropy or memory in a loop.
const maxRandomBytes = 4096

// buildCrypto constructs the per-script `stugan.crypto` table. It captures
// no script state today — the closures only use lua.LState arguments — but
// matches the buildKV/buildLog shape so adding script-scoped helpers later
// is straightforward.
func (h *Host) buildCrypto(s *script) *lua.LTable {
	t := s.L.NewTable()

	t.RawSetString("blowfish_ecb_encrypt", s.L.NewFunction(luaBlowfishECB(true)))
	t.RawSetString("blowfish_ecb_decrypt", s.L.NewFunction(luaBlowfishECB(false)))
	t.RawSetString("blowfish_cbc_encrypt", s.L.NewFunction(luaBlowfishCBC(true)))
	t.RawSetString("blowfish_cbc_decrypt", s.L.NewFunction(luaBlowfishCBC(false)))

	t.RawSetString("sha256", s.L.NewFunction(func(L *lua.LState) int {
		sum := sha256.Sum256([]byte(L.CheckString(1)))
		L.Push(lua.LString(sum[:]))
		return 1
	}))

	t.RawSetString("random", s.L.NewFunction(func(L *lua.LState) int {
		n := L.CheckInt(1)
		if n < 1 || n > maxRandomBytes {
			L.RaiseError("stugan.crypto.random: n must be 1..%d (got %d)", maxRandomBytes, n)
		}
		buf := make([]byte, n)
		if _, err := rand.Read(buf); err != nil {
			L.RaiseError("stugan.crypto.random: %v", err)
		}
		L.Push(lua.LString(buf))
		return 1
	}))

	// modexp(base, exp, mod) -> big-endian bytes, zero-padded to len(mod).
	// Inputs are arbitrary-width big-endian byte strings. The fixed-width
	// output matches what DH-style protocols expect on the wire, so the
	// caller doesn't have to re-pad leading zeros itself.
	t.RawSetString("modexp", s.L.NewFunction(func(L *lua.LState) int {
		base := new(big.Int).SetBytes([]byte(L.CheckString(1)))
		exp := new(big.Int).SetBytes([]byte(L.CheckString(2)))
		modBytes := []byte(L.CheckString(3))
		if len(modBytes) == 0 {
			L.RaiseError("stugan.crypto.modexp: modulus is empty")
		}
		mod := new(big.Int).SetBytes(modBytes)
		if mod.Sign() == 0 {
			L.RaiseError("stugan.crypto.modexp: modulus is zero")
		}
		r := new(big.Int).Exp(base, exp, mod)
		out := make([]byte, len(modBytes))
		rb := r.Bytes()
		copy(out[len(out)-len(rb):], rb)
		L.Push(lua.LString(out))
		return 1
	}))

	return t
}

// luaBlowfishECB returns a Lua function that ECB-encrypts (enc=true) or
// -decrypts (enc=false) a buffer. The buffer length must be a multiple of
// the 8-byte block size; padding is the caller's responsibility.
func luaBlowfishECB(enc bool) lua.LGFunction {
	return func(L *lua.LState) int {
		key, data := checkBlowfishArgs(L, 1, 2)
		c, err := blowfish.NewCipher(key)
		if err != nil {
			L.RaiseError("blowfish: %v", err)
		}
		out := make([]byte, len(data))
		for i := 0; i < len(data); i += blowfish.BlockSize {
			if enc {
				c.Encrypt(out[i:i+blowfish.BlockSize], data[i:i+blowfish.BlockSize])
			} else {
				c.Decrypt(out[i:i+blowfish.BlockSize], data[i:i+blowfish.BlockSize])
			}
		}
		L.Push(lua.LString(out))
		return 1
	}
}

// luaBlowfishCBC returns a Lua function for CBC mode with a caller-supplied
// 8-byte IV. Like the ECB variant, padding is up to the caller — Blowfish
// has no canonical padding scheme and FiSH variants disagree on the choice,
// so we keep this binding strictly raw.
func luaBlowfishCBC(enc bool) lua.LGFunction {
	return func(L *lua.LState) int {
		key := []byte(L.CheckString(1))
		iv := []byte(L.CheckString(2))
		data := []byte(L.CheckString(3))
		checkBlowfishKey(L, key)
		if len(iv) != blowfish.BlockSize {
			L.RaiseError("blowfish: IV must be %d bytes (got %d)", blowfish.BlockSize, len(iv))
		}
		if len(data)%blowfish.BlockSize != 0 {
			L.RaiseError("blowfish: data length must be a multiple of %d (got %d)", blowfish.BlockSize, len(data))
		}
		c, err := blowfish.NewCipher(key)
		if err != nil {
			L.RaiseError("blowfish: %v", err)
		}
		out := make([]byte, len(data))
		var mode cipher.BlockMode
		if enc {
			mode = cipher.NewCBCEncrypter(c, iv)
		} else {
			mode = cipher.NewCBCDecrypter(c, iv)
		}
		mode.CryptBlocks(out, data)
		L.Push(lua.LString(out))
		return 1
	}
}

func checkBlowfishArgs(L *lua.LState, keyIdx, dataIdx int) (key, data []byte) {
	key = []byte(L.CheckString(keyIdx))
	data = []byte(L.CheckString(dataIdx))
	checkBlowfishKey(L, key)
	if len(data)%blowfish.BlockSize != 0 {
		L.RaiseError("blowfish: data length must be a multiple of %d (got %d)", blowfish.BlockSize, len(data))
	}
	return key, data
}

func checkBlowfishKey(L *lua.LState, key []byte) {
	if len(key) < 1 || len(key) > 56 {
		L.RaiseError("blowfish: key must be 1..56 bytes (got %d)", len(key))
	}
}
