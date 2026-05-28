package plugin

import (
	"context"
	"testing"
)

// runScript loads a one-off Lua script that runs `body` (a function body
// ending in `return <string>`) and stuffs the result into msg.text. It is
// the smallest way to exercise the bindings end-to-end through gopher-lua's
// argument marshalling. The body is wrapped in an immediately-invoked
// function so it can declare locals.
func runScript(t *testing.T, body string) string {
	t.Helper()
	api := &fakeAPI{}
	h := newHost(t, api, map[string]string{
		"t.lua": `stugan.hook_message(function(msg)
		  msg.text = (function()
		    ` + body + `
		  end)()
		  return msg
		end)`,
	}, nil)
	out, keep := h.Dispatch(context.Background(), inMsg("trigger"))
	if !keep {
		t.Fatal("script dropped the trigger message")
	}
	return out.Message.Text
}

func TestCryptoBlowfishECBRoundTrip(t *testing.T) {
	// Round-trip an 8-byte plaintext (one block) through ECB.
	got := runScript(t, `
		local k  = "topsecret"
		local pt = "12345678"
		local ct = stugan.crypto.blowfish_ecb_encrypt(k, pt)
		local rt = stugan.crypto.blowfish_ecb_decrypt(k, ct)
		return rt
	`)
	if got != "12345678" {
		t.Errorf("ECB round-trip: got %q, want %q", got, "12345678")
	}
}

func TestCryptoBlowfishECBKnownVector(t *testing.T) {
	// Schneier's published Blowfish test vector: key=0x0000000000000000,
	// plaintext=0x0000000000000000, ciphertext=0x4EF997456198DD78.
	// Confirms we're driving the upstream cipher exactly as documented (no
	// endian shuffles snuck in along the way).
	got := runScript(t, `
		local k  = string.rep("\0", 8)
		local pt = string.rep("\0", 8)
		local ct = stugan.crypto.blowfish_ecb_encrypt(k, pt)
		local hex = ""
		for i = 1, #ct do
		  hex = hex .. string.format("%02x", ct:byte(i))
		end
		return hex
	`)
	if got != "4ef997456198dd78" {
		t.Errorf("ECB known vector: got %s, want 4ef997456198dd78", got)
	}
}

func TestCryptoBlowfishCBCRoundTrip(t *testing.T) {
	// Round-trip a two-block plaintext through CBC with a random IV. Confirms
	// both that the IV path works and that we wired Encrypt/Decrypt modes the
	// right way around.
	got := runScript(t, `
		local k   = "hunter2"
		local iv  = stugan.crypto.random(8)
		local pt  = "abcdefghABCDEFGH"
		local ct  = stugan.crypto.blowfish_cbc_encrypt(k, iv, pt)
		return stugan.crypto.blowfish_cbc_decrypt(k, iv, ct)
	`)
	if got != "abcdefghABCDEFGH" {
		t.Errorf("CBC round-trip: got %q, want %q", got, "abcdefghABCDEFGH")
	}
}

func TestCryptoBlowfishRejectsBadLengths(t *testing.T) {
	// A non-block-aligned plaintext must raise, not silently pad. The errored
	// hook is skipped and the original message passes through untouched, which
	// is how we detect the raise.
	api := &fakeAPI{}
	h := newHost(t, api, map[string]string{
		"t.lua": `stugan.hook_message(function(msg)
		  msg.text = stugan.crypto.blowfish_ecb_encrypt("k", "seven!!") -- 7 bytes
		  return msg
		end)`,
	}, nil)
	out, _ := h.Dispatch(context.Background(), inMsg("trigger"))
	if out.Message.Text != "trigger" {
		t.Errorf("expected raise + passthrough; got text=%q", out.Message.Text)
	}
}

func TestCryptoSHA256(t *testing.T) {
	// FIPS 180-2 vector: sha256("abc").
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	got := runScript(t, `
		local h = stugan.crypto.sha256("abc")
		local hex = ""
		for i = 1, #h do
		  hex = hex .. string.format("%02x", h:byte(i))
		end
		return hex
	`)
	if got != want {
		t.Errorf("sha256(abc) = %s, want %s", got, want)
	}
}

func TestCryptoRandomLength(t *testing.T) {
	// We only assert the length; testing entropy is out of scope. A second
	// draw is included to catch the trivial bug of returning the same buffer.
	got := runScript(t, `
		local a = stugan.crypto.random(16)
		local b = stugan.crypto.random(16)
		return #a .. "," .. #b .. "," .. (a == b and "same" or "diff")
	`)
	if got != "16,16,diff" {
		t.Errorf("random length/distinctness: got %s", got)
	}
}

func TestCryptoModexp(t *testing.T) {
	// 2^10 mod 1000 = 1024 mod 1000 = 24. The encoded result must be
	// left-padded to the modulus length (3 bytes) so DH-style protocols can
	// rely on a fixed wire width.
	got := runScript(t, `
		local function bytes(s)
		  local out = {}
		  for i = 1, #s do out[i] = string.format("%02x", s:byte(i)) end
		  return table.concat(out)
		end
		local base = "\0\0\2"        -- 2
		local exp  = "\0\0\10"       -- 10
		local mod  = "\3\232\0"      -- 0x03E800 = 256000
		-- 2^10 mod 256000 = 1024 = 0x000400. Should be 3 bytes wide.
		return bytes(stugan.crypto.modexp(base, exp, mod))
	`)
	if got != "000400" {
		t.Errorf("modexp = %s, want 000400", got)
	}
}

func TestCryptoModexpLargeWidth(t *testing.T) {
	// A modulus much larger than the actual result must still produce a
	// fixed-width output. We just check the byte length and trailing value.
	got := runScript(t, `
		local mod = string.rep(string.char(255), 128)   -- 1024-bit modulus
		local r   = stugan.crypto.modexp("\2", "\3", mod)
		return tostring(#r) .. ":" .. string.format("%02x", r:byte(#r))
	`)
	if got != "128:08" {
		t.Errorf("modexp width: got %s, want 128:08 (2^3 = 8)", got)
	}
}

// Sanity: confirm raw bytes survive the Lua boundary. CBC ciphertext is the
// only place arbitrary 0x00-bearing buffers cross Go↔Lua↔Go in fish.lua, so
// we explicitly assert that nothing truncates at the first null byte.
func TestCryptoBytesAreEightBitClean(t *testing.T) {
	got := runScript(t, `
		local s = "\0\1\2\3\4\5\6\7"
		local enc = stugan.crypto.blowfish_ecb_encrypt("k", s)
		return tostring(#enc)
	`)
	if got != "8" {
		t.Errorf("encrypted-buffer length round-trip: got %s, want 8", got)
	}
	// And the inverse: decrypt-then-length, to catch a Lua-side truncation.
	got2 := runScript(t, `
		local s   = "\0\1\2\3\4\5\6\7"
		local enc = stugan.crypto.blowfish_ecb_encrypt("k", s)
		local dec = stugan.crypto.blowfish_ecb_decrypt("k", enc)
		return tostring(#dec) .. ":" .. string.format("%02x%02x", dec:byte(1), dec:byte(8))
	`)
	if got2 != "8:0007" {
		t.Errorf("ECB round-trip preserves NULs: got %s, want 8:0007", got2)
	}
}
