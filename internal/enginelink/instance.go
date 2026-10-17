package enginelink

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// instanceFile holds the instance id inside stugan's data directory.
const instanceFile = "engine-instance"

// LoadInstanceID returns this installation's engine instance id, creating it
// on first use. Connection ids are only unique within one stugan data
// directory, so the engine partitions sessions by this value: two stugan
// installations pointed at one engine can never reach each other's sockets.
func LoadInstanceID(dataDir string) (string, error) {
	p := filepath.Join(dataDir, instanceFile)
	b, err := os.ReadFile(p)
	if err == nil {
		id := strings.TrimSpace(string(b))
		if validInstanceID(id) {
			return id, nil
		}
		return "", fmt.Errorf("engine instance id in %s is malformed; delete the file to mint a new one", p)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read engine instance id: %w", err)
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw[:])
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", err
	}
	// O_EXCL: if another process raced us, use the id it wrote.
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return LoadInstanceID(dataDir)
	}
	if err != nil {
		return "", fmt.Errorf("create engine instance id: %w", err)
	}
	if _, err := f.WriteString(id + "\n"); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return id, nil
}

func validInstanceID(id string) bool {
	if len(id) < 8 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
