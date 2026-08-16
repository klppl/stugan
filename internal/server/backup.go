package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klippelism/stugan/internal/backup"
	"github.com/klippelism/stugan/internal/proto"
	"github.com/klippelism/stugan/internal/store"
)

const maxImportSize = 500 * 1024 * 1024 // 500 MB max backup import

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID, ok := s.userOf(r)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	tenant, ok := s.hub.Tenant(userID)
	if !ok || tenant == nil || tenant.Store == nil {
		http.Error(w, "database storage not available for user", http.StatusServiceUnavailable)
		return
	}

	rawFormat := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	format := backup.FormatTarGz
	ext := "tar.gz"
	contentType := "application/gzip"

	switch rawFormat {
	case "zip":
		format = backup.FormatZip
		ext = "zip"
		contentType = "application/zip"
	case "db", "sqlite", "sqlite3":
		format = backup.FormatSQLite
		ext = "db"
		contentType = "application/vnd.sqlite3"
	default:
		format = backup.FormatTarGz
		ext = "tar.gz"
		contentType = "application/gzip"
	}

	// 1. Create a clean vacuumed backup snapshot in a temp file
	tempDir, err := os.MkdirTemp("", "stugan-export-*")
	if err != nil {
		s.log.Error("create temp dir for export", "user", userID, "err", err)
		http.Error(w, "failed to create export", http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tempDir)

	tempDBPath := filepath.Join(tempDir, "stugan.db")
	if err := tenant.Store.Backup(r.Context(), tempDBPath); err != nil {
		s.log.Error("backup store for export", "user", userID, "err", err)
		http.Error(w, "failed to snapshot database", http.StatusInternalServerError)
		return
	}

	// 2. Set download headers
	now := time.Now().UTC()
	filename := fmt.Sprintf("stugan-backup-%s-%s.%s", userID, now.Format("20060102-150405"), ext)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	// 3. Package and stream directly
	manifest := backup.Manifest{
		Version:     1,
		App:         "stugan",
		User:        userID,
		ExportedAt:  now,
		HasDatabase: true,
		HasScripts:  tenant.ScriptsDir != "",
	}

	err = backup.Create(r.Context(), w, backup.CreateOptions{
		Format:     format,
		Manifest:   manifest,
		DBPath:     tempDBPath,
		ScriptsDir: tenant.ScriptsDir,
	})
	if err != nil {
		s.log.Error("stream export archive", "user", userID, "err", err)
	}
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID, ok := s.userOf(r)
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	tenant, ok := s.hub.Tenant(userID)
	if !ok || tenant == nil || tenant.Store == nil {
		http.Error(w, "database storage not available for user", http.StatusServiceUnavailable)
		return
	}

	modeStr := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	importMode := store.ImportModeReplace
	if modeStr == "merge" {
		importMode = store.ImportModeMerge
	}

	// Read upload file from multipart or raw body
	tempDir, err := os.MkdirTemp("", "stugan-import-*")
	if err != nil {
		s.log.Error("create temp dir for import", "user", userID, "err", err)
		http.Error(w, "failed to prepare import", http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tempDir)

	tempUploadPath := filepath.Join(tempDir, "upload.archive")
	tempFile, err := os.OpenFile(tempUploadPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		http.Error(w, "failed to write upload file", http.StatusInternalServerError)
		return
	}

	var written int64
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, maxImportSize)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			tempFile.Close()
			http.Error(w, fmt.Sprintf("invalid upload payload: %v", err), http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			file, _, err = r.FormFile("upload")
			if err != nil {
				file, _, err = r.FormFile("archive")
			}
		}
		if err != nil {
			tempFile.Close()
			http.Error(w, "missing file field in upload", http.StatusBadRequest)
			return
		}
		defer file.Close()
		written, err = io.Copy(tempFile, file)
		if err != nil {
			tempFile.Close()
			http.Error(w, "failed to save uploaded file", http.StatusInternalServerError)
			return
		}
		if formMode := r.FormValue("mode"); formMode != "" {
			if strings.ToLower(formMode) == "merge" {
				importMode = store.ImportModeMerge
			}
		}
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, maxImportSize)
		written, err = io.Copy(tempFile, r.Body)
		if err != nil {
			tempFile.Close()
			http.Error(w, "failed to save uploaded body", http.StatusInternalServerError)
			return
		}
	}

	if written == 0 {
		tempFile.Close()
		http.Error(w, "empty file uploaded", http.StatusBadRequest)
		return
	}

	// 1. Extract and inspect backup
	extractDir := filepath.Join(tempDir, "extracted")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		tempFile.Close()
		http.Error(w, "failed to create extract directory", http.StatusInternalServerError)
		return
	}

	extracted, err := backup.Extract(tempFile, written, extractDir)
	tempFile.Close()
	if err != nil {
		s.log.Warn("failed to extract import archive", "user", userID, "err", err)
		http.Error(w, fmt.Sprintf("invalid archive: %v", err), http.StatusBadRequest)
		return
	}

	if extracted.DBPath == "" {
		http.Error(w, "no database found in archive", http.StatusBadRequest)
		return
	}

	// 2. Import database into store
	if err := tenant.Store.Import(r.Context(), extracted.DBPath, importMode); err != nil {
		s.log.Error("import database into store", "user", userID, "mode", importMode, "err", err)
		http.Error(w, fmt.Sprintf("failed to import database: %v", err), http.StatusInternalServerError)
		return
	}

	// 3. Restore scripts if present in backup and user scriptsDir exists
	if extracted.ScriptsDir != "" && tenant.ScriptsDir != "" {
		_ = os.MkdirAll(tenant.ScriptsDir, 0o755)
		_ = filepath.Walk(extracted.ScriptsDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(extracted.ScriptsDir, path)
			if err != nil || rel == "." {
				return nil
			}
			destFile := filepath.Join(tenant.ScriptsDir, rel)
			_ = os.MkdirAll(filepath.Dir(destFile), 0o755)
			data, err := os.ReadFile(path)
			if err == nil {
				_ = os.WriteFile(destFile, data, 0o644)
			}
			return nil
		})
	}

	// 4. Hot-resync: notify user sessions
	s.resyncUser(r.Context(), userID, tenant)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"message": "Backup imported successfully",
		"mode":    string(importMode),
	})
}

// resyncUser reloads networks and sends fresh init snapshots after a database import.
func (s *Server) resyncUser(ctx context.Context, userID string, tenant *Tenant) {
	if tenant == nil || tenant.Engine == nil {
		return
	}

	// If we have connected clients, broadcast updated state
	s.mu.Lock()
	clients := make([]*client, 0, len(s.clients[userID]))
	for c := range s.clients[userID] {
		clients = append(clients, c)
	}
	s.mu.Unlock()

	if len(clients) == 0 {
		return
	}

	// Build fresh init state
	state := toInitState(tenant.Engine.Snapshot())
	if tenant.History != nil {
		if counts, err := tenant.History.UnreadCounts(ctx); err == nil {
			applyUnread(&state, counts)
		}
		if markers, err := tenant.History.ReadMarkers(ctx); err == nil {
			state.ReadMarkers = markers
		}
	}
	patterns, exceptions := tenant.Engine.HighlightRules()
	state.Highlight = proto.HighlightRules{Patterns: patterns, Exceptions: exceptions}
	state.Aliases = proto.AliasTable{Aliases: tenant.Engine.Aliases()}
	state.Muted = loadMuted(tenant)
	state.Settings = loadSettings(tenant)
	state.Drafts = loadDrafts(tenant)

	if initFrame, err := proto.Frame(proto.TInit, state); err == nil {
		for _, c := range clients {
			c.trySend(initFrame)
		}
	}
}
