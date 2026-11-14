package pcmbundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Resolve validates the executable payload and the platform ABI, then publishes
// only its selected helper in a private, content-addressed runtime cache. It
// returns child-only environment additions; it never changes process variables.
func Resolve(ctx context.Context, executable, dataDir string) (string, []string, error) {
	return resolve(ctx, executable, dataDir, nativeEnvironment)
}
func resolve(ctx context.Context, executable, dataDir string, selectNative func() (string, []string, error)) (string, []string, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	file, err := os.Open(executable)
	if err != nil {
		return "", nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", nil, err
	}
	b, err := openBundle(ctx, file, info.Size(), true)
	if err != nil {
		return "", nil, err
	}
	arch, env, err := selectNative()
	if err != nil {
		return "", nil, err
	}
	helper, exists := b.manifest.Helpers[arch]
	if !exists {
		return "", nil, fmt.Errorf("PCM payload does not contain the %s helper", arch)
	}
	data, err := readBounded(ctx, b.files[helperEntry(arch)], maxHelperSize)
	if err != nil {
		return "", nil, fmt.Errorf("read PCM helper: %w", err)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != helper.SHA256 {
		return "", nil, errors.New("PCM helper SHA-256 mismatch")
	}
	actual, err := elfABI(data)
	if err != nil || actual != arch {
		return "", nil, errors.New("PCM helper ELF ABI differs from its manifest")
	}
	if dataDir == "" {
		return "", nil, errors.New("private PCM data directory is required")
	}
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if err = os.MkdirAll(dataDir, 0o700); err != nil {
		return "", nil, err
	}
	root := filepath.Join(dataDir, "pcm")
	if err = privateDir(root); err != nil {
		return "", nil, err
	}
	directory := filepath.Join(root, helper.SHA256)
	if err = privateDir(directory); err != nil {
		return "", nil, err
	}
	command := filepath.Join(directory, "station-pcm")
	if validCache(ctx, command, data) {
		if err = os.Chmod(command, 0o700); err != nil {
			return "", nil, err
		}
		return command, env, ctx.Err()
	}
	output, err := os.CreateTemp(directory, ".station-pcm-*")
	if err != nil {
		return "", nil, err
	}
	temp := output.Name()
	defer os.Remove(temp)
	defer output.Close()
	if _, err = io.Copy(output, contextReader{ctx, bytes.NewReader(data)}); err != nil {
		return "", nil, err
	}
	if err = output.Chmod(0o700); err != nil {
		return "", nil, err
	}
	if err = output.Close(); err != nil {
		return "", nil, err
	}
	if err = ctx.Err(); err != nil {
		return "", nil, err
	}
	if err = os.Rename(temp, command); err != nil {
		return "", nil, err
	}
	return command, env, nil
}
func privateDir(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("PCM cache directory is not a private directory: %s", path)
	}
	return os.Chmod(path, 0o700)
}
func validCache(ctx context.Context, path string, expected []byte) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(expected)) {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	digest := sha256.New()
	if _, err = io.Copy(digest, contextReader{ctx, file}); err != nil {
		return false
	}
	want := sha256.Sum256(expected)
	return bytes.Equal(digest.Sum(nil), want[:])
}
