package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUploadsConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	tomlData := `
[server]
listen = "127.0.0.1:8080"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(tomlData), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if cfg.Uploads.Mode != "local" {
		t.Errorf("got mode %q, want \"local\"", cfg.Uploads.Mode)
	}
	if cfg.Uploads.FieldName != "file" {
		t.Errorf("got field_name %q, want \"file\"", cfg.Uploads.FieldName)
	}
}

func TestNetworkConnectDefaultsToTrue(t *testing.T) {
	cases := []struct {
		name    string
		connect string
		want    bool
	}{
		{name: "omitted", want: true},
		{name: "explicit true", connect: "connect = true", want: true},
		{name: "explicit false", connect: "connect = false", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tomlData := `
[[networks]]
name = "libera"
addr = "irc.libera.chat:6697"
` + tc.connect + "\n"
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(tomlData), 0o644); err != nil {
				t.Fatal(err)
			}

			cfg, err := LoadFrom(dir)
			if err != nil {
				t.Fatalf("LoadFrom: %v", err)
			}
			if got := cfg.Networks[0].ConnectEnabled(); got != tc.want {
				t.Errorf("ConnectEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUploadsConfigCustomValid(t *testing.T) {
	dir := t.TempDir()
	tomlData := `
[uploads]
mode = "custom"
url = "https://x0.at"
field_name = "file"
response_field = "url"

[uploads.headers]
Authorization = "Bearer secret"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(tomlData), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if cfg.Uploads.Mode != "custom" {
		t.Errorf("got mode %q, want \"custom\"", cfg.Uploads.Mode)
	}
	if cfg.Uploads.URL != "https://x0.at" {
		t.Errorf("got url %q, want \"https://x0.at\"", cfg.Uploads.URL)
	}
	if cfg.Uploads.FieldName != "file" {
		t.Errorf("got field_name %q, want \"file\"", cfg.Uploads.FieldName)
	}
	if cfg.Uploads.Headers["Authorization"] != "Bearer secret" {
		t.Errorf("got header Authorization %q, want \"Bearer secret\"", cfg.Uploads.Headers["Authorization"])
	}
	if cfg.Uploads.ResponseField != "url" {
		t.Errorf("got response_field %q, want \"url\"", cfg.Uploads.ResponseField)
	}
}

func TestUploadsConfigCustomInvalid(t *testing.T) {
	cases := []struct {
		name string
		toml string
	}{
		{
			name: "invalid mode",
			toml: `[uploads]
mode = "ftp"
url = "https://example.com"
`,
		},
		{
			name: "missing url in custom mode",
			toml: `[uploads]
mode = "custom"
`,
		},
		{
			name: "invalid url scheme",
			toml: `[uploads]
mode = "custom"
url = "ftp://example.com"
`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(c.toml), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFrom(dir); err == nil {
				t.Errorf("%s: expected error, got nil", c.name)
			}
		})
	}
}
