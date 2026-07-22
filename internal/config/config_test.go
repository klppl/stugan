package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom empty dir: %v", err)
	}
	if cfg.Server.Listen != "127.0.0.1:8080" {
		t.Errorf("default listen = %q", cfg.Server.Listen)
	}
	if cfg.Log.Level != "info" || cfg.Log.Format != "text" {
		t.Errorf("default log = %+v", cfg.Log)
	}
	if cfg.ScriptsDir() != filepath.Join(dir, "scripts") {
		t.Errorf("ScriptsDir = %q", cfg.ScriptsDir())
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	doc := `
[server]
listen = "0.0.0.0:9000"

[log]
level = "debug"
format = "json"

[[networks]]
name = "libera"
addr = "irc.libera.chat:6697"
tls = true
nick = "stuganbot"
channels = ["#stugan"]
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.Server.Listen != "0.0.0.0:9000" {
		t.Errorf("listen = %q", cfg.Server.Listen)
	}
	if len(cfg.Networks) != 1 || cfg.Networks[0].Name != "libera" {
		t.Fatalf("networks = %+v", cfg.Networks)
	}
	if !cfg.Networks[0].TLS || cfg.Networks[0].Nick != "stuganbot" {
		t.Errorf("network = %+v", cfg.Networks[0])
	}
}

func TestValidateErrors(t *testing.T) {
	cases := map[string]string{
		"bad level":       "[log]\nlevel = \"loud\"\n",
		"bad format":      "[log]\nformat = \"xml\"\n",
		"network no name": "[[networks]]\naddr = \"x:6667\"\n",
		"network no addr": "[[networks]]\nname = \"n\"\n",
		"dup network":     "[[networks]]\nname = \"n\"\naddr = \"a:1\"\n[[networks]]\nname = \"n\"\naddr = \"b:1\"\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(doc), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFrom(dir); err == nil {
				t.Errorf("expected error for %q", name)
			}
		})
	}
}

func TestHomeResolution(t *testing.T) {
	t.Setenv("STUGAN_HOME", "/explicit/path")
	got, err := Home()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/explicit/path" {
		t.Errorf("Home with STUGAN_HOME = %q", got)
	}

	t.Setenv("STUGAN_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	got, err = Home()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join("/xdg", "stugan") {
		t.Errorf("Home with XDG = %q", got)
	}
}

func TestPluginSandbox(t *testing.T) {
	tru, fls := true, false
	tests := []struct {
		name  string
		users []UserConfig
		set   *bool
		want  bool
	}{
		{"single-user default on", nil, nil, true},
		{"single-user explicit off", nil, &fls, false},
		{"single-user explicit on", nil, &tru, true},
		{"multi-user forced on despite off", []UserConfig{{Name: "a", PasswordHash: "x"}}, &fls, true},
		{"multi-user default on", []UserConfig{{Name: "a", PasswordHash: "x"}}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{Users: tt.users}
			c.Plugins.Sandbox = tt.set
			if got := c.PluginSandbox(); got != tt.want {
				t.Errorf("PluginSandbox() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnsureDirs(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "stugan")
	cfg := &Config{
		home: home,
		Users: []UserConfig{
			{Name: "alice"},
		},
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs failed: %v", err)
	}

	for _, d := range []string{
		home,
		cfg.ScriptsDir(),
		cfg.DataDir(),
		filepath.Join(home, "users", "alice", "scripts"),
		filepath.Join(home, "users", "alice", "data"),
	} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("expected directory %s to exist, err: %v", d, err)
		}
	}
}

