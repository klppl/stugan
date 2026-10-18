package irc

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/klippelism/stugan/internal/core"
)

// TestLiveSASL logs in to a real network with SASL, to check against servers
// the fake ones can't stand in for (Libera offers SCRAM-SHA-512, for one). It
// is skipped unless STUGAN_LIVE_SASL_USER and STUGAN_LIVE_SASL_PASS are set,
// so it never runs in CI:
//
//	STUGAN_LIVE_SASL_USER=account STUGAN_LIVE_SASL_PASS=secret \
//	  go test -run TestLiveSASL -v ./internal/irc
//
// STUGAN_LIVE_SASL_ADDR picks the server (default irc.libera.chat:6697, TLS).
// It tries SCRAM first and then PLAIN, and reports which mechanisms the
// server offered.
func TestLiveSASL(t *testing.T) {
	user, pass := os.Getenv("STUGAN_LIVE_SASL_USER"), os.Getenv("STUGAN_LIVE_SASL_PASS")
	if user == "" || pass == "" {
		t.Skip("set STUGAN_LIVE_SASL_USER and STUGAN_LIVE_SASL_PASS to run")
	}
	addr := os.Getenv("STUGAN_LIVE_SASL_ADDR")
	if addr == "" {
		addr = "irc.libera.chat:6697"
	}
	for _, scram := range []bool{true, false} {
		name := "plain"
		if scram {
			name = "scram"
		}
		t.Run(name, func(t *testing.T) {
			h := &lockedHandler{}
			c, err := New(Options{
				Network: "live", Addr: addr, TLS: true,
				Nick: "stgtest" + strings.ReplaceAll(time.Now().Format("0405.000"), ".", ""),
				User: "stugan", Realname: "stugan live test",
				SASLUser: user, SASLPass: pass, SASLScram: scram,
			}, h)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			errc := make(chan error, 1)
			go func() { errc <- c.Connect(ctx) }()

			deadline := time.After(25 * time.Second)
			for {
				select {
				case err := <-errc:
					t.Fatalf("did not register: %v", err)
				case <-deadline:
					t.Fatal("timed out before registering")
				case <-time.After(100 * time.Millisecond):
				}
				h.mu.Lock()
				registered := false
				for _, ev := range h.evs {
					registered = registered || ev.Type == core.EvConnect
				}
				h.mu.Unlock()
				if registered {
					if m, ok := c.mech.(*saslScram); ok {
						t.Logf("registered with %s", m.variant().name)
					} else {
						t.Log("registered with PLAIN")
					}
					c.client.Close()
					return
				}
			}
		})
	}
}
