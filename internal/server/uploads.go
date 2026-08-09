package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Upload retention: files are kept for a minimum of 3 and a maximum of 7
// days. How long depends on the file's size — larger files are deleted
// earlier than small ones, skewed in favour of small files:
//
//	MIN_AGE + (MAX_AGE - MIN_AGE) * (1 - FILE_SIZE/MAX_SIZE)^2
const (
	minUploadAge = 3 * 24 * time.Hour
	maxUploadAge = 7 * 24 * time.Hour
)

// uploadTTL returns how long a file of the given size is kept.
func (s *Server) uploadTTL(size int64) time.Duration {
	ratio := float64(size) / float64(s.maxUpload)
	if ratio > 1 {
		ratio = 1
	}
	if ratio < 0 {
		ratio = 0
	}
	return minUploadAge + time.Duration(float64(maxUploadAge-minUploadAge)*(1-ratio)*(1-ratio))
}

// uploadMetaDir is the subdirectory of uploadDir holding one sidecar JSON
// per stored file (ownership + original filename). It starts with a dot so
// noListFS refuses to serve anything inside it.
const uploadMetaDir = ".meta"

// uploadMeta is the sidecar record written next to each stored upload.
type uploadMeta struct {
	Owner    string    `json:"owner"`
	Name     string    `json:"name"` // original filename as uploaded
	Uploaded time.Time `json:"uploaded"`
	Size     int64     `json:"size,omitempty"`
	URL      string    `json:"url,omitempty"`
}

func (s *Server) uploadMetaPath(stored string) string {
	return filepath.Join(s.uploadDir, uploadMetaDir, stored+".json")
}

func (s *Server) writeUploadMeta(stored string, m uploadMeta) error {
	if err := os.MkdirAll(filepath.Join(s.uploadDir, uploadMetaDir), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(s.uploadMetaPath(stored), b, 0o644)
}

// handleUpload accepts a multipart file upload (field "file"), stores it
// under uploadDir with a random name, and returns its served URL.
// POST /api/upload
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		s.handleUploadDelete(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.maxUpload+1024)
	if err := r.ParseMultipartForm(s.maxUpload + 1024); err != nil {
		http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Read one byte past the cap so an over-limit file is rejected rather
	// than silently truncated (the body cap above leaves headroom for
	// multipart framing, so a file slightly over maxUpload still gets here).
	data, err := io.ReadAll(io.LimitReader(file, s.maxUpload+1))
	if err != nil {
		http.Error(w, "read failed", http.StatusInternalServerError)
		return
	}
	if int64(len(data)) > s.maxUpload {
		http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
		return
	}

	// Strip embedded metadata (EXIF/GPS, comments, text chunks) from images
	// before they hit disk. A photo straight off a phone otherwise leaks the
	// uploader's location and device. Recognised image formats that can't be
	// parsed cleanly are rejected rather than stored with metadata intact.
	data, err = stripImageMetadata(data)
	if err != nil {
		http.Error(w, "unprocessable image", http.StatusUnprocessableEntity)
		return
	}

	if s.uploads.Mode == "custom" {
		s.handleCustomUpload(w, r, hdr.Filename, data)
		return
	}

	if err := os.MkdirAll(s.uploadDir, 0o755); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	name := randomName() + safeExt(hdr.Filename)
	dst, err := os.Create(filepath.Join(s.uploadDir, name))
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	if _, err := dst.Write(data); err != nil {
		os.Remove(dst.Name())
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}

	// Record who uploaded it (for the per-user listing) and when. A failed
	// sidecar write doesn't fail the upload: the file is stored and served
	// fine, it just won't appear in the owner's list, and the sweep expires
	// it from its mtime regardless.
	user, _ := s.userOf(r) // requireUser already vetted the request
	now := time.Now().UTC()
	size := int64(len(data))
	if err := s.writeUploadMeta(name, uploadMeta{Owner: user, Name: hdr.Filename, Uploaded: now, Size: size}); err != nil {
		s.log.Warn("upload sidecar write failed", "file", name, "err", err)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"id":      name,
		"url":     "/uploads/" + name,
		"name":    hdr.Filename,
		"expires": now.Add(s.uploadTTL(size)).Format(time.RFC3339),
	})
}

func (s *Server) handleCustomUpload(w http.ResponseWriter, r *http.Request, filename string, data []byte) {
	var bodyBuf bytes.Buffer
	mw := multipart.NewWriter(&bodyBuf)
	fieldName := s.uploads.FieldName
	if fieldName == "" {
		fieldName = "file"
	}
	fw, err := mw.CreateFormFile(fieldName, filename)
	if err != nil {
		http.Error(w, "failed to build upload request", http.StatusInternalServerError)
		return
	}
	if _, err := fw.Write(data); err != nil {
		http.Error(w, "failed to write upload payload", http.StatusInternalServerError)
		return
	}
	mw.Close()

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.uploads.URL, &bodyBuf)
	if err != nil {
		http.Error(w, "failed to create custom upload request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, v := range s.uploads.Headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		s.log.Error("custom upload request failed", "url", s.uploads.URL, "err", err)
		http.Error(w, "custom upload request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.log.Warn("custom upload host returned error", "status", resp.StatusCode, "url", s.uploads.URL)
		http.Error(w, fmt.Sprintf("custom upload server error (%d)", resp.StatusCode), http.StatusBadGateway)
		return
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		http.Error(w, "failed to read custom upload response", http.StatusInternalServerError)
		return
	}

	uploadURL, err := parseCustomUploadResponse(respBody, s.uploads.ResponseField)
	if err != nil || uploadURL == "" {
		s.log.Error("parse custom upload response failed", "err", err, "body", string(respBody))
		http.Error(w, "failed to parse custom upload response", http.StatusBadGateway)
		return
	}

	user, _ := s.userOf(r)
	now := time.Now().UTC()
	size := int64(len(data))
	name := randomName()
	if s.uploadDir != "" && user != "" {
		if err := s.writeUploadMeta(name, uploadMeta{
			Owner:    user,
			Name:     filename,
			Uploaded: now,
			Size:     size,
			URL:      uploadURL,
		}); err != nil {
			s.log.Warn("custom upload sidecar write failed", "err", err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"id":      name,
		"url":     uploadURL,
		"name":    filename,
		"expires": now.Add(s.uploadTTL(size)).Format(time.RFC3339),
	})
}

// parseCustomUploadResponse extracts a target URL string from custom upload response body.
func parseCustomUploadResponse(body []byte, responseField string) (string, error) {
	trimmed := strings.TrimSpace(string(body))
	if responseField != "" {
		var obj any
		if err := json.Unmarshal(body, &obj); err != nil {
			return "", fmt.Errorf("response_field %q specified but body is not JSON: %w", responseField, err)
		}
		parts := strings.Split(responseField, ".")
		cur := obj
		for _, part := range parts {
			m, ok := cur.(map[string]any)
			if !ok {
				return "", fmt.Errorf("key %q in %q is not an object", part, responseField)
			}
			cur, ok = m[part]
			if !ok {
				return "", fmt.Errorf("key %q not found in response JSON", part)
			}
		}
		str, ok := cur.(string)
		if !ok {
			return "", fmt.Errorf("field %q in response JSON is not a string", responseField)
		}
		return strings.TrimSpace(str), nil
	}

	// Default parsing: try JSON object for a string "url" field first.
	var jsonMap map[string]any
	if err := json.Unmarshal(body, &jsonMap); err == nil {
		if val, ok := jsonMap["url"].(string); ok && strings.TrimSpace(val) != "" {
			return strings.TrimSpace(val), nil
		}
	}

	// Plain text URL response (e.g. x0.at, 0x0.st).
	if trimmed == "" {
		return "", errors.New("empty response body")
	}
	return trimmed, nil
}

// uploadEntry is one row of the per-user upload listing.
type uploadEntry struct {
	ID       string    `json:"id"`
	URL      string    `json:"url"`
	Name     string    `json:"name"` // original filename
	Size     int64     `json:"size"`
	Uploaded time.Time `json:"uploaded"`
	Expires  time.Time `json:"expires"`
}

// handleUploadList returns the requesting user's stored uploads, newest
// first, with each file's computed expiry.
// GET /api/uploads
func (s *Server) handleUploadList(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		s.handleUploadDelete(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, _ := s.userOf(r)
	entries := []uploadEntry{}
	sidecars, err := os.ReadDir(filepath.Join(s.uploadDir, uploadMetaDir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	for _, de := range sidecars {
		stored, ok := strings.CutSuffix(de.Name(), ".json")
		if !ok || de.IsDir() {
			continue
		}
		b, err := os.ReadFile(s.uploadMetaPath(stored))
		if err != nil {
			continue
		}
		var m uploadMeta
		if json.Unmarshal(b, &m) != nil || m.Owner != user {
			continue
		}
		urlStr := "/uploads/" + stored
		size := int64(0)
		if m.URL != "" {
			urlStr = m.URL
			size = m.Size
		} else {
			info, err := os.Stat(filepath.Join(s.uploadDir, stored))
			if err != nil {
				continue // already swept (or sidecar orphaned by a failed write)
			}
			size = info.Size()
		}
		entries = append(entries, uploadEntry{
			ID:       stored,
			URL:      urlStr,
			Name:     m.Name,
			Size:     size,
			Uploaded: m.Uploaded,
			Expires:  m.Uploaded.Add(s.uploadTTL(size)),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Uploaded.After(entries[j].Uploaded) })
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entries)
}

// handleUploadDelete deletes a stored upload (and its sidecar) owned by the
// requesting user.
// DELETE /api/uploads?id=<id> or DELETE /api/upload?id=<id>
func (s *Server) handleUploadDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, _ := s.userOf(r)

	target := r.URL.Query().Get("id")
	if target == "" {
		target = r.URL.Query().Get("name")
	}
	if target == "" {
		target = r.URL.Query().Get("url")
	}
	if target == "" && r.Body != nil {
		var body struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			URL  string `json:"url"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.ID != "" {
			target = body.ID
		} else if body.Name != "" {
			target = body.Name
		} else if body.URL != "" {
			target = body.URL
		}
	}

	if target == "" {
		http.Error(w, "missing upload identifier", http.StatusBadRequest)
		return
	}

	stored := filepath.Base(target)
	if strings.HasPrefix(target, "/uploads/") {
		stored = strings.TrimPrefix(target, "/uploads/")
		stored = filepath.Base(stored)
	}

	if stored == "." || stored == "/" || strings.HasPrefix(stored, ".") {
		http.Error(w, "invalid upload identifier", http.StatusBadRequest)
		return
	}

	metaPath := s.uploadMetaPath(stored)
	b, err := os.ReadFile(metaPath)
	if err != nil {
		foundStored := ""
		sidecars, readErr := os.ReadDir(filepath.Join(s.uploadDir, uploadMetaDir))
		if readErr == nil {
			for _, de := range sidecars {
				scName, ok := strings.CutSuffix(de.Name(), ".json")
				if !ok || de.IsDir() {
					continue
				}
				data, readErr := os.ReadFile(s.uploadMetaPath(scName))
				if readErr != nil {
					continue
				}
				var m uploadMeta
				if json.Unmarshal(data, &m) == nil && m.Owner == user {
					if m.URL == target || "/uploads/"+scName == target || m.Name == target || scName == target {
						foundStored = scName
						b = data
						metaPath = s.uploadMetaPath(scName)
						break
					}
				}
			}
		}
		if foundStored == "" {
			http.Error(w, "upload not found", http.StatusNotFound)
			return
		}
		stored = foundStored
	}

	var m uploadMeta
	if err := json.Unmarshal(b, &m); err != nil || m.Owner != user {
		http.Error(w, "upload not found", http.StatusNotFound)
		return
	}

	if m.URL == "" {
		localPath := filepath.Join(s.uploadDir, stored)
		_ = os.Remove(localPath)
	}

	if err := os.Remove(metaPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// sweepUploads deletes every stored file older than its size-dependent TTL
// (measured from mtime, so uploads that predate sidecar records expire too),
// plus any sidecar left without a file.
func (s *Server) sweepUploads(now time.Time) {
	files, err := os.ReadDir(s.uploadDir)
	if err != nil {
		return
	}
	for _, de := range files {
		if de.IsDir() {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > s.uploadTTL(info.Size()) {
			os.Remove(filepath.Join(s.uploadDir, de.Name()))
			os.Remove(s.uploadMetaPath(de.Name()))
		}
	}
	sidecars, err := os.ReadDir(filepath.Join(s.uploadDir, uploadMetaDir))
	if err != nil {
		return
	}
	for _, de := range sidecars {
		stored, ok := strings.CutSuffix(de.Name(), ".json")
		if !ok {
			continue
		}
		b, err := os.ReadFile(s.uploadMetaPath(stored))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				os.Remove(s.uploadMetaPath(stored))
			}
			continue
		}
		var m uploadMeta
		if err := json.Unmarshal(b, &m); err != nil {
			os.Remove(s.uploadMetaPath(stored))
			continue
		}
		if m.URL != "" {
			if now.Sub(m.Uploaded) > s.uploadTTL(m.Size) {
				os.Remove(s.uploadMetaPath(stored))
			}
		} else {
			if _, err := os.Stat(filepath.Join(s.uploadDir, stored)); errors.Is(err, os.ErrNotExist) {
				os.Remove(s.uploadMetaPath(stored))
			}
		}
	}
}

// sweepUploadsLoop sweeps once immediately, then hourly until ctx ends.
func (s *Server) sweepUploadsLoop(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	s.sweepUploads(time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sweepUploads(time.Now())
		}
	}
}

// uploadFileServer serves stored uploads with sniffing disabled so a stored
// file can't be reinterpreted as active content. Directory listing is
// disabled (noListFS): uploads are guarded only by their unguessable random
// names, so a browsable index would let anyone enumerate every stored file.
func (s *Server) uploadFileServer() http.Handler {
	fs := http.FileServer(noListFS{http.Dir(s.uploadDir)})
	return http.StripPrefix("/uploads/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; media-src 'self'")
		fs.ServeHTTP(w, r)
	}))
}

// noListFS wraps an http.FileSystem so directories report as nonexistent.
// http.FileServer renders an HTML index for any directory request; making
// Open fail for directories turns those requests into 404s instead. Dotted
// path segments are refused too: the .meta sidecars (which record who
// uploaded each file) live inside the upload dir and must never be served.
type noListFS struct{ fs http.FileSystem }

func (n noListFS) Open(name string) (http.File, error) {
	for seg := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(seg, ".") {
			return nil, os.ErrNotExist
		}
	}
	f, err := n.fs.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.IsDir() {
		f.Close()
		return nil, os.ErrNotExist
	}
	return f, nil
}

// errBadImage signals that data was recognised as an image format but could
// not be parsed well enough to guarantee its metadata was removed.
var errBadImage = errors.New("malformed image")

// stripImageMetadata removes embedded metadata from JPEG, PNG, GIF, and WebP,
// losslessly (image pixels are copied verbatim, never re-encoded). The format
// is detected from the leading magic bytes, not the filename, so a mislabelled
// extension can't smuggle metadata past the filter. Metadata-heavy TIFF and
// HEIF-family images are rejected because losslessly rewriting their item
// graphs is not safe here. Other non-image data is returned unchanged. Any
// recognised image that cannot be sanitized returns errBadImage.
func stripImageMetadata(data []byte) ([]byte, error) {
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return stripJPEG(data)
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return stripPNG(data)
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return stripGIF(data)
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return stripWebP(data)
	case isUnsafeImageContainer(data):
		return nil, errBadImage
	}
	return data, nil
}

func stripGIF(data []byte) ([]byte, error) {
	if len(data) < 13 {
		return nil, errBadImage
	}
	i := 13
	if data[10]&0x80 != 0 { // global colour table
		i += 3 * (1 << ((data[10] & 0x07) + 1))
	}
	if i > len(data) {
		return nil, errBadImage
	}
	out := append([]byte(nil), data[:i]...)
	for i < len(data) {
		start := i
		switch data[i] {
		case 0x3B: // trailer; discard anything appended after it
			return append(out, 0x3B), nil
		case 0x2C: // image descriptor + optional local table + image sub-blocks
			if i+10 > len(data) {
				return nil, errBadImage
			}
			i += 10
			if data[start+9]&0x80 != 0 {
				i += 3 * (1 << ((data[start+9] & 0x07) + 1))
			}
			if i >= len(data) {
				return nil, errBadImage
			}
			i++ // LZW minimum code size
			end, ok := gifSubBlocksEnd(data, i)
			if !ok {
				return nil, errBadImage
			}
			i = end
			out = append(out, data[start:i]...)
		case 0x21: // extension
			if i+2 > len(data) {
				return nil, errBadImage
			}
			label := data[i+1]
			i += 2
			end, ok := gifSubBlocksEnd(data, i)
			if !ok {
				return nil, errBadImage
			}
			drop := label == 0xFE // comment extension
			if label == 0xFF {    // application extension: preserve animation loop only
				drop = true
				if i < len(data) && data[i] == 11 && i+12 <= len(data) {
					app := string(data[i+1 : i+12])
					drop = app != "NETSCAPE2.0" && app != "ANIMEXTS1.0"
				}
			}
			i = end
			if !drop {
				out = append(out, data[start:i]...)
			}
		default:
			return nil, errBadImage
		}
	}
	return nil, errBadImage
}

func gifSubBlocksEnd(data []byte, i int) (int, bool) {
	for i < len(data) {
		n := int(data[i])
		i++
		if n == 0 {
			return i, true
		}
		if i+n > len(data) {
			return 0, false
		}
		i += n
	}
	return 0, false
}

func stripWebP(data []byte) ([]byte, error) {
	total := int(binary.LittleEndian.Uint32(data[4:8])) + 8
	if total < 12 || total > len(data) {
		return nil, errBadImage
	}
	out := append([]byte(nil), data[:12]...)
	for i := 12; i < total; {
		if i+8 > total {
			return nil, errBadImage
		}
		size := int(binary.LittleEndian.Uint32(data[i+4 : i+8]))
		end := i + 8 + size
		padded := end + size%2
		if size < 0 || end < i || padded > total {
			return nil, errBadImage
		}
		typ := string(data[i : i+4])
		if typ != "EXIF" && typ != "XMP " && typ != "ICCP" {
			chunkStart := len(out)
			out = append(out, data[i:padded]...)
			if typ == "VP8X" {
				if size < 1 {
					return nil, errBadImage
				}
				out[chunkStart+8] &^= 0x2C // clear ICC, EXIF, and XMP feature flags
			}
		}
		i = padded
	}
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-8))
	return out, nil
}

func isUnsafeImageContainer(data []byte) bool {
	if bytes.HasPrefix(data, []byte("II*\x00")) || bytes.HasPrefix(data, []byte("MM\x00*")) ||
		bytes.HasPrefix(data, []byte("II+\x00")) || bytes.HasPrefix(data, []byte("MM\x00+")) {
		return true // TIFF
	}
	if len(data) < 12 || string(data[4:8]) != "ftyp" {
		return false
	}
	imageBrands := map[string]bool{
		"avif": true, "avis": true, "heic": true, "heix": true,
		"hevc": true, "hevx": true, "mif1": true, "msf1": true,
	}
	if imageBrands[string(data[8:12])] {
		return true
	}
	end := len(data)
	if boxSize := int(binary.BigEndian.Uint32(data[:4])); boxSize >= 16 && boxSize < end {
		end = boxSize
	}
	for i := 16; i+4 <= end; i += 4 {
		if imageBrands[string(data[i:i+4])] {
			return true
		}
	}
	return false
}

// stripJPEG drops every APPn (0xE0–0xEF, which holds EXIF/GPS, XMP, ICC, JFIF
// thumbnails) and COM comment segment, keeping the frame, tables, and the
// entropy-coded scans. A JPEG is SOI followed by length-prefixed marker
// segments; each SOS starts an unframed entropy-coded scan (progressive files
// have several), and EOI ends the image. Everything after EOI is dropped:
// it is not image data, and phone "motion photos" append a whole MP4 (with
// GPS) there that must not survive the metadata strip.
func stripJPEG(data []byte) ([]byte, error) {
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, errBadImage
	}
	out := make([]byte, 0, len(data))
	out = append(out, 0xFF, 0xD8) // SOI
	i := 2
	for {
		// A marker is 0xFF followed by a non-0xFF, non-0x00 byte; runs of
		// 0xFF are fill bytes that precede the real marker byte.
		if i+1 >= len(data) || data[i] != 0xFF {
			return nil, errBadImage
		}
		for i < len(data) && data[i] == 0xFF {
			i++
		}
		if i >= len(data) {
			return nil, errBadImage
		}
		marker := data[i]
		i++
		switch {
		case marker == 0xD9: // EOI: done; trailing bytes are dropped
			out = append(out, 0xFF, marker)
			return out, nil
		case marker == 0xDA:
			// SOS: copy its length-prefixed header, then the entropy-coded
			// scan. Within the scan 0xFF is followed only by 0x00 (stuffing),
			// fill 0xFFs, or RSTn — anything else is a real marker that ends
			// the scan and hands control back to this loop (so a progressive
			// file's later DHT/SOS segments are parsed, not pattern-matched,
			// and a stray FF D9 inside a table can't truncate the image).
			if i+2 > len(data) {
				return nil, errBadImage
			}
			segLen := int(binary.BigEndian.Uint16(data[i:]))
			if segLen < 2 || i+segLen > len(data) {
				return nil, errBadImage
			}
			out = append(out, 0xFF, marker)
			out = append(out, data[i:i+segLen]...)
			i += segLen
			start := i
			for i < len(data) {
				if data[i] != 0xFF {
					i++
					continue
				}
				k := i
				for k < len(data) && data[k] == 0xFF {
					k++
				}
				if k >= len(data) {
					i = len(data)
					break
				}
				if m := data[k]; m == 0x00 || (m >= 0xD0 && m <= 0xD7) {
					i = k + 1 // stuffing or restart marker: scan continues
					continue
				}
				break // real marker at i (kept for the outer loop)
			}
			out = append(out, data[start:i]...)
			if i >= len(data) {
				return out, nil // truncated scan; nothing trails it anyway
			}
		case marker >= 0xD0 && marker <= 0xD7, marker == 0x01:
			// Standalone markers (RSTn, TEM) carry no payload.
			out = append(out, 0xFF, marker)
		default:
			if i+2 > len(data) {
				return nil, errBadImage
			}
			segLen := int(binary.BigEndian.Uint16(data[i:]))
			if segLen < 2 || i+segLen > len(data) {
				return nil, errBadImage
			}
			drop := marker == 0xFE || (marker >= 0xE0 && marker <= 0xEF)
			if !drop {
				out = append(out, 0xFF, marker)
				out = append(out, data[i:i+segLen]...)
			}
			i += segLen
		}
	}
}

// pngMetaChunks are the ancillary PNG chunk types that carry no rendering
// information, only metadata: text (tEXt/zTXt/iTXt), embedded EXIF (eXIf), and
// the last-modified time (tIME). Everything else — including colour chunks like
// iCCP/gAMA/sRGB — is preserved so the image still renders faithfully.
var pngMetaChunks = map[string]bool{
	"tEXt": true, "zTXt": true, "iTXt": true, "eXIf": true, "tIME": true,
}

// stripPNG drops the metadata chunks listed in pngMetaChunks and copies every
// other chunk (length, type, data, CRC) verbatim through to IEND.
func stripPNG(data []byte) ([]byte, error) {
	const sig = "\x89PNG\r\n\x1a\n"
	if !bytes.HasPrefix(data, []byte(sig)) {
		return nil, errBadImage
	}
	out := make([]byte, 0, len(data))
	out = append(out, sig...)
	i := len(sig)
	for {
		if i+8 > len(data) {
			return nil, errBadImage
		}
		dataLen := int(binary.BigEndian.Uint32(data[i:]))
		typ := string(data[i+4 : i+8])
		end := i + 12 + dataLen // length(4) + type(4) + data + crc(4)
		if dataLen < 0 || end > len(data) {
			return nil, errBadImage
		}
		if !pngMetaChunks[typ] {
			out = append(out, data[i:end]...)
		}
		i = end
		if typ == "IEND" {
			return out, nil
		}
	}
}

func randomName() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// safeExt returns a lowercased, dot-prefixed extension limited to a short
// alphanumeric tail, or "" if none.
func safeExt(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if len(ext) < 2 || len(ext) > 6 {
		return ""
	}
	for _, c := range ext[1:] {
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') {
			return ""
		}
	}
	return ext
}
