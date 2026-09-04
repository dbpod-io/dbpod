package dist

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// rpm.go: minimal RPM reader — enough to locate and decompress the cpio
// payload of a .rpm without external dependencies.
//
// RPM file layout:
//
//	lead (96 bytes)
//	signature header (header structure, padded to 8 bytes)
//	header (header structure)
//	payload archive (cpio, compressed)

const rpmHeaderMagic = "\x8e\xad\xe8"

// rpmHeader is the parsed index of a header section.
type rpmHeader struct {
	NumEntries uint32
	// Index entries: (tag, type, offset, count) — only what we need.
	DataSize uint32
}

// rpmPayloadReader returns a reader over the decompressed cpio payload.
func rpmPayloadReader(f *os.File) (io.Reader, error) {
	// lead: 96 bytes (magic 0xEDABEEDB at offset 0)
	lead := make([]byte, 96)
	if _, err := io.ReadFull(f, lead); err != nil {
		return nil, err
	}
	if binary.BigEndian.Uint32(lead[0:4]) != 0xEDABEEDB {
		return nil, fmt.Errorf("not an rpm file (bad lead magic)")
	}

	// skip the signature header section, then read the main header
	if err := skipRPMHeader(f); err != nil {
		return nil, err
	}
	hdr, err := readRPMHeader(f)
	if err != nil {
		return nil, err
	}
	_ = hdr

	// the payload starts right after the header, byte-aligned
	comp, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return decompressRPMPayload(comp)
}

// skipRPMHeader reads one header structure (entries + store) and leaves the
// file positioned after the 8-byte padding.
func skipRPMHeader(f *os.File) error {
	const headerMagic = "\x8e\xad\xe8"
	magic := make([]byte, 3)
	if _, err := io.ReadFull(f, magic); err != nil {
		return err
	}
	if string(magic) != rpmHeaderMagic {
		return fmt.Errorf("bad rpm header magic")
	}
	reserved := make([]byte, 5)
	if _, err := io.ReadFull(f, reserved); err != nil {
		return err
	}
	il := make([]byte, 4)
	if _, err := io.ReadFull(f, il); err != nil {
		return err
	}
	ds := make([]byte, 4)
	if _, err := io.ReadFull(f, ds); err != nil {
		return err
	}
	numEntries := binary.BigEndian.Uint32(il)
	storeSize := binary.BigEndian.Uint32(ds)
	// index entries: 16 bytes each; then the data store
	skip := int64(numEntries)*16 + int64(storeSize)
	if pad := (skip + 7) % 8; pad != 0 {
		skip += 8 - pad // header sections are padded to 8 bytes
	}
	_, err := f.Seek(skip, io.SeekCurrent)
	return err
}

// readRPMHeader parses (and discards) a header structure, returning the
// number of entries.
func readRPMHeader(f *os.File) (rpmHeader, error) {
	var h rpmHeader
	magic := make([]byte, 3)
	if _, err := io.ReadFull(f, magic); err != nil {
		return h, err
	}
	if string(magic) != rpmHeaderMagic {
		return h, fmt.Errorf("bad rpm header magic")
	}
	reserved := make([]byte, 5)
	if _, err := io.ReadFull(f, reserved); err != nil {
		return h, err
	}
	il := make([]byte, 4)
	ds := make([]byte, 4)
	if _, err := io.ReadFull(f, il); err != nil {
		return h, err
	}
	if _, err := io.ReadFull(f, ds); err != nil {
		return h, err
	}
	h.NumEntries = binary.BigEndian.Uint32(il)
	h.DataSize = binary.BigEndian.Uint32(ds)
	skip := int64(h.NumEntries)*16 + int64(h.DataSize)
	if pad := (skip + 7) % 8; pad != 0 {
		skip += 8 - pad
	}
	if _, err := f.Seek(skip, io.SeekCurrent); err != nil {
		return h, err
	}
	return h, nil
}

// decompressRPMPayload detects and decompresses the cpio payload by magic
// bytes (gzip 1f 8b, xz fd 37 7a 58 5a, zstd 28 b5 2f fd).
func decompressRPMPayload(data []byte) (io.Reader, error) {
	switch {
	case len(data) >= 3 && data[0] == 0x1f && data[1] == 0x8b:
		return gzip.NewReader(bytes.NewReader(data))
	case len(data) >= 6 && data[0] == 0xfd && data[1] == '7' && data[2] == 'z' && data[3] == 'X' && data[4] == 'Z' && data[5] == 0x00:
		return xzReader(bytes.NewReader(data))
	case len(data) >= 4 && data[0] == 0x28 && data[1] == 0xb5 && data[2] == 0x2f && data[3] == 0xfd:
		return zstdReader(bytes.NewReader(data))
	default:
		return nil, fmt.Errorf("unknown rpm payload compression (magic % x)", data[:min(4, len(data))])
	}
}
