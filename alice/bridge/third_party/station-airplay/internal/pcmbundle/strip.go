package pcmbundle

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Strip atomically removes this package's validated payload before a producer
// compresses the host. It preserves the complete ELF prefix and permissions.
// A plain ELF or a foreign ZIP overlay is left unchanged and returns false.
func Strip(ctx context.Context, executable string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	info, err := os.Lstat(executable)
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("PCM host must be a regular ELF file")
	}
	input, err := os.Open(executable)
	if err != nil {
		return false, err
	}
	defer input.Close()
	header := make([]byte, 20)
	if _, err = input.ReadAt(header, 0); err != nil {
		return false, err
	}
	if string(header[:4]) != "\x7fELF" || (header[4] != 1 && header[4] != 2) || header[5] != 1 {
		return false, errors.New("PCM host must be a little-endian ELF")
	}
	reader, err := zip.NewReader(input, info.Size())
	if err != nil || reader.Comment != magic {
		return false, nil
	}
	b, err := openBundle(ctx, input, info.Size(), true)
	if err != nil {
		return false, err
	}
	output, err := os.CreateTemp(filepath.Dir(executable), ".pcm-strip-*")
	if err != nil {
		return false, err
	}
	temp := output.Name()
	defer os.Remove(temp)
	defer output.Close()
	if _, err = input.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	if _, err = io.CopyN(output, contextReader{ctx, input}, b.hostSize); err != nil {
		return false, err
	}
	if err = output.Chmod(info.Mode().Perm()); err != nil {
		return false, err
	}
	if err = output.Close(); err != nil {
		return false, err
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	if err = os.Rename(temp, executable); err != nil {
		return false, err
	}
	return true, nil
}
