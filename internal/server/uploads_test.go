package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klippelism/stugan/internal/config"
	"github.com/klippelism/stugan/internal/core"
)

// jpegSeg builds a length-prefixed JPEG marker segment (0xFF, marker, len, payload).
func jpegSeg(marker byte, payload []byte) []byte {
	seg := []byte{0xFF, marker, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	return append(seg, payload...)
}

func TestStripJPEG(t *testing.T) {
	scan := []byte{0xFF, 0xDA, 0x00, 0x08, 0x01, 0x01, 0x00, 0x3F, 0x00, 0x12, 0x34} // SOS + entropy
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8})                             // SOI
	b.Write(jpegSeg(0xE0, []byte("JFIF\x00\x01\x01\x00")))  // APP0 JFIF
	b.Write(jpegSeg(0xE1, []byte("Exif\x00\x00secretgps"))) // APP1 EXIF
	b.Write(jpegSeg(0xFE, []byte("a private comment")))     // COM
	b.Write(jpegSeg(0xDB, bytes.Repeat([]byte{0x10}, 64)))  // DQT (kept)
	b.Write(scan)                                           // SOS + scan
	b.Write([]byte{0xFF, 0xD9})                             // EOI

	out, err := stripImageMetadata(b.Bytes())
	if err != nil {
		t.Fatalf("stripImageMetadata: %v", err)
	}
	if bytes.Contains(out, []byte("Exif")) || bytes.Contains(out, []byte("secretgps")) {
		t.Error("EXIF segment survived stripping")
	}
	if bytes.Contains(out, []byte("private comment")) {
		t.Error("COM comment survived stripping")
	}
	if bytes.Contains(out, []byte("JFIF")) {
		t.Error("APP0 JFIF segment survived stripping")
	}
	if !bytes.Contains(out, scan) {
		t.Error("scan data was not preserved verbatim")
	}
	if !bytes.HasSuffix(out, []byte{0xFF, 0xD9}) {
		t.Error("EOI marker missing from output")
	}
	if !bytes.Contains(out, jpegSeg(0xDB, bytes.Repeat([]byte{0x10}, 64))) {
		t.Error("DQT table was dropped; only metadata should be removed")
	}
}

func TestStripJPEGMalformed(t *testing.T) {
	// SOI then a truncated APP1 length pointing past the buffer.
	bad := []byte{0xFF, 0xD8, 0xFF, 0xE1, 0xFF, 0xFF, 0x00}
	if _, err := stripImageMetadata(bad); err != errBadImage {
		t.Fatalf("want errBadImage for malformed JPEG, got %v", err)
	}
}

// pngChunk builds a PNG chunk with a valid CRC over type+data.
func pngChunk(typ string, data []byte) []byte {
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, uint32(len(data)))
	out = append(out, typ...)
	out = append(out, data...)
	crc := crc32.ChecksumIEEE(append([]byte(typ), data...))
	var c [4]byte
	binary.BigEndian.PutUint32(c[:], crc)
	return append(out, c[:]...)
}

func TestStripPNG(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	b.Write(pngChunk("IHDR", make([]byte, 13)))
	b.Write(pngChunk("tEXt", []byte("Author\x00Jane Doe")))
	b.Write(pngChunk("eXIf", []byte("II*\x00gpsdata")))
	b.Write(pngChunk("iCCP", []byte("profile\x00\x00data"))) // colour, must survive
	b.Write(pngChunk("IDAT", []byte("pixels")))
	b.Write(pngChunk("IEND", nil))

	out, err := stripImageMetadata(b.Bytes())
	if err != nil {
		t.Fatalf("stripImageMetadata: %v", err)
	}
	if bytes.Contains(out, []byte("Jane Doe")) {
		t.Error("tEXt metadata survived stripping")
	}
	if bytes.Contains(out, []byte("gpsdata")) {
		t.Error("eXIf chunk survived stripping")
	}
	if !bytes.Contains(out, []byte("profile")) {
		t.Error("iCCP colour chunk was dropped; only metadata should be removed")
	}
	if !bytes.Contains(out, []byte("pixels")) {
		t.Error("IDAT pixel data was not preserved")
	}
	if !bytes.HasSuffix(out, pngChunk("IEND", nil)) {
		t.Error("IEND chunk missing from output")
	}
}

func TestStripGIFMetadata(t *testing.T) {
	data := []byte("GIF89a")
	data = append(data, 1, 0, 1, 0, 0, 0, 0) // logical screen, no colour table
	data = append(data, 0x21, 0xFE, 6)
	data = append(data, []byte("secret")...)
	data = append(data, 0) // comment terminator
	data = append(data, 0x21, 0xFF, 11)
	data = append(data, []byte("NETSCAPE2.0")...)
	data = append(data, 3, 1, 0, 0, 0) // looping extension
	data = append(data, 0x2C, 0, 0, 0, 0, 1, 0, 1, 0, 0)
	data = append(data, 2, 1, 0, 0, 0x3B) // LZW size, data block, terminator, trailer

	out, err := stripImageMetadata(data)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("secret")) {
		t.Fatal("GIF comment metadata survived")
	}
	if !bytes.Contains(out, []byte("NETSCAPE2.0")) || !bytes.Contains(out, []byte{0x2C}) {
		t.Fatal("GIF animation/image data was not preserved")
	}
}

func webPChunk(typ string, payload []byte) []byte {
	out := append([]byte(typ), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(payload)))
	out = append(out, payload...)
	if len(payload)%2 != 0 {
		out = append(out, 0)
	}
	return out
}

func TestStripWebPMetadata(t *testing.T) {
	data := append([]byte("RIFF\x00\x00\x00\x00WEBP"), webPChunk("VP8X", append([]byte{0x2C}, make([]byte, 9)...))...)
	data = append(data, webPChunk("EXIF", []byte("secret gps"))...)
	data = append(data, webPChunk("XMP ", []byte("private xmp"))...)
	data = append(data, webPChunk("VP8 ", []byte("pixels"))...)
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
	data = append(data, []byte("trailing private bytes")...)

	out, err := stripImageMetadata(data)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("secret gps")) || bytes.Contains(out, []byte("private xmp")) {
		t.Fatal("WebP metadata survived")
	}
	if !bytes.Contains(out, []byte("pixels")) {
		t.Fatal("WebP image data was dropped")
	}
	if out[20]&0x2C != 0 {
		t.Fatalf("VP8X metadata flags survived: %#x", out[20])
	}
	if got := int(binary.LittleEndian.Uint32(out[4:8])) + 8; got != len(out) {
		t.Fatalf("WebP RIFF size = %d, output len = %d", got, len(out))
	}
}

func TestUnsafeImageContainersFailClosed(t *testing.T) {
	for name, data := range map[string][]byte{
		"tiff": []byte("II*\x00private exif"),
		"avif": append([]byte{0, 0, 0, 20}, []byte("ftypavif\x00\x00\x00\x00avif")...),
	} {
		if _, err := stripImageMetadata(data); err != errBadImage {
			t.Errorf("%s: got %v, want errBadImage", name, err)
		}
	}
}

func TestUploadStripsEXIFEndToEnd(t *testing.T) {
	scan := []byte{0xFF, 0xDA, 0x00, 0x08, 0x01, 0x01, 0x00, 0x3F, 0x00, 0x42}
	var img bytes.Buffer
	img.Write([]byte{0xFF, 0xD8})
	img.Write(jpegSeg(0xE1, []byte("Exif\x00\x00secretgpsfix")))
	img.Write(scan)
	img.Write([]byte{0xFF, 0xD9})

	eng := core.New(core.Options{Sink: noopSink{}})
	srv := New(SingleUser(&Tenant{Engine: eng}), Options{UploadDir: t.TempDir(), MaxUpload: 1 << 20})
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "photo.jpg")
	fw.Write(img.Bytes())
	mw.Close()

	resp, err := http.Post(hs.URL+"/api/upload", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d", resp.StatusCode)
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}

	got, err := http.Get(hs.URL + out.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	stored, _ := io.ReadAll(got.Body)
	if bytes.Contains(stored, []byte("secretgpsfix")) {
		t.Error("served upload still contains EXIF metadata")
	}
	if !bytes.Contains(stored, scan) {
		t.Error("served upload lost its image scan data")
	}
}

func TestUploadTTL(t *testing.T) {
	s := &Server{maxUpload: 10 << 20}
	day := 24 * time.Hour
	cases := []struct {
		size int64
		want time.Duration
	}{
		{0, 7 * day},        // empty file → maximum age
		{10 << 20, 3 * day}, // at MAX_SIZE → minimum age
		{5 << 20, 4 * day},  // half size → 3d + 4d*(0.5)^2 = 4d
		{20 << 20, 3 * day}, // over MAX_SIZE clamps to minimum
		{1 << 20, 3*day + time.Duration(0.81*float64(4*day))}, // 3d + 4d*(0.9)^2
	}
	for _, c := range cases {
		if got := s.uploadTTL(c.size); got != c.want {
			t.Errorf("uploadTTL(%d) = %v, want %v", c.size, got, c.want)
		}
	}
}

// uploadFile posts one multipart upload and returns the served URL.
func uploadFile(t *testing.T, base, filename string, content []byte) string {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", filename)
	fw.Write(content)
	mw.Close()
	resp, err := http.Post(base+"/api/upload", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d", resp.StatusCode)
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.URL
}

func TestUploadListAndSweep(t *testing.T) {
	dir := t.TempDir()
	eng := core.New(core.Options{Sink: noopSink{}})
	srv := New(SingleUser(&Tenant{Engine: eng}), Options{UploadDir: dir, MaxUpload: 1 << 20})
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	url := uploadFile(t, hs.URL, "notes.txt", []byte("hello there"))
	stored := strings.TrimPrefix(url, "/uploads/")

	// The sidecar with the owner id must never be served.
	if r, err := http.Get(hs.URL + "/uploads/.meta/" + stored + ".json"); err != nil {
		t.Fatal(err)
	} else if r.Body.Close(); r.StatusCode != http.StatusNotFound {
		t.Errorf("sidecar served with status %d, want 404", r.StatusCode)
	}

	// The listing shows the upload, owned by the implicit user.
	r, err := http.Get(hs.URL + "/api/uploads")
	if err != nil {
		t.Fatal(err)
	}
	var list []struct {
		URL      string    `json:"url"`
		Name     string    `json:"name"`
		Size     int64     `json:"size"`
		Uploaded time.Time `json:"uploaded"`
		Expires  time.Time `json:"expires"`
	}
	if err := json.NewDecoder(r.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if len(list) != 1 {
		t.Fatalf("listed %d uploads, want 1", len(list))
	}
	if list[0].URL != url || list[0].Name != "notes.txt" || list[0].Size != int64(len("hello there")) {
		t.Errorf("listing entry = %+v", list[0])
	}
	ttl := list[0].Expires.Sub(list[0].Uploaded)
	if ttl < 3*24*time.Hour || ttl > 7*24*time.Hour {
		t.Errorf("listed ttl = %v, want within [3d, 7d]", ttl)
	}

	// A fresh file survives a sweep.
	srv.sweepUploads(time.Now())
	if _, err := os.Stat(filepath.Join(dir, stored)); err != nil {
		t.Fatalf("fresh upload swept: %v", err)
	}

	// Backdate the file past 7 days: even the smallest file must be gone.
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, stored), old, old); err != nil {
		t.Fatal(err)
	}
	srv.sweepUploads(time.Now())
	if _, err := os.Stat(filepath.Join(dir, stored)); !os.IsNotExist(err) {
		t.Error("expired upload still on disk after sweep")
	}
	if _, err := os.Stat(filepath.Join(dir, uploadMetaDir, stored+".json")); !os.IsNotExist(err) {
		t.Error("sidecar of expired upload still on disk after sweep")
	}

	// And it no longer appears in the listing.
	r2, err := http.Get(hs.URL + "/api/uploads")
	if err != nil {
		t.Fatal(err)
	}
	list = list[:0]
	if err := json.NewDecoder(r2.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if len(list) != 0 {
		t.Errorf("listed %d uploads after sweep, want 0", len(list))
	}
}

func TestSweepUsesSizeDependentTTL(t *testing.T) {
	dir := t.TempDir()
	eng := core.New(core.Options{Sink: noopSink{}})
	srv := New(SingleUser(&Tenant{Engine: eng}), Options{UploadDir: dir, MaxUpload: 1 << 20})
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	// A max-size file only gets the 3-day minimum; a tiny one keeps ~7 days.
	bigURL := uploadFile(t, hs.URL, "big.bin", bytes.Repeat([]byte{0xAB}, 1<<20))
	smallURL := uploadFile(t, hs.URL, "small.txt", []byte("x"))
	big := strings.TrimPrefix(bigURL, "/uploads/")
	small := strings.TrimPrefix(smallURL, "/uploads/")

	// At 4 days old the big file is past its TTL, the small one is not.
	old := time.Now().Add(-4 * 24 * time.Hour)
	for _, f := range []string{big, small} {
		if err := os.Chtimes(filepath.Join(dir, f), old, old); err != nil {
			t.Fatal(err)
		}
	}
	srv.sweepUploads(time.Now())
	if _, err := os.Stat(filepath.Join(dir, big)); !os.IsNotExist(err) {
		t.Error("large file survived past its shortened TTL")
	}
	if _, err := os.Stat(filepath.Join(dir, small)); err != nil {
		t.Errorf("small file swept before its 7-day TTL: %v", err)
	}
}

func TestStripImageMetadataPassthrough(t *testing.T) {
	// A non-image upload (e.g. a text file) must be returned byte-for-byte.
	in := []byte("just some plain text, not an image at all")
	out, err := stripImageMetadata(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(in, out) {
		t.Error("non-image data was modified")
	}
}

// TestUploadOverLimitRejected: a file slightly over the cap fits under the
// body limit (which leaves headroom for multipart framing) and used to be
// stored silently truncated to exactly maxUpload bytes. It must 413 instead.
func TestUploadOverLimitRejected(t *testing.T) {
	const maxUpload = 4 << 10
	eng := core.New(core.Options{Sink: noopSink{}})
	srv := New(SingleUser(&Tenant{Engine: eng}), Options{UploadDir: t.TempDir(), MaxUpload: maxUpload})
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	post := func(size int) *http.Response {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, _ := mw.CreateFormFile("file", "blob.bin")
		fw.Write(bytes.Repeat([]byte{0xAB}, size))
		mw.Close()
		resp, err := http.Post(hs.URL+"/api/upload", mw.FormDataContentType(), &body)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	if resp := post(maxUpload + 1); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("file over cap: status = %d, want 413", resp.StatusCode)
	}
	if resp := post(maxUpload); resp.StatusCode != http.StatusOK {
		t.Errorf("file exactly at cap: status = %d, want 200", resp.StatusCode)
	}
}

// TestStripJPEGTrailingData: bytes after EOI (e.g. a phone "motion photo"
// MP4 carrying GPS) must not survive; a progressive file's post-scan marker
// segments must (including a stray FF D9 inside a table payload, which a
// naive EOI byte-search would mistake for the end of image).
func TestStripJPEGTrailingData(t *testing.T) {
	scan := []byte{0xFF, 0xDA, 0x00, 0x08, 0x01, 0x01, 0x00, 0x3F, 0x00, 0x42}
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8})
	b.Write(jpegSeg(0xDB, bytes.Repeat([]byte{0x10}, 4))) // DQT
	b.Write(scan)
	// Progressive: a DHT between scans whose payload contains FF D9.
	b.Write(jpegSeg(0xC4, []byte{0x00, 0xFF, 0xD9, 0x01}))
	b.Write(scan)
	b.Write([]byte{0xFF, 0xD9})
	b.Write([]byte("....ftypmp42 GPS COORDS HIDING IN TRAILING MP4"))

	out, err := stripImageMetadata(b.Bytes())
	if err != nil {
		t.Fatalf("stripImageMetadata: %v", err)
	}
	if !bytes.HasSuffix(out, []byte{0xFF, 0xD9}) {
		t.Errorf("output does not end at EOI: % x", out[len(out)-8:])
	}
	if bytes.Contains(out, []byte("ftypmp42")) {
		t.Error("trailing motion-photo payload survived the strip")
	}
	if !bytes.Contains(out, []byte{0xFF, 0xC4, 0x00, 0x06, 0x00, 0xFF, 0xD9, 0x01}) {
		t.Error("progressive DHT between scans was mangled")
	}
	if bytes.Count(out, []byte{0x3F, 0x00, 0x42}) != 2 {
		t.Error("scan data not preserved across both scans")
	}
}

func TestParseCustomUploadResponse(t *testing.T) {
	t.Run("plain text response", func(t *testing.T) {
		body := []byte("https://x0.at/abcdef.png\n")
		url, err := parseCustomUploadResponse(body, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if url != "https://x0.at/abcdef.png" {
			t.Errorf("got %q, want %q", url, "https://x0.at/abcdef.png")
		}
	})

	t.Run("default JSON url key", func(t *testing.T) {
		body := []byte(`{"url": "https://0x0.st/xyz.jpg"}`)
		url, err := parseCustomUploadResponse(body, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if url != "https://0x0.st/xyz.jpg" {
			t.Errorf("got %q, want %q", url, "https://0x0.st/xyz.jpg")
		}
	})

	t.Run("custom response field path", func(t *testing.T) {
		body := []byte(`{"data": {"link": "https://custom.host/file.png"}}`)
		url, err := parseCustomUploadResponse(body, "data.link")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if url != "https://custom.host/file.png" {
			t.Errorf("got %q, want %q", url, "https://custom.host/file.png")
		}
	})

	t.Run("missing custom response field", func(t *testing.T) {
		body := []byte(`{"data": {}}`)
		if _, err := parseCustomUploadResponse(body, "data.link"); err == nil {
			t.Error("expected error for missing field, got nil")
		}
	})
}

func TestCustomUploadHostEndToEnd(t *testing.T) {
	// Mock custom upload endpoint (e.g. x0.at)
	mockHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Auth-Token") != "secretToken" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		file, hdr, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "missing file", http.StatusBadRequest)
			return
		}
		file.Close()
		if hdr.Filename != "hello.txt" {
			http.Error(w, "bad filename", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("https://x0.at/12345.txt\n"))
	}))
	defer mockHost.Close()

	uploadDir := t.TempDir()
	eng := core.New(core.Options{Sink: noopSink{}})
	srv := New(SingleUser(&Tenant{Engine: eng}), Options{
		UploadDir: uploadDir,
		MaxUpload: 1 << 20,
		Uploads: config.UploadsConfig{
			Mode:      "custom",
			URL:       mockHost.URL,
			FieldName: "file",
			Headers: map[string]string{
				"X-Auth-Token": "secretToken",
			},
		},
	})
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "hello.txt")
	fw.Write([]byte("hello world"))
	mw.Close()

	resp, err := http.Post(hs.URL+"/api/upload", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d, want 200", resp.StatusCode)
	}

	var res struct {
		URL  string `json:"url"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}

	if res.URL != "https://x0.at/12345.txt" {
		t.Errorf("got url %q, want \"https://x0.at/12345.txt\"", res.URL)
	}
	if res.Name != "hello.txt" {
		t.Errorf("got name %q, want \"hello.txt\"", res.Name)
	}

	// Verify custom upload appears in /api/uploads listing
	r, err := http.Get(hs.URL + "/api/uploads")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var list []struct {
		URL  string `json:"url"`
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("listed %d uploads, want 1", len(list))
	}
	if list[0].URL != "https://x0.at/12345.txt" || list[0].Name != "hello.txt" || list[0].Size != int64(len("hello world")) {
		t.Errorf("listed entry = %+v", list[0])
	}
}

func TestUploadDelete(t *testing.T) {
	dir := t.TempDir()
	eng := core.New(core.Options{Sink: noopSink{}})
	srv := New(SingleUser(&Tenant{Engine: eng}), Options{UploadDir: dir, MaxUpload: 1 << 20})
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	// 1. Upload a file
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "testfile.txt")
	fw.Write([]byte("delete me"))
	mw.Close()

	resp, err := http.Post(hs.URL+"/api/upload", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d, want 200", resp.StatusCode)
	}

	var uploadRes struct {
		ID   string `json:"id"`
		URL  string `json:"url"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&uploadRes); err != nil {
		t.Fatal(err)
	}
	if uploadRes.ID == "" {
		t.Fatal("upload ID was empty")
	}

	// Verify upload exists on disk
	storedPath := filepath.Join(dir, uploadRes.ID)
	if _, err := os.Stat(storedPath); err != nil {
		t.Fatalf("stored file missing on disk: %v", err)
	}
	metaPath := srv.uploadMetaPath(uploadRes.ID)
	if _, err := os.Stat(metaPath); err != nil {
		t.Fatalf("meta sidecar missing on disk: %v", err)
	}

	// 2. Verify in listing
	rList, err := http.Get(hs.URL + "/api/uploads")
	if err != nil {
		t.Fatal(err)
	}
	defer rList.Body.Close()
	var list []struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.NewDecoder(rList.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d uploads in list, want 1", len(list))
	}
	if list[0].ID != uploadRes.ID {
		t.Errorf("got listed ID %q, want %q", list[0].ID, uploadRes.ID)
	}

	// 3. Invalid delete attempt (path traversal / bad target)
	reqBad, _ := http.NewRequest(http.MethodDelete, hs.URL+"/api/uploads?id=../secret", nil)
	respBad, err := http.DefaultClient.Do(reqBad)
	if err != nil {
		t.Fatal(err)
	}
	respBad.Body.Close()
	if respBad.StatusCode != http.StatusBadRequest && respBad.StatusCode != http.StatusNotFound {
		t.Errorf("path traversal delete status = %d, want 400 or 404", respBad.StatusCode)
	}

	// 4. Delete the upload manually
	reqDel, _ := http.NewRequest(http.MethodDelete, hs.URL+"/api/uploads?id="+uploadRes.ID, nil)
	respDel, err := http.DefaultClient.Do(reqDel)
	if err != nil {
		t.Fatal(err)
	}
	defer respDel.Body.Close()
	if respDel.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", respDel.StatusCode)
	}

	// 5. Verify file and sidecar were removed from disk
	if _, err := os.Stat(storedPath); !os.IsNotExist(err) {
		t.Errorf("stored file still exists on disk after delete")
	}
	if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
		t.Errorf("meta sidecar still exists on disk after delete")
	}

	// 6. Verify empty listing
	rList2, err := http.Get(hs.URL + "/api/uploads")
	if err != nil {
		t.Fatal(err)
	}
	defer rList2.Body.Close()
	var list2 []any
	if err := json.NewDecoder(rList2.Body).Decode(&list2); err != nil {
		t.Fatal(err)
	}
	if len(list2) != 0 {
		t.Errorf("got %d uploads in list after delete, want 0", len(list2))
	}
}
