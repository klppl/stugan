package ircserver

import (
	"testing"
)

func TestParseLine(t *testing.T) {
	tests := []struct {
		raw         string
		cmd         string
		params      []string
		wantRawTags string
	}{
		{
			raw:    "PING :123456\r\n",
			cmd:    "PING",
			params: []string{"123456"},
		},
		{
			raw:    ":user!ident@host PRIVMSG #chan :hello world\r\n",
			cmd:    "PRIVMSG",
			params: []string{"#chan", "hello world"},
		},
		{
			raw:         "@+typing=active;servertime=123 PRIVMSG #chan :hi",
			cmd:         "PRIVMSG",
			params:      []string{"#chan", "hi"},
			wantRawTags: "+typing=active",
		},
		{
			raw:    "JOIN #chan,#chan2 key1,key2\r\n",
			cmd:    "JOIN",
			params: []string{"#chan,#chan2", "key1,key2"},
		},
		{
			raw:    "PASS user/libera:secret123\r\n",
			cmd:    "PASS",
			params: []string{"user/libera:secret123"},
		},
		{
			raw:    "CAP REQ :server-time message-tags echo-message\r\n",
			cmd:    "CAP",
			params: []string{"REQ", "server-time message-tags echo-message"},
		},
	}

	for _, tt := range tests {
		msg := ParseLine(tt.raw)
		if msg == nil {
			t.Fatalf("ParseLine(%q) returned nil", tt.raw)
		}
		if msg.Command != tt.cmd {
			t.Errorf("ParseLine(%q).Command = %q; want %q", tt.raw, msg.Command, tt.cmd)
		}
		if len(msg.Params) != len(tt.params) {
			t.Fatalf("ParseLine(%q).Params len = %d; want %d (%v vs %v)", tt.raw, len(msg.Params), len(tt.params), msg.Params, tt.params)
		}
		for i := range tt.params {
			if msg.Params[i] != tt.params[i] {
				t.Errorf("ParseLine(%q).Params[%d] = %q; want %q", tt.raw, i, msg.Params[i], tt.params[i])
			}
		}
		if msg.RawTags != tt.wantRawTags {
			t.Errorf("ParseLine(%q).RawTags = %q; want %q", tt.raw, msg.RawTags, tt.wantRawTags)
		}
	}
}

func TestFormatLine(t *testing.T) {
	line := FormatLine("", "stugan.bouncer", "001", "alice", ":Welcome to the stugan bouncer, alice")
	want := ":stugan.bouncer 001 alice :Welcome to the stugan bouncer, alice"
	if line != want {
		t.Errorf("FormatLine() = %q; want %q", line, want)
	}

	tagged := FormatLine("time=2026-08-16T12:00:00.000Z", "alice!stugan@bouncer", "PRIVMSG", "#chan", "hello world")
	wantTagged := "@time=2026-08-16T12:00:00.000Z :alice!stugan@bouncer PRIVMSG #chan :hello world"
	if tagged != wantTagged {
		t.Errorf("FormatLine() = %q; want %q", tagged, wantTagged)
	}
}

func TestTagEscaping(t *testing.T) {
	original := `hello; world\test\r\n`
	escaped := EscapeTagValue(original)
	unescaped := UnescapeTagValue(escaped)
	if unescaped != original {
		t.Errorf("Unescape(Escape(%q)) = %q; want %q", original, unescaped, original)
	}
}

func TestUnmarshalLogin(t *testing.T) {
	tests := []struct {
		raw     string
		wantU   string
		wantNet string
		wantCl  string
	}{
		{"alice", "alice", "", ""},
		{"alice/libera", "alice", "libera", ""},
		{"alice/libera@desktop", "alice", "libera", "desktop"},
		{"alice@desktop/libera", "alice", "libera", "desktop"},
		{"alice@desktop", "alice", "", "desktop"},
	}

	for _, tt := range tests {
		res := UnmarshalLogin(tt.raw)
		if res.Username != tt.wantU || res.Network != tt.wantNet || res.Client != tt.wantCl {
			t.Errorf("UnmarshalLogin(%q) = %+v; want u=%q net=%q cl=%q", tt.raw, res, tt.wantU, tt.wantNet, tt.wantCl)
		}
	}
}

func TestParseCredentials(t *testing.T) {
	creds, ok := ParseCredentials("alice/libera:mysecret", "")
	if !ok || creds.Username != "alice" || creds.Network != "libera" || creds.Secret != "mysecret" {
		t.Errorf("ParseCredentials(ZNC shape) = %+v, %v", creds, ok)
	}

	creds2, ok := ParseCredentials("mysecret", "alice/oftc")
	if !ok || creds2.Username != "alice" || creds2.Network != "oftc" || creds2.Secret != "mysecret" {
		t.Errorf("ParseCredentials(USER shape) = %+v, %v", creds2, ok)
	}
}
