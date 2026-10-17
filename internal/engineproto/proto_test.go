package engineproto

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	in := &Frame{
		Op: OpAttached, Handle: "h", Replay: []string{":s 001 me :hi"}, Nick: "me",
		Channels: []string{"#a"}, Unattended: true, DetachedMs: 42, Backlog: 3, Lost: 1,
	}
	b, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	if b[len(b)-1] != '\n' || bytes.Count(b, []byte("\n")) != 1 {
		t.Fatalf("frame must be exactly one line: %q", b)
	}
	r := NewReader(bytes.NewReader(append([]byte("\n  \n"), b...)))
	out, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if out.Op != in.Op || out.Nick != "me" || out.Replay[0] != in.Replay[0] || !out.Unattended || out.Lost != 1 {
		t.Fatalf("round trip: %+v", out)
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestLineWithNewlinesStaysOneFrame(t *testing.T) {
	b, err := Encode(&Frame{Op: OpLine, Line: "a\r\nb"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(b, []byte("\n")) != 1 {
		t.Fatalf("embedded newline leaked into the framing: %q", b)
	}
}

func TestReaderRejects(t *testing.T) {
	cases := map[string]string{
		"malformed": "{not json}\n",
		"no op":     `{"id":"x"}` + "\n",
		"too long":  `{"op":"line","line":"` + strings.Repeat("x", MaxFrameBytes) + `"}` + "\n",
	}
	for name, in := range cases {
		if _, err := NewReader(strings.NewReader(in)).Next(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := NewReader(strings.NewReader(cases["too long"])).Next(); !errors.Is(err, ErrFrameTooLong) {
		t.Errorf("too long: %v", err)
	}
}

func TestEncodeRejectsOversize(t *testing.T) {
	if _, err := Encode(&Frame{Op: OpLine, Line: strings.Repeat("x", MaxFrameBytes)}); err == nil {
		t.Fatal("oversize frame encoded")
	}
}

func TestParseAddr(t *testing.T) {
	ok := map[string][2]string{
		"127.0.0.1:8016":           {"tcp", "127.0.0.1:8016"},
		"tcp://stugan-engine:8016": {"tcp", "stugan-engine:8016"},
		"tcp://[::1]:8016":         {"tcp", "[::1]:8016"},
		"unix:///run/stugan.sock":  {"unix", "/run/stugan.sock"},
		"unix:/run/stugan.sock":    {"unix", "/run/stugan.sock"},
		"  host:1  ":               {"tcp", "host:1"},
	}
	for in, want := range ok {
		n, a, err := ParseAddr(in)
		if err != nil || n != want[0] || a != want[1] {
			t.Errorf("ParseAddr(%q) = %q %q %v, want %v", in, n, a, err, want)
		}
	}
	for _, bad := range []string{"", "host", "host:", "host:abc", "host:70000", "host:0", "http://x:1", "tcp://x:1/path", "unix://", "tcp://x"} {
		if _, _, err := ParseAddr(bad); err == nil {
			t.Errorf("ParseAddr(%q) accepted", bad)
		}
	}
}
