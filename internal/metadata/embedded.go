package metadata

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed data/mysql.json.gz
var embeddedMySQLGZ []byte

// embeddedLoader is a package variable so tests can swap the embedded source.
var embeddedLoader = func(engine string) (*Index, error) {
	var raw []byte
	switch engine {
	case "mysql":
		raw = embeddedMySQLGZ
	default:
		return nil, fmt.Errorf("no embedded metadata for engine %q", engine)
	}
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

// Embedded returns the metadata baked into the binary at build time
// (gzip-compressed generated metadata).
func Embedded(engine string) (*Index, error) {
	return embeddedLoader(engine)
}
