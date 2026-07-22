// Package config loads and validates stugan's TOML configuration.
//
// The configuration, scripts, and data all live under a single home
// directory resolved by [Home]: $STUGAN_HOME if set, otherwise
// $XDG_CONFIG_HOME/stugan, otherwise ~/.config/stugan.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"
)

// Config is the root configuration document, loaded from config.toml.
//
// Field zero-values are filled with sensible defaults by [withDefaults]
// after decoding, so a missing or partial config file still yields a
// runnable daemon.
type Config struct {
	// home is the resolved root directory. Not part of the TOML document;
	// set during Load. Accessed via Home, ScriptsDir, DataDir.
	home string

	Server  ServerConfig  `toml:"server"`
	Log     LogConfig     `toml:"log"`
	Plugins PluginsConfig `toml:"plugins"`
	History HistoryConfig `toml:"history"`

	// Networks is the static list of IRC networks to connect on startup.
	// In multi-user mode this moves into per-user state; for now it is a
	// top-level list owned by the single implicit user.
	Networks []NetworkConfig `toml:"networks"`

	// Highlight configures which incoming messages are flagged.
	Highlight HighlightConfig `toml:"highlight"`

	// Aliases maps a command name to a template expanded with $1..$9, $*
	// (all args) and $N- (args from N onward). E.g. j = "/join #$1".
	Aliases map[string]string `toml:"aliases"`

	// Users enables multi-user mode. When non-empty, authentication is
	// required and each user owns their own networks. When empty, the
	// daemon runs single-user with the top-level Networks and no auth.
	Users []UserConfig `toml:"users"`

	// Auth tunes session behavior (only relevant when Users is set).
	Auth AuthConfig `toml:"auth"`

	// SSH exposes the terminal UI over SSH. Public-key auth only; disabled
	// unless [ssh].enabled is set.
	SSH SSHConfig `toml:"ssh"`
}

// SSHConfig configures the SSH-served terminal UI. Authentication is by
// public key only: a session's key must appear in the target user's
// authorized_keys (per-[[user]] in multi-user mode, or the top-level
// [ssh].authorized_keys for the implicit single user).
type SSHConfig struct {
	// Enabled turns the SSH listener on. Off by default: nothing binds an
	// SSH port unless this is set.
	Enabled bool `toml:"enabled"`
	// Listen is the address the SSH server binds, e.g. "0.0.0.0:2222".
	// Defaults to ":2222" when enabled and left empty.
	Listen string `toml:"listen"`
	// HostKey is the path to the server's private host key (OpenSSH PEM).
	// Generated (ed25519) on first run when the file is absent. Defaults to
	// <data>/ssh_host_ed25519_key.
	HostKey string `toml:"host_key"`
	// AuthorizedKeys lists the OpenSSH public keys ("ssh-ed25519 AAAA…")
	// allowed to log in as the implicit "default" user in single-user mode.
	// Ignored in multi-user mode, where each [[users]] block carries its own
	// authorized_keys.
	AuthorizedKeys []string `toml:"authorized_keys"`
}

// HistoryConfig controls message-history retention.
type HistoryConfig struct {
	// RetentionDays prunes messages older than this many days, hourly,
	// from every user's history (search index included). 0 (the default)
	// keeps history forever.
	RetentionDays int `toml:"retention_days"`
}

// UserConfig is one account in multi-user mode.
type UserConfig struct {
	Name         string          `toml:"name"`
	PasswordHash string          `toml:"password_hash"` // bcrypt; see `stugan -hashpw`
	Networks     []NetworkConfig `toml:"networks"`
	// AuthorizedKeys lists the OpenSSH public keys allowed to open an SSH TUI
	// session as this user (see [ssh]). Empty means this user cannot log in
	// over SSH.
	AuthorizedKeys []string `toml:"authorized_keys"`
}

// AuthConfig tunes authentication.
type AuthConfig struct {
	// SessionHours is the session lifetime; 0 → 30 days.
	SessionHours int `toml:"session_hours"`
}

// AuthEnabled reports whether multi-user authentication is in effect.
func (c *Config) AuthEnabled() bool { return len(c.Users) > 0 }

// PluginsEnabled reports whether the Lua plugin host should run. It defaults
// to true (the documented default) when [plugins].enabled is omitted, while
// still honouring an explicit `enabled = false`.
func (c *Config) PluginsEnabled() bool {
	return c.Plugins.Enabled == nil || *c.Plugins.Enabled
}

// PluginSandbox reports whether the Lua stdlib should be restricted for
// plugins. Multi-user mode is always sandboxed: tenants are mutually
// untrusted and share the daemon process, so a script must never reach
// os/io. Single-user mode also defaults to sandboxed; the operator may opt
// out with an explicit `sandbox = false` since the scripts are their own
// code on their own machine.
func (c *Config) PluginSandbox() bool {
	if c.AuthEnabled() {
		return true
	}
	if c.Plugins.Sandbox == nil {
		return true
	}
	return *c.Plugins.Sandbox
}

// EffectiveUsers returns the users to run: the configured accounts, or a
// single implicit "default" user owning the top-level networks.
func (c *Config) EffectiveUsers() []UserConfig {
	if len(c.Users) > 0 {
		return c.Users
	}
	return []UserConfig{{
		Name:           "default",
		Networks:       c.Networks,
		AuthorizedKeys: c.SSH.AuthorizedKeys,
	}}
}

// SSHHostKeyPath returns the configured host-key path, or the default under
// the data directory when unset.
func (c *Config) SSHHostKeyPath() string {
	if c.SSH.HostKey != "" {
		return c.SSH.HostKey
	}
	return filepath.Join(c.DataDir(), "ssh_host_ed25519_key")
}

// SSHListen returns the SSH listen address, defaulting to ":2222".
func (c *Config) SSHListen() string {
	if c.SSH.Listen != "" {
		return c.SSH.Listen
	}
	return ":2222"
}

// HighlightConfig holds case-insensitive regex highlight rules. A nick
// mention always highlights; Exceptions suppress a would-be highlight.
type HighlightConfig struct {
	Patterns   []string `toml:"patterns"`
	Exceptions []string `toml:"exceptions"`
}

// ServerConfig controls the HTTP/WebSocket listener.
type ServerConfig struct {
	// Listen is the address the HTTP server binds, e.g. "127.0.0.1:8080".
	Listen string `toml:"listen"`
	// PublicURL is the externally reachable base URL, used for absolute
	// links in link previews, uploads, and web-push payloads. Optional.
	PublicURL string `toml:"public_url"`
	// StaticDir is the directory of the built Vue client served at /.
	// Defaults to "client/dist" (relative to the working directory).
	StaticDir string `toml:"static_dir"`
	// OriginPatterns authorizes WebSocket origins (path.Match patterns).
	// Empty falls back to localhost variants.
	OriginPatterns []string `toml:"origin_patterns"`
	// TrustedProxies lists CIDRs (or bare IPs) of reverse proxies in front
	// of the daemon. When a request's direct peer matches, the real client
	// IP used for auth rate-limiting is taken from X-Forwarded-For instead
	// of the proxy's address. Leave empty when the daemon is directly
	// exposed; an untrusted peer's forwarded headers are always ignored.
	TrustedProxies []string `toml:"trusted_proxies"`
}

// LogConfig controls structured logging.
type LogConfig struct {
	// Level is one of "debug", "info", "warn", "error".
	Level string `toml:"level"`
	// Format is "text" (human, default) or "json".
	Format string `toml:"format"`
}

// PluginsConfig controls the Lua plugin host.
type PluginsConfig struct {
	// Enabled toggles the plugin host entirely. A nil pointer (the field
	// omitted from config) means "use the default" — see PluginsEnabled.
	Enabled *bool `toml:"enabled"`
	// Sandbox restricts the Lua stdlib exposed to scripts. A nil pointer
	// (the field omitted) means "use the default" — see PluginSandbox,
	// which defaults to sandboxed (true). Multi-user mode is always
	// sandboxed regardless of this value.
	// TODO(multi-user): back the sandbox with a WASM host.
	Sandbox *bool `toml:"sandbox"`
	// Settings holds arbitrary per-plugin configuration tables, keyed by
	// script name. Exposed read-only to scripts via stugan.config.
	Settings map[string]map[string]any `toml:"settings"`
}

// NetworkConfig describes one IRC network connection.
type NetworkConfig struct {
	// Name is the unique identifier shown in the UI, e.g. "libera".
	Name string `toml:"name"`
	// Addr is host:port, e.g. "irc.libera.chat:6697".
	Addr string `toml:"addr"`
	// Fallbacks are additional host:port servers tried in order when the
	// primary Addr fails to connect.
	Fallbacks []string `toml:"fallbacks"`
	// TLS enables an encrypted connection.
	TLS bool `toml:"tls"`
	// Insecure skips TLS certificate verification, for self-signed or
	// LAN servers. Only meaningful with TLS; never enable it against a
	// server reached over the public internet.
	Insecure bool `toml:"insecure"`
	// Nick, User, and Realname identify the client to the server.
	Nick     string `toml:"nick"`
	User     string `toml:"user"`
	Realname string `toml:"realname"`
	// SASL PLAIN credentials.
	SASLUser string `toml:"sasl_user"`
	SASLPass string `toml:"sasl_pass"`
	// SASLExternal authenticates with SASL EXTERNAL (CertFP) instead of
	// PLAIN; requires CertFile and TLS.
	SASLExternal bool `toml:"sasl_external"`
	// CertFile is a path to a PEM file holding the client certificate and
	// private key (concatenated). Presented during the TLS handshake for
	// CertFP / SASL EXTERNAL. Read into the network's CertPEM on first run.
	CertFile string `toml:"cert_file"`
	// ServerPass is the connection password (IRC PASS), for bouncers
	// (ZNC/soju) and password-gated servers.
	ServerPass string `toml:"server_pass"`
	// Perform is a list of command lines run after registration on every
	// (re)connect, e.g. "/msg NickServ IDENTIFY hunter2" or
	// "/join #private secretkey".
	Perform []string `toml:"perform"`
	// Channels to auto-join after connect/registration.
	Channels []string `toml:"channels"`
	// Monitor is the friends list watched via IRCv3 MONITOR (online/offline
	// notifications). Editable from the GUI thereafter.
	Monitor []string `toml:"monitor"`
	// Connect, when false, leaves the network configured but idle.
	Connect bool `toml:"connect"`
}

// Load resolves the home directory, reads config.toml from it (tolerating
// a missing file), applies defaults, and validates the result.
func Load() (*Config, error) {
	home, err := Home()
	if err != nil {
		return nil, err
	}
	return LoadFrom(home)
}

// LoadFrom loads configuration rooted at an explicit home directory.
// Useful for tests. A missing config.toml is not an error; defaults apply.
func LoadFrom(home string) (*Config, error) {
	cfg := &Config{home: home}

	path := filepath.Join(home, "config.toml")
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// No file: run on defaults.
	case err != nil:
		return nil, fmt.Errorf("read config %s: %w", path, err)
	default:
		if err := toml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}

	cfg.home = home
	cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// withDefaults fills unset fields with defaults.
func (c *Config) withDefaults() {
	if c.Server.Listen == "" {
		c.Server.Listen = "127.0.0.1:8080"
	}
	if c.Server.StaticDir == "" {
		c.Server.StaticDir = "client/dist"
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Log.Format == "" {
		c.Log.Format = "text"
	}
}

// validate reports configuration errors that should stop startup.
func (c *Config) validate() error {
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: invalid log.level %q", c.Log.Level)
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		return fmt.Errorf("config: invalid log.format %q", c.Log.Format)
	}
	if err := validateNetworks(c.Networks, "networks"); err != nil {
		return err
	}
	seenUser := make(map[string]bool, len(c.Users))
	for i, u := range c.Users {
		if u.Name == "" {
			return fmt.Errorf("config: users[%d] missing name", i)
		}
		if seenUser[u.Name] {
			return fmt.Errorf("config: duplicate user name %q", u.Name)
		}
		seenUser[u.Name] = true
		if u.PasswordHash == "" {
			return fmt.Errorf("config: user %q missing password_hash (generate with `stugan -hashpw`)", u.Name)
		}
		if err := validateNetworks(u.Networks, "user "+u.Name+" networks"); err != nil {
			return err
		}
	}
	return nil
}

// validateNetworks checks a network list for unique names and addresses.
func validateNetworks(nets []NetworkConfig, ctx string) error {
	seen := make(map[string]bool, len(nets))
	for i, n := range nets {
		if n.Name == "" {
			return fmt.Errorf("config: %s[%d] missing name", ctx, i)
		}
		if seen[n.Name] {
			return fmt.Errorf("config: %s: duplicate network name %q", ctx, n.Name)
		}
		seen[n.Name] = true
		if n.Addr == "" {
			return fmt.Errorf("config: %s: network %q missing addr", ctx, n.Name)
		}
	}
	return nil
}

// Home returns the resolved configuration root directory without reading
// any file. Resolution order: $STUGAN_HOME, $XDG_CONFIG_HOME/stugan,
// ~/.config/stugan.
func Home() (string, error) {
	if h := os.Getenv("STUGAN_HOME"); h != "" {
		return h, nil
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "stugan"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", "stugan"), nil
}

// Home returns the resolved root directory for this config.
func (c *Config) Home() string { return c.home }

// ScriptsDir is where Lua plugins live ($home/scripts).
func (c *Config) ScriptsDir() string { return filepath.Join(c.home, "scripts") }

// DataDir is where the SQLite database and uploads live ($home/data).
func (c *Config) DataDir() string { return filepath.Join(c.home, "data") }

// EnsureDirs creates the home, scripts, and data directories if absent.
func (c *Config) EnsureDirs() error {
	for _, dir := range []string{c.home, c.ScriptsDir(), c.DataDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create dir %s: %w", dir, err)
		}
	}
	return nil
}
