package server

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// handleUpload accepts a multipart file upload (field "file"), stores it
// under uploadDir with a random name, and returns its served URL.
// POST /api/upload
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
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

	data, err := io.ReadAll(io.LimitReader(file, s.maxUpload))
	if err != nil {
		http.Error(w, "read failed", http.StatusInternalServerError)
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
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"url":  "/uploads/" + name,
		"name": hdr.Filename,
	})
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
// Open fail for directories turns those requests into 404s instead.
type noListFS struct{ fs http.FileSystem }

func (n noListFS) Open(name string) (http.File, error) {
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

// stripImageMetadata removes embedded metadata from JPEG and PNG uploads,
// losslessly (image pixels are copied verbatim, never re-encoded). The format
// is detected from the leading magic bytes, not the filename, so a mislabelled
// extension can't smuggle metadata past the filter. Non-image data and image
// formats we don't rewrite (e.g. GIF, WebP) are returned unchanged. A
// recognised image that fails to parse returns errBadImage so the caller can
// fail closed rather than store a file with its metadata intact.
func stripImageMetadata(data []byte) ([]byte, error) {
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return stripJPEG(data)
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return stripPNG(data)
	}
	return data, nil
}

// stripJPEG drops every APPn (0xE0–0xEF, which holds EXIF/GPS, XMP, ICC, JFIF
// thumbnails) and COM comment segment, keeping the frame, tables, and the
// entropy-coded scan. A JPEG is SOI followed by length-prefixed marker
// segments until SOS, after which the compressed scan runs unframed to EOI.
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
		case marker == 0xD9: // EOI
			out = append(out, 0xFF, marker)
			return out, nil
		case marker == 0xDA: // SOS: copy marker + the rest (scan data) verbatim
			out = append(out, 0xFF, marker)
			out = append(out, data[i:]...)
			return out, nil
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
