package core

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// soju.im/bouncer-networks, client side. When stugan connects to soju without
// binding to one of its networks (a SASL login of just "user"), soju offers a
// control channel: BOUNCER LISTNETWORKS / ADDNETWORK / DELNETWORK, and with
// -notify pushes every change. The list rides the network snapshot to the UI,
// which can add and remove soju networks and "open" one — create a stugan
// network bound to it (login "user/network"), the way the docs describe doing
// by hand.

const capBouncerNetworks = "soju.im/bouncer-networks"

// BouncerNetwork is one upstream network a bouncer (soju) manages for us.
type BouncerNetwork struct {
	ID    string
	Name  string
	Host  string
	Port  int
	TLS   bool
	Nick  string
	State string // connected, connecting or disconnected
	Error string
}

// ParseBouncerAttrs parses the "key=value;key=value" attribute list of a
// BOUNCER NETWORK line (values escaped like message tags). Unknown keys are
// ignored.
func ParseBouncerAttrs(id, attrs string) BouncerNetwork {
	b := BouncerNetwork{ID: id}
	for _, kv := range strings.Split(attrs, ";") {
		k, v, _ := strings.Cut(kv, "=")
		v = unescapeBouncerValue(v)
		switch k {
		case "name":
			b.Name = v
		case "host":
			b.Host = v
		case "port":
			b.Port, _ = strconv.Atoi(v)
		case "tls":
			b.TLS = v == "1"
		case "nickname":
			b.Nick = v
		case "state":
			b.State = v
		case "error":
			b.Error = v
		}
	}
	return b
}

var (
	bouncerUnescaper = strings.NewReplacer(`\:`, ";", `\s`, " ", `\\`, `\`, `\r`, "\r", `\n`, "\n")
	bouncerEscaper   = strings.NewReplacer(`\`, `\\`, ";", `\:`, " ", `\s`, "\r", `\r`, "\n", `\n`)
)

func unescapeBouncerValue(s string) string { return bouncerUnescaper.Replace(s) }
func escapeBouncerValue(s string) string   { return bouncerEscaper.Replace(s) }

var errNoBouncer = errors.New("this network is not a bouncer control connection (log in without a /network suffix to manage networks)")

// bouncerConn returns the network's live connection if it negotiated
// soju.im/bouncer-networks.
func (e *Engine) bouncerConn(network string) (IRCConn, error) {
	e.mu.RLock()
	conn := e.conns[network]
	e.mu.RUnlock()
	if conn == nil {
		return nil, errors.New("unknown network")
	}
	if !slices.Contains(conn.Caps(), capBouncerNetworks) {
		return nil, errNoBouncer
	}
	return conn, nil
}

// listBouncerNetworks asks the bouncer for its network list; a no-op on any
// connection that did not negotiate the capability.
func (e *Engine) listBouncerNetworks(network string) {
	if conn, err := e.bouncerConn(network); err == nil {
		conn.SendRaw("BOUNCER LISTNETWORKS")
	}
}

// BouncerAdd creates a network on the bouncer. host is required; the rest are
// optional (a zero port lets the bouncer pick).
func (e *Engine) BouncerAdd(network string, b BouncerNetwork, pass string) error {
	conn, err := e.bouncerConn(network)
	if err != nil {
		return err
	}
	if strings.TrimSpace(b.Host) == "" {
		return errors.New("a host is required")
	}
	attr := func(k, v string) string {
		if v == "" {
			return ""
		}
		return ";" + k + "=" + escapeBouncerValue(v)
	}
	attrs := "host=" + escapeBouncerValue(strings.TrimSpace(b.Host))
	attrs += attr("name", strings.TrimSpace(b.Name))
	if b.Port > 0 {
		attrs += ";port=" + strconv.Itoa(b.Port)
	}
	if b.TLS {
		attrs += ";tls=1"
	} else {
		attrs += ";tls=0"
	}
	attrs += attr("nickname", strings.TrimSpace(b.Nick))
	attrs += attr("pass", pass)
	if err := conn.SendRaw("BOUNCER ADDNETWORK " + attrs); err != nil {
		return err
	}
	// Without bouncer-networks-notify the server won't announce the new
	// network on its own; asking again is cheap and always correct.
	return conn.SendRaw("BOUNCER LISTNETWORKS")
}

// BouncerDelete removes one of the bouncer's networks, by the id it listed.
func (e *Engine) BouncerDelete(network, id string) error {
	conn, err := e.bouncerConn(network)
	if err != nil {
		return err
	}
	if _, ok := e.bouncerNetwork(network, id); !ok {
		return errors.New("unknown bouncer network")
	}
	if err := conn.SendRaw("BOUNCER DELNETWORK " + id); err != nil {
		return err
	}
	return conn.SendRaw("BOUNCER LISTNETWORKS")
}

func (e *Engine) bouncerNetwork(network, id string) (BouncerNetwork, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if n := e.user.Network(network); n != nil {
		for _, b := range n.Bouncer {
			if b.ID == id {
				return b, true
			}
		}
	}
	return BouncerNetwork{}, false
}

// BouncerOpen creates a stugan network bound to one of the bouncer's networks:
// it copies the control network's server, TLS and credentials, and logs in as
// "user/<network>" (keeping any "@client" suffix, which gives soju a separate
// session and backlog per client).
func (e *Engine) BouncerOpen(network, id string) error {
	b, ok := e.bouncerNetwork(network, id)
	if !ok {
		return errors.New("unknown bouncer network")
	}
	p, ok := e.NetworkConfig(network)
	if !ok {
		return errors.New("unknown network")
	}
	if b.Name == "" || p.SASLUser == "" {
		return errors.New("opening a bouncer network needs a SASL login on the control network and a named upstream")
	}
	user, client := p.SASLUser, ""
	if i := strings.IndexByte(user, '@'); i >= 0 {
		user, client = user[:i], user[i:]
	}
	if i := strings.IndexByte(user, '/'); i >= 0 {
		user = user[:i]
	}
	name := network + "-" + b.Name
	np := p
	np.ID, np.Name = name, name
	np.SASLUser = user + "/" + b.Name + client
	// Bound connections get their channels from the bouncer; the control
	// network's own settings must not leak into them.
	np.Channels, np.ChannelKeys, np.Perform, np.Monitor = nil, nil, nil, nil
	np.BufferOrder, np.Pos = nil, 0
	if err := e.AddNetworkLive(np); err != nil {
		if errors.Is(err, errNetworkExists) {
			return fmt.Errorf("%q is already open", name)
		}
		return err
	}
	return nil
}
