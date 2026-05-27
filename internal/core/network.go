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
