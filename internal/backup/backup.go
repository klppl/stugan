package backup

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Format represents an archive format.
type Format string

const (
	FormatTarGz  Format = "tar.gz"
	FormatZip    Format = "zip"
	FormatSQLite Format = "db"
)

// Manifest contains backup metadata.
type Manifest struct {
	Version     int       `json:"version"`
	App         string    `json:"app"`
	User        string    `json:"user"`
	ExportedAt  time.Time `json:"exported_at"`
	HasDatabase bool      `json:"has_database"`
	HasScripts  bool      `json:"has_scripts"`
	HasUploads  bool      `json:"has_uploads,omitempty"`
}

// CreateOptions specifies archive creation parameters.
type CreateOptions struct {
	Format     Format
	Manifest   Manifest
	DBPath     string
	ScriptsDir string
	UploadsDir string
}

// Create builds a portable archive stream and writes it to w.
func Create(ctx context.Context, w io.Writer, opts CreateOptions) error {
	switch opts.Format {
	case FormatZip:
		return createZip(ctx, w, opts)
	case FormatSQLite:
		return createRawDB(ctx, w, opts)
	default:
		return createTarGz(ctx, w, opts)
	}
}

func createRawDB(ctx context.Context, w io.Writer, opts CreateOptions) error {
	if opts.DBPath == "" {
		return errors.New("missing db path")
	}
	f, err := os.Open(opts.DBPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

func createTarGz(ctx context.Context, w io.Writer, opts CreateOptions) error {
	gw := gzip.NewWriter(w)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	// 1. Write manifest.json
	manifestBytes, err := json.MarshalIndent(opts.Manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := writeTarFile(tw, "manifest.json", manifestBytes, 0o644, opts.Manifest.ExportedAt); err != nil {
		return err
	}

	// 2. Write stugan.db
	if opts.DBPath != "" {
		if err := addFileToTar(tw, opts.DBPath, "stugan.db", opts.Manifest.ExportedAt); err != nil {
			return fmt.Errorf("add stugan.db: %w", err)
		}
	}

	// 3. Write scripts/
	if opts.ScriptsDir != "" {
		if err := addDirToTar(tw, opts.ScriptsDir, "scripts", opts.Manifest.ExportedAt); err != nil {
			return fmt.Errorf("add scripts: %w", err)
		}
	}

	// 4. Write uploads/
	if opts.UploadsDir != "" {
		if err := addDirToTar(tw, opts.UploadsDir, "uploads", opts.Manifest.ExportedAt); err != nil {
			return fmt.Errorf("add uploads: %w", err)
		}
	}

	return nil
}

func createZip(ctx context.Context, w io.Writer, opts CreateOptions) error {
	zw := zip.NewWriter(w)
	defer zw.Close()

	// 1. Write manifest.json
	manifestBytes, err := json.MarshalIndent(opts.Manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := writeZipFile(zw, "manifest.json", manifestBytes, opts.Manifest.ExportedAt); err != nil {
		return err
	}

	// 2. Write stugan.db
	if opts.DBPath != "" {
		if err := addFileToZip(zw, opts.DBPath, "stugan.db", opts.Manifest.ExportedAt); err != nil {
			return fmt.Errorf("add stugan.db: %w", err)
		}
	}

	// 3. Write scripts/
	if opts.ScriptsDir != "" {
		if err := addDirToZip(zw, opts.ScriptsDir, "scripts", opts.Manifest.ExportedAt); err != nil {
			return fmt.Errorf("add scripts: %w", err)
		}
	}

	// 4. Write uploads/
	if opts.UploadsDir != "" {
		if err := addDirToZip(zw, opts.UploadsDir, "uploads", opts.Manifest.ExportedAt); err != nil {
			return fmt.Errorf("add uploads: %w", err)
		}
	}

	return nil
}

func writeTarFile(tw *tar.Writer, name string, content []byte, mode int64, modTime time.Time) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    mode,
		Size:    int64(len(content)),
		ModTime: modTime,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(content)
	return err
}

func addFileToTar(tw *tar.Writer, srcPath, destName string, modTime time.Time) error {
	fi, err := os.Stat(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()

	hdr := &tar.Header{
		Name:    destName,
		Mode:    0o644,
		Size:    fi.Size(),
		ModTime: modTime,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func addDirToTar(tw *tar.Writer, srcDir, destPrefix string, modTime time.Time) error {
	if _, err := os.Stat(srcDir); err != nil {
		return nil
	}
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil || rel == "." {
			return nil
		}
		destName := filepath.ToSlash(filepath.Join(destPrefix, rel))
		if info.IsDir() {
			hdr := &tar.Header{
				Name:     destName + "/",
				Mode:     0o755,
				Typeflag: tar.TypeDir,
				ModTime:  modTime,
			}
			return tw.WriteHeader(hdr)
		}
		return addFileToTar(tw, path, destName, modTime)
	})
}

func writeZipFile(zw *zip.Writer, name string, content []byte, modTime time.Time) error {
	fh := &zip.FileHeader{
		Name:     name,
		Method:   zip.Deflate,
		Modified: modTime,
	}
	w, err := zw.CreateHeader(fh)
	if err != nil {
		return err
	}
	_, err = w.Write(content)
	return err
}

func addFileToZip(zw *zip.Writer, srcPath, destName string, modTime time.Time) error {
	fi, err := os.Stat(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()

	fh := &zip.FileHeader{
		Name:     destName,
		Method:   zip.Deflate,
		Modified: modTime,
	}
	fh.SetMode(fi.Mode())
	w, err := zw.CreateHeader(fh)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return err
}

func addDirToZip(zw *zip.Writer, srcDir, destPrefix string, modTime time.Time) error {
	if _, err := os.Stat(srcDir); err != nil {
		return nil
	}
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil || rel == "." {
			return nil
		}
		destName := filepath.ToSlash(filepath.Join(destPrefix, rel))
		if info.IsDir() {
			fh := &zip.FileHeader{
				Name:     destName + "/",
				Modified: modTime,
			}
			fh.SetMode(info.Mode())
			_, err := zw.CreateHeader(fh)
			return err
		}
		return addFileToZip(zw, path, destName, modTime)
	})
}

// ExtractedBackup holds the unpacked files from a backup.
type ExtractedBackup struct {
	Manifest   *Manifest
	DBPath     string
	ScriptsDir string
	UploadsDir string
}

// Extract detects the archive format and extracts it safely into destDir.
func Extract(r io.ReaderAt, size int64, destDir string) (*ExtractedBackup, error) {
	header := make([]byte, 512)
	n, _ := r.ReadAt(header, 0)
	header = header[:n]

	// Check magic
	if bytes.HasPrefix(header, []byte("SQLite format 3\x00")) {
		// Single DB file
		dbPath := filepath.Join(destDir, "stugan.db")
		f, err := os.Create(dbPath)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		sr := io.NewSectionReader(r, 0, size)
		if _, err := io.Copy(f, sr); err != nil {
			return nil, err
		}
		return &ExtractedBackup{
			Manifest: &Manifest{Version: 1, App: "stugan", HasDatabase: true},
			DBPath:   dbPath,
		}, nil
	}

	if bytes.HasPrefix(header, []byte("PK\x03\x04")) {
		return extractZip(r, size, destDir)
	}

	if bytes.HasPrefix(header, []byte("\x1f\x8b")) {
		return extractTarGz(r, size, destDir)
	}

	// Try tar.gz as fallback
	res, err := extractTarGz(r, size, destDir)
	if err == nil {
		return res, nil
	}
	return nil, errors.New("unrecognized archive format (expected .tar.gz, .zip, or .db)")
}

func extractZip(r io.ReaderAt, size int64, destDir string) (*ExtractedBackup, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("open zip reader: %w", err)
	}

	res := &ExtractedBackup{}

	for _, f := range zr.File {
		cleanName := filepath.Clean(filepath.FromSlash(f.Name))
		if strings.HasPrefix(cleanName, "..") || filepath.IsAbs(cleanName) {
			continue // Zip Slip protection
		}
		targetPath := filepath.Join(destDir, cleanName)

		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(targetPath, 0o755)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return nil, err
		}

		rc, err := f.Open()
		if err != nil {
			return nil, err
		}

		out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return nil, err
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return nil, err
		}

		if cleanName == "manifest.json" {
			b, _ := os.ReadFile(targetPath)
			var m Manifest
			if json.Unmarshal(b, &m) == nil {
				res.Manifest = &m
			}
		} else if cleanName == "stugan.db" {
			res.DBPath = targetPath
		}
	}

	scriptsPath := filepath.Join(destDir, "scripts")
	if fi, err := os.Stat(scriptsPath); err == nil && fi.IsDir() {
		res.ScriptsDir = scriptsPath
	}

	uploadsPath := filepath.Join(destDir, "uploads")
	if fi, err := os.Stat(uploadsPath); err == nil && fi.IsDir() {
		res.UploadsDir = uploadsPath
	}

	if res.DBPath == "" {
		// Look for any .db file in destDir
		entries, _ := os.ReadDir(destDir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".db") {
				res.DBPath = filepath.Join(destDir, e.Name())
				break
			}
		}
	}

	return res, nil
}

func extractTarGz(r io.ReaderAt, size int64, destDir string) (*ExtractedBackup, error) {
	sr := io.NewSectionReader(r, 0, size)
	gr, err := gzip.NewReader(sr)
	if err != nil {
		return nil, fmt.Errorf("open gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	res := &ExtractedBackup{}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar entry: %w", err)
		}

		cleanName := filepath.Clean(filepath.FromSlash(hdr.Name))
		if strings.HasPrefix(cleanName, "..") || filepath.IsAbs(cleanName) {
			continue // Tar slip protection
		}
		targetPath := filepath.Join(destDir, cleanName)

		if hdr.Typeflag == tar.TypeDir {
			_ = os.MkdirAll(targetPath, 0o755)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return nil, err
		}

		out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(out, tr)
		out.Close()
		if err != nil {
			return nil, err
		}

		if cleanName == "manifest.json" {
			b, _ := os.ReadFile(targetPath)
			var m Manifest
			if json.Unmarshal(b, &m) == nil {
				res.Manifest = &m
			}
		} else if cleanName == "stugan.db" {
			res.DBPath = targetPath
		}
	}

	scriptsPath := filepath.Join(destDir, "scripts")
	if fi, err := os.Stat(scriptsPath); err == nil && fi.IsDir() {
		res.ScriptsDir = scriptsPath
	}

	uploadsPath := filepath.Join(destDir, "uploads")
	if fi, err := os.Stat(uploadsPath); err == nil && fi.IsDir() {
		res.UploadsDir = uploadsPath
	}

	if res.DBPath == "" {
		entries, _ := os.ReadDir(destDir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".db") {
				res.DBPath = filepath.Join(destDir, e.Name())
				break
			}
		}
	}

	return res, nil
}
