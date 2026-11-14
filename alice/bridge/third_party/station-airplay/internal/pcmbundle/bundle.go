// Package pcmbundle stores the isolated native PCM helper inside a static host
// executable. No output files are needed beside the installed executable.
package pcmbundle

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	magic           = "station-airplay-pcm-v1"
	manifestEntry   = "pcm/manifest.json"
	maxHelperSize   = 32 << 20
	maxManifestSize = 64 << 10
)

type helperInfo struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type manifest struct {
	HelperABI int                   `json:"helper_abi"`
	HostSize  int64                 `json:"host_size"`
	Helpers   map[string]helperInfo `json:"helpers"`
}
type bundle struct {
	files    map[string]*zip.File
	manifest manifest
	hostSize int64
}

func helperEntry(arch string) string { return "pcm/" + arch }

func elfABI(data []byte) (string, error) {
	if len(data) < 20 || string(data[:4]) != "\x7fELF" || data[5] != 1 {
		return "", errors.New("not a little-endian ELF")
	}
	machine := binary.LittleEndian.Uint16(data[18:20])
	switch {
	case data[4] == 1 && machine == 40:
		return "armv7", nil
	case data[4] == 2 && machine == 183:
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported ELF class %d/machine %d", data[4], machine)
	}
}
func readBounded(ctx context.Context, entry *zip.File, limit int64) ([]byte, error) {
	if entry.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("PCM payload entry %q is too large", entry.Name)
	}
	reader, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx, reader}, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("PCM payload entry %q exceeds its size limit", entry.Name)
	}
	return data, nil
}
func openBundle(ctx context.Context, file *os.File, size int64, strictHost bool) (*bundle, error) {
	header := make([]byte, 20)
	if _, err := file.ReadAt(header, 0); err != nil || string(header[:4]) != "\x7fELF" || (header[4] != 1 && header[4] != 2) || header[5] != 1 {
		return nil, errors.New("PCM payload requires a little-endian ELF host executable")
	}
	reader, err := zip.NewReader(file, size)
	if err != nil {
		return nil, fmt.Errorf("PCM payload is missing or corrupt; build with station-pcm-package: %w", err)
	}
	if reader.Comment != magic {
		return nil, errors.New("PCM payload is missing; build the executable with station-pcm-package")
	}
	// UPX can copy an existing overlay and add its own trailer after it. That
	// trailer is part of the packed host, so stripping at the ZIP start would
	// silently lose it. Producers must remove our payload before compression.
	end := make([]byte, 22+len(magic))
	if size < int64(len(end)) {
		return nil, errors.New("invalid PCM payload end record")
	}
	if _, err = file.ReadAt(end, size-int64(len(end))); err != nil {
		return nil, err
	}
	if string(end[:4]) != "PK\x05\x06" || int(binary.LittleEndian.Uint16(end[20:])) != len(magic) || string(end[22:]) != magic {
		return nil, errors.New("PCM payload has trailing bytes; compress before bundling (strip the payload first)")
	}
	if len(reader.File) < 2 || len(reader.File) > 3 {
		return nil, errors.New("invalid PCM payload entry count")
	}
	b := &bundle{files: make(map[string]*zip.File), hostSize: size}
	for _, entry := range reader.File {
		if entry.Name != manifestEntry && entry.Name != helperEntry("armv7") && entry.Name != helperEntry("arm64") {
			return nil, fmt.Errorf("unknown PCM payload entry %q", entry.Name)
		}
		if _, exists := b.files[entry.Name]; exists {
			return nil, fmt.Errorf("duplicate PCM payload entry %q", entry.Name)
		}
		if entry.Method != zip.Store && entry.Method != zip.Deflate {
			return nil, errors.New("unsupported PCM payload compression")
		}
		if entry.UncompressedSize64 > maxHelperSize {
			return nil, errors.New("PCM payload entry is too large")
		}
		offset, err := entry.DataOffset()
		if err != nil {
			return nil, err
		}
		// Our writer uses no extra fields. Deriving the actual local-header offset
		// also recognizes an overlay copied by UPX after changing the ELF prefix.
		start := offset - 30 - int64(len(entry.Name)) - int64(len(entry.Extra))
		header := make([]byte, 30)
		if start < 20 {
			return nil, errors.New("invalid PCM payload host boundary")
		}
		if _, err = file.ReadAt(header, start); err != nil {
			return nil, err
		}
		if string(header[:4]) != "PK\x03\x04" || int(binary.LittleEndian.Uint16(header[26:])) != len(entry.Name) || int(binary.LittleEndian.Uint16(header[28:])) != len(entry.Extra) {
			return nil, errors.New("invalid PCM payload local header")
		}
		b.hostSize = min(b.hostSize, start)
		b.files[entry.Name] = entry
	}
	entry := b.files[manifestEntry]
	if entry == nil {
		return nil, errors.New("PCM payload manifest is missing")
	}
	data, err := readBounded(ctx, entry, maxManifestSize)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&b.manifest); err != nil {
		return nil, fmt.Errorf("PCM payload manifest: %w", err)
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("PCM payload manifest has trailing data")
	}
	if b.manifest.HelperABI != 1 || b.manifest.HostSize < 20 || len(b.manifest.Helpers) < 1 || len(b.manifest.Helpers) > 2 {
		return nil, errors.New("unsupported PCM payload manifest or helper ABI")
	}
	if strictHost && b.manifest.HostSize != b.hostSize {
		return nil, errors.New("PCM payload host size differs; re-bundle after modifying the executable")
	}
	for arch, info := range b.manifest.Helpers {
		if arch != "armv7" && arch != "arm64" {
			return nil, errors.New("unsupported PCM payload helper ABI")
		}
		entry := b.files[helperEntry(arch)]
		digest, err := hex.DecodeString(info.SHA256)
		if err != nil || len(digest) != sha256.Size || info.Size < 20 || info.Size > maxHelperSize || entry == nil || entry.UncompressedSize64 != uint64(info.Size) {
			return nil, errors.New("invalid PCM payload helper metadata")
		}
	}
	if len(b.files) != len(b.manifest.Helpers)+1 {
		return nil, errors.New("PCM payload has an undeclared helper")
	}
	return b, nil
}

// Append atomically adds or replaces a versioned PCM payload, keeping the host
// ELF bytes and permissions. It never requires files beside the published host.
func Append(ctx context.Context, executable string, helpers map[string][]byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(helpers) < 1 || len(helpers) > 2 {
		return errors.New("one or two PCM helper ABIs are required")
	}
	info, err := os.Lstat(executable)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("PCM host must be a regular ELF file")
	}
	input, err := os.Open(executable)
	if err != nil {
		return err
	}
	defer input.Close()
	header := make([]byte, 20)
	if _, err = input.ReadAt(header, 0); err != nil {
		return err
	}
	if string(header[:4]) != "\x7fELF" || header[5] != 1 {
		return errors.New("PCM host must be a little-endian ELF")
	}
	hostSize := info.Size()
	if reader, zipErr := zip.NewReader(input, info.Size()); zipErr == nil && reader.Comment == magic {
		existing, err := openBundle(ctx, input, info.Size(), false)
		if err != nil {
			return err
		}
		hostSize = existing.hostSize
	}
	m := manifest{HelperABI: 1, HostSize: hostSize, Helpers: make(map[string]helperInfo)}
	for arch, data := range helpers {
		if len(data) > maxHelperSize {
			return fmt.Errorf("%s PCM helper exceeds size limit", arch)
		}
		actual, err := elfABI(data)
		if err != nil || actual != arch {
			return fmt.Errorf("%s PCM helper has an incompatible ELF ABI", arch)
		}
		digest := sha256.Sum256(data)
		m.Helpers[arch] = helperInfo{hex.EncodeToString(digest[:]), int64(len(data))}
	}
	metadata, err := json.Marshal(m)
	if err != nil {
		return err
	}
	output, err := os.CreateTemp(filepath.Dir(executable), ".pcm-bundle-*")
	if err != nil {
		return err
	}
	temp := output.Name()
	defer os.Remove(temp)
	defer output.Close()
	if _, err = input.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err = io.CopyN(output, contextReader{ctx, input}, hostSize); err != nil {
		return err
	}
	writer := zip.NewWriter(output)
	writer.SetOffset(hostSize)
	if err = writer.SetComment(magic); err != nil {
		return err
	}
	entry, err := writer.Create(manifestEntry)
	if err != nil {
		return err
	}
	if _, err = entry.Write(metadata); err != nil {
		return err
	}
	for _, arch := range []string{"armv7", "arm64"} {
		data, exists := helpers[arch]
		if !exists {
			continue
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		entry, err = writer.Create(helperEntry(arch))
		if err != nil {
			return err
		}
		if _, err = entry.Write(data); err != nil {
			return err
		}
	}
	if err = writer.Close(); err != nil {
		return err
	}
	if err = output.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if err = output.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return os.Rename(temp, executable)
}

type contextReader struct {
	context context.Context
	reader  io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.context.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
