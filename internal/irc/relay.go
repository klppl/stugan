package irc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/klippelism/stugan/internal/core"
	"github.com/klippelism/stugan/internal/enginelink"
	"github.com/lrstanley/girc"
)

// ackInterval is how often processed lines are acked to the engine. An ack
// only goes out after a core barrier (see syncThen), so this bounds how many
// already-committed lines a crash could see re-delivered.
const ackInterval = 250 * time.Millisecond

// connectRelay is Connect through stugan-engine: connect-or-attach, then run
// girc over the relay session until it ends.
func (c *Conn) connectRelay(ctx context.Context) error {
	host, port, err := splitAddr(c.addrs[c.addrIdx], c.opts.TLS)
	if err != nil {
		c.advanceAddr()
		return fmt.Errorf("irc %s: %w", c.opts.Network, err)
	}
	// girc validates Server/Port even though the relay dialer ignores them.
	c.client.Config.Server, c.client.Config.Port = host, port
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	via := "plaintext"
	if c.opts.TLS {
		via = "TLS"
	}
	c.emit(core.Event{
		Type: core.EvNumeric, Network: c.opts.Network, Time: time.Now(),
		Text: fmt.Sprintf("connecting to %s (%s) via stugan-engine…", addr, via),
	})

	sess, err := c.opts.Relay.Open(ctx, enginelink.ConnectRequest{
		ID: c.opts.RelayID, Addr: addr, TLS: c.opts.TLS, Insecure: c.opts.Insecure,
		CertPEM: c.opts.CertPEM, Fingerprint: c.fingerprint,
	})
	if ctx.Err() != nil {
		if sess != nil {
			_ = sess.Close()
		}
		return ctx.Err()
	}
	if err != nil {
		// Only the IRC server being unreachable rotates fallbacks; an engine
		// problem says nothing about this server.
		var ue *enginelink.UpstreamError
		if errors.As(err, &ue) {
			c.advanceAddr()
		}
		return fmt.Errorf("irc %s: %w", c.opts.Network, err)
	}
	c.sess.Store(sess)
	if c.closing.Load() {
		// Close ran while the connect was in flight.
		sess.Terminate()
		return net.ErrClosed
	}

	info := sess.Info()
	c.lastSeq.Store(0)
	c.processed.Store(0)
	// girc learns the nick from 001 on a background goroutine; until then
	// GetNick falls back to Config.Nick. A replay is processed in a burst, so
	// the first backlog lines could be judged against a stale nick (echo and
	// self detection). On attach, the configured nick is the live one; a
	// fresh registration uses the network's nick as always.
	c.client.Config.Nick = firstNonEmpty(c.opts.Nick, "stugan")
	if info.Attached && info.Nick != "" {
		c.client.Config.Nick = info.Nick
	}
	if info.Attached {
		c.restoring.Store(true)
		c.skipConnected.Store(!info.Unattended)
		c.resume.Store(&resumeState{info: info, marker: sess.Marker()})
		sess.OnRestored(func() { c.restoring.Store(false) })
	} else {
		c.restoring.Store(false)
		c.skipConnected.Store(false)
		c.resume.Store(nil)
	}
	if c.mech != nil {
		c.client.Config.SASL = c.mech
	}

	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			c.client.Close()
		case <-stop:
		}
	}()
	ackDone := make(chan struct{})
	ackExited := make(chan struct{})
	go func() {
		defer close(ackExited)
		c.ackLoop(sess, ackDone)
	}()

	start := time.Now()
	c.authFailed.Store(false)
	err = c.client.DialerConnect(relayDialer{sess})
	close(stop)
	close(ackDone)
	<-ackExited
	// girc has returned, so every line it read has been handled.
	if last := c.lastSeq.Load(); last > 0 {
		c.syncThen(func() { sess.Ack(last) })
	}
	c.restoring.Store(false)
	c.resume.Store(nil)
	if ctx.Err() == nil && c.authFailed.Load() {
		// Rejected credentials: the network is parked, and the half-
		// registered socket must not linger in the engine.
		sess.Terminate()
		return fmt.Errorf("irc %s: %w: %v", c.opts.Network, core.ErrAuthFailed, err)
	}
	// Detach (if girc's close didn't already): the engine keeps the socket
	// unless Close asked for it to end.
	_ = sess.Close()

	if ctx.Err() != nil {
		return ctx.Err()
	}
	if time.Since(start) < stableConn {
		c.advanceAddr()
	}
	if err != nil {
		return fmt.Errorf("irc %s: %w", c.opts.Network, err)
	}
	return nil
}

// ackLoop periodically acks the lines girc has finished handling.
func (c *Conn) ackLoop(sess *enginelink.Session, done <-chan struct{}) {
	t := time.NewTicker(ackInterval)
	defer t.Stop()
	var queued uint64
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		if p := c.processed.Load(); p > queued {
			queued = p
			c.syncThen(func() { sess.Ack(p) })
		}
	}
}

// syncThen runs fn once every event emitted so far has been committed by the
// engine loop (when the handler offers that barrier), so an ack never covers a
// line whose events are still queued in memory.
func (c *Conn) syncThen(fn func()) {
	if s, ok := c.handler.(interface{ Sync(func()) }); ok {
		s.Sync(fn)
		return
	}
	fn()
}

// finishResume runs when girc answers the restore marker on its handler
// goroutine — after the whole replay and before any backlog line, so the
// resumed connect reaches core ahead of the messages that arrived while
// stugan was away.
func (c *Conn) finishResume(r *resumeState) {
	info := r.info
	text := fmt.Sprintf("resumed connection held by stugan-engine (detached %s, %d buffered line(s))",
		info.DetachedFor.Round(time.Second), info.Backlog)
	c.emit(core.Event{Type: core.EvNumeric, Network: c.opts.Network, Time: time.Now(), Text: text})
	if info.Lost > 0 {
		c.emit(core.Event{
			Type: core.EvNumeric, Network: c.opts.Network, Time: time.Now(),
			Text: fmt.Sprintf("warning: stugan-engine's buffer filled while stugan was down; %d earlier line(s) were lost", info.Lost),
		})
	}
	if info.Unattended {
		// Registration completed with nobody attached, so perform/autojoin
		// never ran: the replayed welcome's CONNECTED (not skipped) runs them
		// as for any fresh registration.
		return
	}
	nick := info.Nick
	if nick == "" {
		nick = c.client.GetNick()
	}
	c.emit(core.Event{
		Type: core.EvConnect, Network: c.opts.Network, Nick: nick,
		Resumed: true, Args: info.Channels,
	})
	c.armMonitor()
	// Member lists and topics live only in memory, so a new stugan process
	// knows none of them: ask again. Background priority, paced by girc's
	// flood control; NAMES and TOPIC replies are silent in the buffers.
	if len(info.Channels) > 0 {
		chans := append([]string(nil), info.Channels...)
		go func() {
			for _, ch := range chans {
				for _, line := range []string{"NAMES " + ch, "TOPIC " + ch} {
					if c.outbound.enqueueBackground(outboundRequest{raw: line}) != nil {
						return
					}
				}
			}
		}()
	}
}

// relayDialer hands girc the relay session as its "socket".
type relayDialer struct{ conn net.Conn }

func (d relayDialer) Dial(string, string) (net.Conn, error) { return d.conn, nil }

// relayTag returns the sequence number in tag key (TagSeq or TagAck) of a
// relayed line.
func relayTag(e *girc.Event, key string) (uint64, bool) {
	if e.Tags == nil {
		return 0, false
	}
	v, ok := e.Tags.Get(key)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(v, 10, 64)
	return n, err == nil && n > 0
}

// markProcessed raises the processed watermark (it never goes back).
func (c *Conn) markProcessed(seq uint64) {
	for {
		cur := c.processed.Load()
		if seq <= cur || c.processed.CompareAndSwap(cur, seq) {
			return
		}
	}
}

// isReplay reports whether e came from an attach replay.
func isReplay(e *girc.Event) bool {
	if e.Tags == nil {
		return false
	}
	_, ok := e.Tags.Get(enginelink.TagReplay)
	return ok
}

// registrationFingerprint digests every setting that shapes how the socket
// registered — the same set core treats as needing a reconnect when edited.
// The nick is left out on purpose: a nick change applies live, and must not
// turn the next restart into a fresh connection.
func registrationFingerprint(o Options, addrs []string) string {
	parts := []string{
		"v1",
		strings.Join(addrs, ","),
		strconv.FormatBool(o.TLS), strconv.FormatBool(o.Insecure),
		o.User, o.Realname, o.SASLUser, o.SASLPass, o.ServerPass,
		strconv.FormatBool(o.SASLExternal), o.CertPEM,
	}
	// Appended only when set, so every existing fingerprint stays valid.
	if o.SASLScram && !o.SASLExternal && o.SASLUser != "" {
		parts = append(parts, "scram")
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}
