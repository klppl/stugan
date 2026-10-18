package irc

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klippelism/stugan/internal/core"
)

// lockedHandler records events from girc's goroutines for a polling test.
type lockedHandler struct {
	mu  sync.Mutex
	evs []core.Event
}

func (h *lockedHandler) HandleEvent(ev core.Event) {
	h.mu.Lock()
	h.evs = append(h.evs, ev)
	h.mu.Unlock()
}

// scramServerConn is a minimal IRC server for one connection: it advertises
// SASL mechanisms, runs the server side of SCRAM, and registers the client on
// success. It exists to drive stugan's real connection code (girc's CAP and
// AUTHENTICATE handling included) through a SCRAM login.
type scramServerConn struct {
	t         *testing.T
	advertise string // value of the sasl cap
	pass      string
	chose     chan string // SASL mechanism the client asked for
	// capNew makes the server announce "CAP NEW sasl" before the welcome, the
	// way soju does for a connection bound to a network.
	capNew bool
	// late counts AUTHENTICATE / CAP REQ lines the client sent after SASL
	// succeeded: a client that re-runs SASL mid-session gets dropped.
	late atomic.Int32
	done atomic.Bool
}

func (s *scramServerConn) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	w := func(f string, a ...any) { c.Write([]byte(fmt.Sprintf(f, a...) + "\r\n")) }
	var (
		v      scramHash
		bare   string
		sfirst string
		salted []byte
		salt   = []byte("serversalt-1234")
		iter   = 4096
		step   int
		chosen bool
		plain  bool
		nick   = "*"
		mac    = func(key []byte, msg string) []byte {
			h := hmac.New(v.New, key)
			h.Write([]byte(msg))
			return h.Sum(nil)
		}
		deadline = time.Now().Add(10 * time.Second)
	)
	for time.Now().Before(deadline) {
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) == 0 {
			continue
		}
		if s.done.Load() && (f[0] == "AUTHENTICATE" || (f[0] == "CAP" && len(f) > 1 && f[1] == "REQ")) {
			s.late.Add(1)
			w(":srv 904 %s :Invalid base64-encoded response", nick)
			continue
		}
		switch f[0] {
		case "NICK":
			nick = f[1]
		case "CAP":
			switch f[1] {
			case "LS":
				w(":srv CAP * LS :sasl=%s message-tags", s.advertise)
			case "REQ":
				w(":srv CAP * ACK %s", strings.TrimPrefix(strings.Join(f[2:], " "), ""))
			case "END":
				if s.capNew {
					for i := 0; i < 3; i++ {
						w(":srv CAP * NEW :sasl=PLAIN,SCRAM-SHA-256")
					}
				}
				w(":srv 001 %s :Welcome", nick)
			}
		case "AUTHENTICATE":
			arg := f[1]
			if !chosen {
				chosen = true
				s.chose <- arg
				if arg == "PLAIN" {
					plain = true
					w("AUTHENTICATE +")
					continue
				}
				for _, h := range scramHashes {
					if h.name == arg {
						v = h
					}
				}
				if v.New == nil {
					w(":srv 908 %s PLAIN,EXTERNAL :are available SASL mechanisms", nick)
					w(":srv 904 %s :SASL authentication failed", nick)
					return
				}
				w("AUTHENTICATE +")
				continue
			}
			raw, _ := base64.StdEncoding.DecodeString(arg)
			msg := string(raw)
			if plain {
				w(":srv 900 %s %s!u@h acct :You are now logged in", nick, nick)
				w(":srv 903 %s :SASL authentication successful", nick)
				s.done.Store(true)
				continue
			}
			switch step {
			case 0: // client-first
				bare = strings.TrimPrefix(msg, "n,,")
				snonce := scramAttrs(bare)["r"] + "S3rv3r"
				sfirst = "r=" + snonce + ",s=" + base64.StdEncoding.EncodeToString(salt) + ",i=" + strconv.Itoa(iter)
				salted, _ = pbkdf2.Key(v.New, s.pass, salt, iter, v.New().Size())
				w("AUTHENTICATE %s", base64.StdEncoding.EncodeToString([]byte(sfirst)))
				step = 1
			case 1: // client-final
				proofB64 := scramAttrs(msg)["p"]
				noProof := strings.TrimSuffix(msg, ",p="+proofB64)
				authMsg := bare + "," + sfirst + "," + noProof
				stored := v.New()
				stored.Write(mac(salted, "Client Key"))
				clientSig := mac(stored.Sum(nil), authMsg)
				proof, _ := base64.StdEncoding.DecodeString(proofB64)
				ck := make([]byte, len(proof))
				for i := range proof {
					ck[i] = proof[i] ^ clientSig[i%len(clientSig)]
				}
				chk := v.New()
				chk.Write(ck)
				if !hmac.Equal(chk.Sum(nil), stored.Sum(nil)) {
					w(":srv 904 %s :SASL authentication failed", nick)
					return
				}
				sig := "v=" + base64.StdEncoding.EncodeToString(mac(mac(salted, "Server Key"), authMsg))
				w("AUTHENTICATE %s", base64.StdEncoding.EncodeToString([]byte(sig)))
				step = 2
			case 2: // empty final response
				w(":srv 900 %s %s!u@h acct :You are now logged in", nick, nick)
				w(":srv 903 %s :SASL authentication successful", nick)
				s.done.Store(true)
			}
		}
	}
}

func runScramLogin(t *testing.T, advertise, serverPass, clientPass string) (chose string, registered bool, err error) {
	t.Helper()
	ln, lerr := net.Listen("tcp", "127.0.0.1:0")
	if lerr != nil {
		t.Fatal(lerr)
	}
	defer ln.Close()
	srv := &scramServerConn{t: t, advertise: advertise, pass: serverPass, chose: make(chan string, 1)}
	go func() {
		c, aerr := ln.Accept()
		if aerr == nil {
			srv.serve(c)
		}
	}()

	h := &lockedHandler{}
	c, nerr := New(Options{
		Network: "n", Addr: ln.Addr().String(), Nick: "me", User: "me", Realname: "me",
		SASLUser: "alice", SASLPass: clientPass, SASLScram: true,
	}, h)
	if nerr != nil {
		t.Fatal(nerr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- c.Connect(ctx) }()

	deadline := time.After(6 * time.Second)
	for {
		select {
		case m := <-srv.chose:
			chose = m
		case err = <-errc:
			return chose, registered, err
		case <-deadline:
			return chose, registered, errors.New("timeout")
		case <-time.After(20 * time.Millisecond):
			h.mu.Lock()
			for _, ev := range h.evs {
				if ev.Type == core.EvConnect {
					registered = true
				}
			}
			h.mu.Unlock()
			if registered {
				cancel()
			}
		}
	}
}

// A SCRAM login over the real connection code: the CAP LS handler must record
// the server's mechanisms before girc asks for one, and the exchange must
// complete against an independent server implementation.
func TestSCRAMLoginPicksStrongestAdvertised(t *testing.T) {
	for _, tt := range []struct {
		name, advertise, want string
	}{
		{"libera style", "ECDSA-NIST256P-CHALLENGE,EXTERNAL,PLAIN,SCRAM-SHA-512", "SCRAM-SHA-512"},
		{"both offered", "PLAIN,SCRAM-SHA-256,SCRAM-SHA-512", "SCRAM-SHA-512"},
		{"sha256 only", "PLAIN,SCRAM-SHA-256", "SCRAM-SHA-256"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			chose, registered, err := runScramLogin(t, tt.advertise, "hunter2", "hunter2")
			if chose != tt.want {
				t.Errorf("client asked for %q, want %q", chose, tt.want)
			}
			if !registered {
				t.Errorf("never registered (Connect err: %v)", err)
			}
		})
	}
}

func TestSCRAMLoginWrongPassword(t *testing.T) {
	_, registered, err := runScramLogin(t, "SCRAM-SHA-256", "hunter2", "wrong")
	if registered {
		t.Fatal("registered with the wrong password")
	}
	if !errors.Is(err, core.ErrAuthFailed) {
		t.Fatalf("Connect err = %v, want ErrAuthFailed", err)
	}
}

// soju sends "CAP NEW sasl" to a bound connection before RPL_WELCOME. girc
// answers each by re-requesting sasl and re-authenticating unless SASL was
// switched off once it succeeded; the server then rejects the second attempt
// and the connection dies.
func TestNoSASLRerunAfterSuccess(t *testing.T) {
	for _, tt := range []struct {
		name  string
		scram bool
	}{{"scram", true}, {"plain", false}} {
		t.Run(tt.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			srv := &scramServerConn{t: t, advertise: "PLAIN,SCRAM-SHA-256", pass: "hunter2", chose: make(chan string, 1), capNew: true}
			accepted := make(chan net.Conn, 1)
			go func() {
				if c, err := ln.Accept(); err == nil {
					accepted <- c
					srv.serve(c)
				}
			}()
			h := &lockedHandler{}
			c, err := New(Options{
				Network: "n", Addr: ln.Addr().String(), Nick: "me", User: "me", Realname: "me",
				SASLUser: "alice", SASLPass: "hunter2", SASLScram: tt.scram,
			}, h)
			if err != nil {
				t.Fatal(err)
			}
			errc := make(chan error, 1)
			go func() { errc <- c.Connect(context.Background()) }()
			var connErr error
			select {
			case connErr = <-errc:
			case <-time.After(2500 * time.Millisecond):
				// Still up: end it from the server side (girc's own Close races
				// with Connect on the context-cancel path).
				(<-accepted).Close()
				connErr = <-errc
			}
			if errors.Is(connErr, core.ErrAuthFailed) {
				t.Fatalf("connection dropped by a second SASL attempt: %v", connErr)
			}
			if n := srv.late.Load(); n != 0 {
				t.Errorf("client re-ran SASL %d time(s) after it succeeded", n)
			}
		})
	}
}
