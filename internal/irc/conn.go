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

	"github.com/klippelism/stugan/internal/core"
	"github.com/lrstanley/girc"
)

// Options configures a single network connection. main builds these from
// config so this package never imports the config package.
type Options struct {
	Network  string // network id/name, stamped onto every event
	Addr     string // host:port
	TLS      bool
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
	Logger      *slog.Logger
}

// Conn is a girc-backed implementation of core.IRCConn.
type Conn struct {
	opts    Options
	handler core.ConnHandler
	log     *slog.Logger
	client  *girc.Client
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
		},
	}
	// A client certificate enables CertFP and is required for SASL EXTERNAL.
	// girc uses our TLSConfig verbatim when set, so we must supply ServerName
	// ourselves (girc only fills it in for its own default config).
	if opts.CertPEM != "" {
		cert, err := tls.X509KeyPair([]byte(opts.CertPEM), []byte(opts.CertPEM))
		if err != nil {
			return nil, fmt.Errorf("parse client certificate: %w", err)
		}
		gcfg.TLSConfig = &tls.Config{
			ServerName:   host,
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
	}
	switch {
	case opts.SASLExternal:
		gcfg.SASL = &girc.SASLExternal{}
	case opts.SASLUser != "":
		gcfg.SASL = &girc.SASLPlain{User: opts.SASLUser, Pass: opts.SASLPass}
	}

	c := &Conn{
		opts:    opts,
		handler: handler,
		log:     log.With("network", opts.Network),
		client:  girc.New(gcfg),
	}
	c.registerHandlers()
	return c, nil
}

// registerHandlers wires girc callbacks to the normalized event bus.
func (c *Conn) registerHandlers() {
	h := c.client.Handlers

	h.Add(girc.CONNECTED, func(gc *girc.Client, _ girc.Event) {
		c.emit(core.Event{Type: core.EvConnect, Network: c.opts.Network, Nick: gc.GetNick()})
		c.autojoin(gc)
	})
	h.Add(girc.DISCONNECTED, func(_ *girc.Client, e girc.Event) {
		c.emit(core.Event{Type: core.EvDisconnect, Network: c.opts.Network, Text: e.Last()})
	})

	cmds := []string{
		girc.PRIVMSG, girc.NOTICE, girc.JOIN, girc.PART,
		girc.QUIT, girc.NICK, girc.TOPIC, girc.RPL_NAMREPLY, girc.AWAY,
		girc.RPL_LIST, girc.RPL_LISTEND, girc.CAP_TAGMSG,
		// IRCv3 message-redaction (REDACT) and standard-replies (FAIL/WARN/NOTE).
		"REDACT", "FAIL", "WARN", "NOTE",
	}
	cmds = append(cmds, numericReplies...)
	for _, cmd := range cmds {
		h.Add(cmd, func(gc *girc.Client, e girc.Event) {
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
func (c *Conn) Connect(ctx context.Context) error {
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			c.client.Close()
		case <-stop:
		}
	}()
	defer close(stop)

	if err := c.client.Connect(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("irc %s: %w", c.opts.Network, err)
	}
	return nil
}

// SendRaw writes a raw IRC line.
func (c *Conn) SendRaw(line string) error { return c.client.Cmd.SendRaw(line) }

// Message sends a PRIVMSG to target.
func (c *Conn) Message(target, text string) error {
	c.client.Cmd.Message(target, text)
	return nil
}

// knownCaps are the IRCv3 capabilities stugan cares about; Caps reports
// which of them the server negotiated.
var knownCaps = []string{
	"echo-message", "server-time", "away-notify", "account-notify",
	"message-tags", "multi-prefix", "extended-join", "userhost-in-names",
	"chghost", "setname", "invite-notify", "draft/chathistory", "chathistory",
	"account-tag", "labeled-response", "standard-replies",
	"draft/message-redaction",
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
