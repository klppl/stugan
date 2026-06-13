// Package irc owns all IRC protocol concerns and is the only package that
// imports the underlying IRC library (github.com/lrstanley/girc). Callers
// depend on the core.IRCConn interface, never on a girc type, so the
// implementation can be swapped for a custom IRCv3 core later without
// touching core/, server/, or plugin/.
package irc

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/klippelism/stugan/internal/core"
	"github.com/lrstanley/girc"
)

// Options configures a single network connection. main builds these from
// config so this package never imports the config package.
type Options struct {
	Network string // network id/name, stamped onto every event
	Addr    string // host:port (the primary server)
	// Fallbacks are additional host:port servers tried in order when a
	// connection fails to establish on the current one. A server that stays up
	// keeps being used; only a failed or immediately-flapping connection rotates
	// to the next. Empty means the network has only the primary Addr.
	Fallbacks []string
	TLS       bool
	// Insecure skips TLS certificate verification (self-signed / LAN
	// servers). Only meaningful with TLS.
	Insecure bool
	Nick     string
	User     string
	Realname string
	SASLUser string
	SASLPass string
	// ServerPass is the connection password (IRC PASS); empty disables it.
	ServerPass string
	// SASLExternal authenticates via SASL EXTERNAL (CertFP) instead of PLAIN.
	// Requires CertPEM and TLS.
	SASLExternal bool
	// CertPEM is a client certificate (cert + private key concatenated, PEM)
	// presented during the TLS handshake for CertFP / SASL EXTERNAL.
	CertPEM  string
	Channels []string // auto-joined after registration
	// ChannelKeys maps a channel in Channels to its join key (+k password).
	// Channels without a key are absent.
	ChannelKeys map[string]string
	// Monitor is the friends list watched via IRCv3 MONITOR, re-armed after
	// registration on every (re)connect.
	Monitor []string
	Logger  *slog.Logger
}

// Conn is a girc-backed implementation of core.IRCConn.
type Conn struct {
	opts    Options
	handler core.ConnHandler
	log     *slog.Logger
	client  *girc.Client
	// addrs is the ordered server list (primary first, then Fallbacks); addrIdx
	// is the one the next Connect dials. Connect is called serially by the
	// engine's reconnect loop, so these need no locking.
	addrs   []string
	addrIdx int
	// batches collects open inbound draft/multiline batches (ref → buffer). Read
	// and written only from girc's single read goroutine (the BATCH and message
	// handlers), so it needs no locking.
	batches map[string]*multilineBatch
	// batchSeq names outbound multiline batches; atomic since Message may be
	// called from the engine loop or a plugin goroutine.
	batchSeq atomic.Uint64
}

// multilineBatch accumulates the lines of one inbound draft/multiline message
// until the closing BATCH. tags/source come from the opening BATCH command,
// which carries the msgid/time/account for the whole logical message.
type multilineBatch struct {
	notice bool
	target string
	source *girc.Source
	tags   girc.Tags
	lines  []string
	concat []bool // concat[i]: join line i to the previous with no newline
}

// compile-time check that Conn satisfies the interface core depends on.
var _ core.IRCConn = (*Conn)(nil)

// New builds a connection. Inbound events are delivered to handler. It does
// not dial; call Connect.
func New(opts Options, handler core.ConnHandler) (*Conn, error) {
	host, port, err := splitAddr(opts.Addr, opts.TLS)
	if err != nil {
		return nil, err
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	gcfg := girc.Config{
		Server:      host,
		Port:        port,
		Nick:        firstNonEmpty(opts.Nick, "stugan"),
		User:        firstNonEmpty(opts.User, opts.Nick, "stugan"),
		Name:        firstNonEmpty(opts.Realname, "stugan"),
		SSL:         opts.TLS,
		ServerPass:  opts.ServerPass,
		Version:     "stugan",
		RecoverFunc: girc.DefaultRecoverHandler, // a handler panic never kills us
		// Request caps girc doesn't enable by default: echo-message (own
		// sent lines come back with the server's msgid/server-time) and
		// draft/chathistory (server-side history on bouncers/ergo).
		SupportedCaps: map[string][]string{
			"echo-message":      nil,
			"draft/chathistory": nil,
			// account-tag stamps the sender's account on every message (not
			// just JOINs); labeled-response correlates replies; standard-replies
			// gives structured FAIL/WARN/NOTE; message-redaction enables
			// deleting messages. Reactions ride the already-negotiated
			// message-tags via the +draft/react client tag (no separate cap).
			"account-tag":             nil,
			"labeled-response":        nil,
			"standard-replies":        nil,
			"draft/message-redaction": nil,
			// draft/multiline groups a message split across several lines into
			// one logical block via BATCH (see (un)batchMultiline).
			"draft/multiline": nil,
		},
	}
	// A client certificate (CertFP / SASL EXTERNAL) or skipping verification
	// (self-signed servers) both need our own TLSConfig. girc uses it verbatim
	// when set, so we must supply ServerName ourselves (girc only fills it in
	// for its own default config).
	if opts.CertPEM != "" || opts.Insecure {
		tcfg := &tls.Config{
			ServerName:         host,
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: opts.Insecure, // user opt-in for self-signed/LAN servers
		}
		if opts.CertPEM != "" {
			cert, err := tls.X509KeyPair([]byte(opts.CertPEM), []byte(opts.CertPEM))
			if err != nil {
				return nil, fmt.Errorf("parse client certificate: %w", err)
			}
			tcfg.Certificates = []tls.Certificate{cert}
		}
		gcfg.TLSConfig = tcfg
	}
	switch {
	case opts.SASLExternal:
		gcfg.SASL = &girc.SASLExternal{}
	case opts.SASLUser != "":
		gcfg.SASL = &girc.SASLPlain{User: opts.SASLUser, Pass: opts.SASLPass}
	}

	addrs := []string{opts.Addr}
	for _, a := range opts.Fallbacks {
		if a = strings.TrimSpace(a); a != "" {
			addrs = append(addrs, a)
		}
	}
	c := &Conn{
		opts:    opts,
		handler: handler,
		log:     log.With("network", opts.Network),
		client:  girc.New(gcfg),
		addrs:   addrs,
		batches: map[string]*multilineBatch{},
	}
	c.registerHandlers()
	return c, nil
}

// registerHandlers wires girc callbacks to the normalized event bus.
func (c *Conn) registerHandlers() {
	h := c.client.Handlers

	h.Add(girc.CONNECTED, func(gc *girc.Client, _ girc.Event) {
		// Drop any draft/multiline batch left half-open by a previous
		// connection that dropped mid-batch (no closing BATCH -ref arrived),
		// so it can't linger across reconnects. Runs on girc's single handler
		// goroutine, same as absorbMultiline/handleBatch, so no lock is needed.
		clear(c.batches)
		c.emit(core.Event{Type: core.EvConnect, Network: c.opts.Network, Nick: gc.GetNick()})
		c.autojoin(gc)
		c.armMonitor(gc)
	})

	// MONITOR status replies: 730 = the listed nicks are online, 731 = offline.
	// These don't go through toEvent (they're not user-visible system lines) —
	// they update the friends-list presence directly.
	h.Add(girc.RPL_MONONLINE, func(_ *girc.Client, e girc.Event) { c.emitMonitor(&e, true) })
	h.Add(girc.RPL_MONOFFLINE, func(_ *girc.Client, e girc.Event) { c.emitMonitor(&e, false) })
	h.Add(girc.DISCONNECTED, func(_ *girc.Client, e girc.Event) {
		c.emit(core.Event{Type: core.EvDisconnect, Network: c.opts.Network, Text: e.Last()})
	})

	cmds := []string{
		girc.PRIVMSG, girc.NOTICE, girc.JOIN, girc.PART,
		girc.QUIT, girc.NICK, girc.TOPIC, girc.RPL_TOPIC, girc.RPL_NAMREPLY, girc.AWAY,
		girc.RPL_LIST, girc.RPL_LISTEND, girc.CAP_TAGMSG,
		// IRCv3 message-redaction (REDACT) and standard-replies (FAIL/WARN/NOTE).
		"REDACT", "FAIL", "WARN", "NOTE",
	}
	// BATCH brackets a group of messages. We only act on draft/multiline
	// batches (reassembled into one message); other types (e.g. chathistory)
	// just tag their members, which pass through normally.
	h.Add("BATCH", func(gc *girc.Client, e girc.Event) { c.handleBatch(gc, &e) })

	cmds = append(cmds, numericReplies...)
	for _, cmd := range cmds {
		h.Add(cmd, func(gc *girc.Client, e girc.Event) {
			// A message inside an open draft/multiline batch is buffered, not
			// emitted on its own — it surfaces as one message at BATCH close.
			if c.absorbMultiline(&e) {
				return
			}
			if ev, ok := toEvent(c.opts.Network, &e, gc.GetNick()); ok {
				c.emit(ev)
			}
		})
	}

	// Channel MODE needs the server's PREFIX/CHANMODES to map mode letters to
	// membership prefixes and consume arguments, so it gets a dedicated handler
	// (the generic toEvent path above has no access to those server options).
	h.Add(girc.MODE, func(gc *girc.Client, e girc.Event) {
		prefix, ok := gc.GetServerOption("PREFIX")
		if !ok {
			prefix = girc.DefaultPrefixes
		}
		chanmodes, ok := gc.GetServerOption("CHANMODES")
		if !ok {
			chanmodes = girc.ModeDefaults
		}
		if ev, ok := channelModeEvent(c.opts.Network, &e, prefix, chanmodes); ok {
			c.emit(ev)
		}
	})

	// echo-message events are NOT delivered to command-specific handlers by
	// girc (only to ALLEVENTS), so handle our own echoed PRIVMSG/NOTICE here.
	// Gating on e.Echo avoids double-processing the normal events above.
	h.Add(girc.ALL_EVENTS, func(gc *girc.Client, e girc.Event) {
		if e.Echo {
			// An echoed draft/multiline message arrives as a BATCH whose member
			// lines carry e.Echo; like the command path, absorb them so the
			// reassembled message surfaces once at BATCH close rather than as
			// separate lines plus an empty one. (The BATCH open/close commands
			// are not echoes, so handleBatch still runs from the command path.)
			if c.absorbMultiline(&e) {
				return
			}
			if ev, ok := toEvent(c.opts.Network, &e, gc.GetNick()); ok {
				c.emit(ev)
			}
		}
	})
}

// autojoin (re)joins the configured channels after registration. Keyed
// channels are sent individually with their key; keyless ones are batched.
func (c *Conn) autojoin(gc *girc.Client) {
	keyed, keyless := planAutojoin(c.opts.Channels, c.opts.ChannelKeys)
	for _, k := range keyed {
		gc.Cmd.JoinKey(k.channel, k.key)
	}
	if len(keyless) > 0 {
		gc.Cmd.Join(keyless...)
	}
}

// monitorChunk is how many nicks ride one MONITOR + command. MONITOR targets
// are comma-separated in a single parameter, so this keeps the line well within
// IRC's 512-byte budget; a list longer than the server's MONITOR limit just
// draws a 734 the server handles.
const monitorChunk = 15

// armMonitor watches the friends list via MONITOR after registration, chunked
// so a long list stays within the line-length budget.
func (c *Conn) armMonitor(gc *girc.Client) {
	for i := 0; i < len(c.opts.Monitor); i += monitorChunk {
		end := min(i+monitorChunk, len(c.opts.Monitor))
		gc.Cmd.Monitor('+', strings.Join(c.opts.Monitor[i:end], ","))
	}
}

// emitMonitor turns a 730/731 reply into an EvMonitor. The trailing param is a
// comma-separated target list — nick!user@host for 730, bare nick for 731 — so
// we keep just the nick of each.
func (c *Conn) emitMonitor(e *girc.Event, online bool) {
	list := e.Last()
	if list == "" {
		return
	}
	var nicks []string
	for tok := range strings.SplitSeq(list, ",") {
		tok = strings.TrimSpace(tok)
		if i := strings.IndexByte(tok, '!'); i >= 0 {
			tok = tok[:i]
		}
		if tok != "" {
			nicks = append(nicks, tok)
		}
	}
	if len(nicks) > 0 {
		c.emit(core.Event{Type: core.EvMonitor, Network: c.opts.Network, Online: online, Args: nicks})
	}
}

type channelKey struct{ channel, key string }

// planAutojoin splits the auto-join list into keyed channels (joined one at a
// time with JoinKey) and keyless channels (batched into one JOIN). Keeping it
// pure makes the keyed/keyless decision unit-testable without a live client.
func planAutojoin(channels []string, keys map[string]string) (keyed []channelKey, keyless []string) {
	for _, ch := range channels {
		if key := keys[ch]; key != "" {
			keyed = append(keyed, channelKey{ch, key})
		} else {
			keyless = append(keyless, ch)
		}
	}
	return keyed, keyless
}

func (c *Conn) emit(ev core.Event) {
	if c.handler != nil {
		c.handler.HandleEvent(ev)
	}
}

// Connect runs the connection, blocking until disconnected or ctx is
// cancelled (which closes the underlying socket).
// stableConn is how long a connection must last before we treat the current
// server as healthy and stay on it; a shorter-lived attempt (a failed dial or
// an immediate flap) rotates to the next fallback on the following Connect.
const stableConn = 30 * time.Second

func (c *Conn) Connect(ctx context.Context) error {
	// Point girc at the current address. A bad address is skipped by advancing
	// past it so the next retry tries another server rather than wedging here.
	if err := c.useAddr(c.addrs[c.addrIdx]); err != nil {
		c.advanceAddr()
		return fmt.Errorf("irc %s: %w", c.opts.Network, err)
	}

	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			c.client.Close()
		case <-stop:
		}
	}()
	defer close(stop)

	start := time.Now()
	err := c.client.Connect()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Rotate to the next fallback only when the connection didn't stay up — a
	// stable session that merely dropped should retry the same server first.
	if time.Since(start) < stableConn {
		c.advanceAddr()
	}
	if err != nil {
		return fmt.Errorf("irc %s: %w", c.opts.Network, err)
	}
	return nil
}

// useAddr points girc at addr for the next dial. When we own the TLS config
// (client cert or skip-verify), its ServerName must track the host too; when we
// don't, girc fills ServerName from Config.Server itself.
func (c *Conn) useAddr(addr string) error {
	host, port, err := splitAddr(addr, c.opts.TLS)
	if err != nil {
		return err
	}
	c.client.Config.Server = host
	c.client.Config.Port = port
	if c.client.Config.TLSConfig != nil {
		c.client.Config.TLSConfig.ServerName = host
	}
	return nil
}

// advanceAddr moves to the next server in the list, wrapping around. A no-op
// when there are no fallbacks.
func (c *Conn) advanceAddr() {
	if len(c.addrs) > 1 {
		c.addrIdx = (c.addrIdx + 1) % len(c.addrs)
	}
}

// SendRaw writes a raw IRC line.
func (c *Conn) SendRaw(line string) error { return c.client.Cmd.SendRaw(line) }

// Message sends a PRIVMSG to target.
func (c *Conn) Message(target, text string) error {
	if !strings.Contains(text, "\n") {
		c.client.Cmd.Message(target, text)
		return nil
	}
	lines := strings.Split(text, "\n")
	// On a server that negotiated draft/multiline, ship the lines as one logical
	// message inside a BATCH; otherwise fall back to one PRIVMSG per line so the
	// text still gets through, just as separate lines.
	if !c.client.HasCapability("draft/multiline") {
		for _, ln := range lines {
			c.client.Cmd.Message(target, ln)
		}
		return nil
	}
	ref := "ml" + strconv.FormatUint(c.batchSeq.Add(1), 10)
	_ = c.client.Cmd.SendRaw("BATCH +" + ref + " draft/multiline " + target)
	for _, ln := range lines {
		_ = c.client.Cmd.SendRaw("@batch=" + ref + " PRIVMSG " + target + " :" + ln)
	}
	_ = c.client.Cmd.SendRaw("BATCH -" + ref)
	return nil
}

// handleBatch opens or closes a draft/multiline batch. Other batch types are
// ignored here (their member messages still pass through normally, tagged).
func (c *Conn) handleBatch(gc *girc.Client, e *girc.Event) {
	if len(e.Params) == 0 || len(e.Params[0]) < 2 {
		return
	}
	ref := e.Params[0][1:]
	switch e.Params[0][0] {
	case '+':
		if len(e.Params) >= 3 && e.Params[1] == "draft/multiline" {
			c.batches[ref] = &multilineBatch{
				notice: false,
				target: e.Params[2],
				source: e.Source,
				tags:   e.Tags,
			}
		}
	case '-':
		if b := c.batches[ref]; b != nil {
			delete(c.batches, ref)
			c.finishBatch(gc, b)
		}
	}
}

// absorbMultiline buffers a PRIVMSG/NOTICE that belongs to an open
// draft/multiline batch and reports that it was consumed (so the caller does
// not emit it as a standalone message). Anything else returns false.
func (c *Conn) absorbMultiline(e *girc.Event) bool {
	if e.Command != girc.PRIVMSG && e.Command != girc.NOTICE {
		return false
	}
	b := c.batches[tag(e, "batch")]
	if b == nil {
		return false
	}
	b.notice = e.Command == girc.NOTICE
	b.lines = append(b.lines, e.Last())
	// draft/multiline-concat is a value-less tag, so test presence, not value.
	concat := false
	if e.Tags != nil {
		_, concat = e.Tags.Get("draft/multiline-concat")
	}
	b.concat = append(b.concat, concat)
	return true
}

// finishBatch reassembles a closed draft/multiline batch into one synthetic
// PRIVMSG/NOTICE and runs it through the normal translation path, so buffer
// routing, msgid, account and echo handling are reused unchanged.
func (c *Conn) finishBatch(gc *girc.Client, b *multilineBatch) {
	cmd := girc.PRIVMSG
	if b.notice {
		cmd = girc.NOTICE
	}
	syn := &girc.Event{
		Command: cmd,
		Source:  b.source,
		Params:  []string{b.target, joinMultiline(b.lines, b.concat)},
		Tags:    b.tags,
	}
	if ev, ok := toEvent(c.opts.Network, syn, gc.GetNick()); ok {
		c.emit(ev)
	}
}

// joinMultiline concatenates batch lines: each is preceded by a newline except
// where draft/multiline-concat asks for it to be glued to the previous line.
func joinMultiline(lines []string, concat []bool) string {
	var b strings.Builder
	for i, ln := range lines {
		if i > 0 && (i >= len(concat) || !concat[i]) {
			b.WriteByte('\n')
		}
		b.WriteString(ln)
	}
	return b.String()
}

// knownCaps are the IRCv3 capabilities stugan cares about; Caps reports
// which of them the server negotiated.
var knownCaps = []string{
	"echo-message", "server-time", "away-notify", "account-notify",
	"message-tags", "multi-prefix", "extended-join", "userhost-in-names",
	"chghost", "setname", "invite-notify", "draft/chathistory", "chathistory",
	"account-tag", "labeled-response", "standard-replies",
	"draft/message-redaction", "draft/multiline",
}

// Caps returns the negotiated IRCv3 capabilities (from the set stugan uses).
func (c *Conn) Caps() []string {
	var caps []string
	for _, name := range knownCaps {
		if c.client.HasCapability(name) {
			caps = append(caps, name)
		}
	}
	return caps
}

// CurrentNick returns our current nick on this network.
func (c *Conn) CurrentNick() string { return c.client.GetNick() }

// Close terminates the connection.
func (c *Conn) Close() error {
	c.client.Close()
	return nil
}

// splitAddr parses "host:port" into a host and port, defaulting the port to
// the standard IRC TLS/plaintext port when absent.
func splitAddr(addr string, tls bool) (string, int, error) {
	if addr == "" {
		return "", 0, fmt.Errorf("irc: empty addr")
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		// No port specified: treat the whole string as the host.
		host = addr
		if tls {
			return host, 6697, nil
		}
		return host, 6667, nil
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("irc: bad port in %q: %w", addr, err)
	}
	return host, port, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
