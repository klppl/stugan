package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/klippelism/stugan/internal/engine"
)

func TestEnvBytes(t *testing.T) {
	t.Setenv("X_BYTES", "")
	if n, err := envBytes("X_BYTES"); n != 0 || err != nil {
		t.Fatalf("unset: %d %v", n, err)
	}
	t.Setenv("X_BYTES", "1048576")
	if n, err := envBytes("X_BYTES"); n != 1<<20 || err != nil {
		t.Fatalf("valid: %d %v", n, err)
	}
	for _, bad := range []string{"1k", "-5", "100", "lots"} {
		t.Setenv("X_BYTES", bad)
		if _, err := envBytes("X_BYTES"); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestEnvOrphanTimeout(t *testing.T) {
	for in, want := range map[string]time.Duration{"": 0, "0": -1, "30m": 30 * time.Minute} {
		t.Setenv("STUGAN_ENGINE_ORPHAN_TIMEOUT", in)
		if got, err := envOrphanTimeout(); err != nil || got != want {
			t.Errorf("%q: %v %v, want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"-1h", "soon", "5"} {
		t.Setenv("STUGAN_ENGINE_ORPHAN_TIMEOUT", bad)
		if _, err := envOrphanTimeout(); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestProbe(t *testing.T) {
	srv, err := engine.New(engine.Options{Secret: "0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ctx, ln) }()
	defer func() { cancel(); <-done }()

	if err := probe(ln.Addr().String()); err != nil {
		t.Fatalf("probe of a live engine: %v", err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	if err := probe("0.0.0.0:" + port); err != nil {
		t.Fatalf("wildcard bind is probed over loopback: %v", err)
	}
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := dead.Addr().String()
	_ = dead.Close()
	if err := probe(addr); err == nil {
		t.Fatal("probe of a dead address succeeded")
	}
}
