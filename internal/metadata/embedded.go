package metadata

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"
)

//go:embed data/mysql.json.gz
var embeddedMySQLGZ []byte

// embeddedRaw holds the raw embedded metadata bytes per engine, registered
// by the owning engine packages at init time (mysql registers its gz copy
// here; postgres registers versions.json in providers/postgres).
var (
	embeddedMu     sync.RWMutex
	embeddedRaw    = map[string][]byte{}
	embeddedGunzip = map[string]bool{} // whether the bytes are gzip-compressed
)

// RegisterEmbedded makes engine's baked-in metadata available to
// EnsureBuiltin. Compressed bytes (gzip) are decompressed on read.
func RegisterEmbedded(engine string, raw []byte, gzipCompressed bool) {
	embeddedMu.Lock()
	defer embeddedMu.Unlock()
	embeddedRaw[engine] = raw
	embeddedGunzip[engine] = gzipCompressed
}

func init() {
	RegisterEmbedded("mysql", embeddedMySQLGZ, true)
}

// Embedded returns the metadata baked into the binary for an engine.
func Embedded(engine string) (*Index, error) {
	embeddedMu.RLock()
	raw, ok := embeddedRaw[engine]
	gz := embeddedGunzip[engine]
	embeddedMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no embedded metadata for engine %q", engine)
	}
	if gz {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("embedded %s metadata corrupt: %w", engine, err)
		}
		defer zr.Close()
		var ix Index
		if err := json.NewDecoder(zr).Decode(&ix); err != nil {
			return nil, fmt.Errorf("embedded %s metadata corrupt: %w", engine, err)
		}
		return &ix, nil
	}
	var ix Index
	if err := json.Unmarshal(raw, &ix); err != nil {
		return nil, fmt.Errorf("embedded %s metadata corrupt: %w", engine, err)
	}
	return &ix, nil
}
