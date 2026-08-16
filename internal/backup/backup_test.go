package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupTarGzRoundtrip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// 1. Create dummy files
	dbPath := filepath.Join(dir, "source.db")
	if err := os.WriteFile(dbPath, []byte("SQLite format 3\x00 dummy db content"), 0o644); err != nil {
		t.Fatal(err)
	}

	scriptsDir := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scriptsDir, "hello.lua"), []byte("print('hello')"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 2. Create tar.gz archive
	var buf bytes.Buffer
	manifest := Manifest{
		Version:     1,
		App:         "stugan",
		User:        "alice",
		ExportedAt:  time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
		HasDatabase: true,
		HasScripts:  true,
	}

	err := Create(ctx, &buf, CreateOptions{
		Format:     FormatTarGz,
		Manifest:   manifest,
		DBPath:     dbPath,
		ScriptsDir: scriptsDir,
	})
	if err != nil {
		t.Fatalf("Create TarGz failed: %v", err)
	}

	// 3. Extract tar.gz archive
	extractDir := filepath.Join(dir, "extracted")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatal(err)
	}

	r := bytes.NewReader(buf.Bytes())
	res, err := Extract(r, int64(buf.Len()), extractDir)
	if err != nil {
		t.Fatalf("Extract TarGz failed: %v", err)
	}

	if res.Manifest == nil || res.Manifest.User != "alice" {
		t.Fatalf("Manifest user = %v, want alice", res.Manifest)
	}
	if res.DBPath == "" {
		t.Fatalf("Expected extracted DBPath")
	}
	dbContent, err := os.ReadFile(res.DBPath)
	if err != nil || string(dbContent) != "SQLite format 3\x00 dummy db content" {
		t.Fatalf("DB content mismatch: %s, %v", string(dbContent), err)
	}

	scriptContent, err := os.ReadFile(filepath.Join(res.ScriptsDir, "hello.lua"))
	if err != nil || string(scriptContent) != "print('hello')" {
		t.Fatalf("Script content mismatch: %s, %v", string(scriptContent), err)
	}
}

func TestBackupZipRoundtrip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	dbPath := filepath.Join(dir, "source.db")
	if err := os.WriteFile(dbPath, []byte("SQLite format 3\x00 dummy db content"), 0o644); err != nil {
		t.Fatal(err)
	}

	scriptsDir := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scriptsDir, "test.lua"), []byte("return true"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	manifest := Manifest{
		Version:     1,
		App:         "stugan",
		User:        "bob",
		ExportedAt:  time.Now(),
		HasDatabase: true,
		HasScripts:  true,
	}

	err := Create(ctx, &buf, CreateOptions{
		Format:     FormatZip,
		Manifest:   manifest,
		DBPath:     dbPath,
		ScriptsDir: scriptsDir,
	})
	if err != nil {
		t.Fatalf("Create Zip failed: %v", err)
	}

	extractDir := filepath.Join(dir, "extracted_zip")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatal(err)
	}

	r := bytes.NewReader(buf.Bytes())
	res, err := Extract(r, int64(buf.Len()), extractDir)
	if err != nil {
		t.Fatalf("Extract Zip failed: %v", err)
	}

	if res.Manifest == nil || res.Manifest.User != "bob" {
		t.Fatalf("Manifest user = %v, want bob", res.Manifest)
	}
	if res.DBPath == "" {
		t.Fatalf("Expected extracted DBPath")
	}
	dbContent, _ := os.ReadFile(res.DBPath)
	if string(dbContent) != "SQLite format 3\x00 dummy db content" {
		t.Fatalf("DB content mismatch: %s", string(dbContent))
	}
}

func TestBackupRawDB(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	dbPath := filepath.Join(dir, "source.db")
	if err := os.WriteFile(dbPath, []byte("SQLite format 3\x00 single db"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	err := Create(ctx, &buf, CreateOptions{
		Format: FormatSQLite,
		DBPath: dbPath,
	})
	if err != nil {
		t.Fatalf("Create RawDB failed: %v", err)
	}

	extractDir := filepath.Join(dir, "extracted_raw")
	_ = os.MkdirAll(extractDir, 0o755)

	r := bytes.NewReader(buf.Bytes())
	res, err := Extract(r, int64(buf.Len()), extractDir)
	if err != nil {
		t.Fatalf("Extract RawDB failed: %v", err)
	}

	if res.DBPath == "" {
		t.Fatalf("Expected extracted DBPath")
	}
	dbContent, _ := os.ReadFile(res.DBPath)
	if string(dbContent) != "SQLite format 3\x00 single db" {
		t.Fatalf("DB content mismatch: %s", string(dbContent))
	}
}
