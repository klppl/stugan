package core

import "maps"

// NetworkParams fully describes a network connection. It is the unit added
// at runtime (from the GUI) and persisted, so it carries everything needed
// to dial — unlike NetworkSpec, which only seeds display state.
type NetworkParams struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Addr string `json:"addr"`
	// Fallbacks are additional host:port servers tried in order when the
	// primary Addr fails to connect. Empty for a single-server network.
	Fallbacks []string `json:"fallbacks,omitempty"`
	TLS       bool     `json:"tls"`
	// Insecure skips TLS certificate verification (self-signed / LAN
	// servers). Only meaningful with TLS.
	Insecure bool     `json:"insecure,omitempty"`
	Nick     string   `json:"nick"`
	User     string   `json:"user"`
	Realname string   `json:"realname"`
	SASLUser string   `json:"sasl_user"`
	SASLPass string   `json:"sasl_pass"`
	Channels []string `json:"channels"`
	// Monitor is the friends list: lowercased nicks watched via IRCv3 MONITOR,
	// re-armed on every (re)connect. Empty when the network has no friends.
	Monitor []string `json:"monitor,omitempty"`
	// ChannelKeys maps a channel name (as stored in Channels) to its join key
	// (+k password), so keyed channels rejoin automatically on (re)connect.
	// Channels without a key are simply absent from the map.
	ChannelKeys map[string]string `json:"channel_keys,omitempty"`
	// ServerPass is the connection password (IRC PASS), used by bouncers
	// (ZNC/soju) and password-gated servers. Empty disables it.
	ServerPass string `json:"server_pass,omitempty"`
	// Perform is a list of command lines run after registration, on every
	// (re)connect. Each is processed like user input (alias + /command +
	// plugin hooks), e.g. "/msg NickServ IDENTIFY hunter2" or
	// "/join #private secretkey". Use it to identify, ghost, set modes, or
	// join keyed channels on networks without SASL. Commands run one second
	// apart and finish before configured channels auto-join. Variables describe
	// the connection at execution time; see docs/config.md for supported values.
	Perform []string `json:"perform,omitempty"`
	// SASLExternal authenticates with SASL EXTERNAL (CertFP) instead of
	// PLAIN. Requires a client certificate (CertPEM) and TLS.
	SASLExternal bool `json:"sasl_external,omitempty"`
	// CertPEM is a client certificate (cert and private key concatenated in
	// PEM form) presented during the TLS handshake. Enables CertFP and is
	// required for SASLExternal. Empty disables the client certificate.
	CertPEM string `json:"cert_pem,omitempty"`
	// Pos is the network's manual sort position in the sidebar (lower first).
	// Set by drag-and-drop reordering; networks load ordered by it on boot.
	Pos int `json:"pos,omitempty"`
	// BufferOrder is the user's manual buffer order within this network, as
	// lowercased display names (channels and queries). Buffers absent from the
	// list — freshly joined channels, the status buffer — sort to the end.
	// Applied to snapshots; see Engine.orderChannels.
	BufferOrder []string `json:"buffer_order,omitempty"`
	// JoinHoldTimeout is the maximum duration in seconds autojoin will wait when
	// held by a plugin before auto-releasing. Defaults to 45 seconds if <= 0.
	JoinHoldTimeout int `json:"join_hold_timeout,omitempty"`
}

// clone returns a deep copy of p, duplicating its slice fields so the copy can
// be handed off (e.g. to the store) without aliasing the live engine state.
func (p NetworkParams) clone() NetworkParams {
	c := p
	if p.Channels != nil {
		c.Channels = append([]string(nil), p.Channels...)
	}
	if p.Fallbacks != nil {
		c.Fallbacks = append([]string(nil), p.Fallbacks...)
	}
	if p.Monitor != nil {
		c.Monitor = append([]string(nil), p.Monitor...)
	}
	if p.Perform != nil {
		c.Perform = append([]string(nil), p.Perform...)
	}
	if p.ChannelKeys != nil {
		c.ChannelKeys = maps.Clone(p.ChannelKeys)
	}
	if p.BufferOrder != nil {
		c.BufferOrder = append([]string(nil), p.BufferOrder...)
	}
	return c
}

// Connector builds an IRCConn from params, delivering inbound events to the
// handler. It is implemented in the composition root (wrapping internal/irc)
// so core can create connections at runtime without importing the IRC
// library.
type Connector interface {
	Dial(p NetworkParams, h ConnHandler) (IRCConn, error)
}

// NetworkStore persists a user's networks so GUI-managed networks survive
// restarts. Implemented by internal/store.
type NetworkStore interface {
	SaveNetwork(p NetworkParams) error
	DeleteNetwork(id string) error
}
