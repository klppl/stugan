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
