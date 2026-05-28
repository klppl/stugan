package core

// NetworkParams fully describes a network connection. It is the unit added
// at runtime (from the GUI) and persisted, so it carries everything needed
// to dial — unlike NetworkSpec, which only seeds display state.
type NetworkParams struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Addr     string   `json:"addr"`
	TLS      bool     `json:"tls"`
	Nick     string   `json:"nick"`
	User     string   `json:"user"`
	Realname string   `json:"realname"`
	SASLUser string   `json:"sasl_user"`
	SASLPass string   `json:"sasl_pass"`
	Channels []string `json:"channels"`
	// ServerPass is the connection password (IRC PASS), used by bouncers
	// (ZNC/soju) and password-gated servers. Empty disables it.
	ServerPass string `json:"server_pass,omitempty"`
	// Perform is a list of command lines run after registration, on every
	// (re)connect. Each is processed like user input (alias + /command +
	// plugin hooks), e.g. "/msg NickServ IDENTIFY hunter2" or
	// "/join #private secretkey". Use it to identify, ghost, set modes, or
	// join keyed channels on networks without SASL.
	Perform []string `json:"perform,omitempty"`
	// SASLExternal authenticates with SASL EXTERNAL (CertFP) instead of
	// PLAIN. Requires a client certificate (CertPEM) and TLS.
	SASLExternal bool `json:"sasl_external,omitempty"`
	// CertPEM is a client certificate (cert and private key concatenated in
	// PEM form) presented during the TLS handshake. Enables CertFP and is
	// required for SASLExternal. Empty disables the client certificate.
	CertPEM string `json:"cert_pem,omitempty"`
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
