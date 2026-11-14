package homekit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type PrivateStorage struct{ dir string }

func NewPrivateStorage(dir string) (*PrivateStorage, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return &PrivateStorage{dir: dir}, nil
}
func (s *PrivateStorage) path(key string) (string, error) {
	if key == "" || key == "." || key == ".." || filepath.Base(key) != key || strings.ContainsAny(key, "\\\x00") {
		return "", fmt.Errorf("invalid storage key")
	}
	return filepath.Join(s.dir, key), nil
}
func (s *PrivateStorage) Set(key string, b []byte) error {
	path, err := s.path(key)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".state-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func (s *PrivateStorage) Get(key string) ([]byte, error) {
	p, err := s.path(key)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}
func (s *PrivateStorage) Delete(key string) error {
	p, err := s.path(key)
	if err != nil {
		return err
	}
	return os.Remove(p)
}
func (s *PrivateStorage) KeysWithSuffix(suffix string) ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), suffix) && !strings.HasPrefix(e.Name(), ".state-") {
			keys = append(keys, e.Name())
		}
	}
	return keys, nil
}
