package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Identity struct {
	UUID     string `json:"uuid"`
	DeviceID string `json:"device_id"`
}

var macPattern = regexp.MustCompile(`^[0-9A-F]{2}(:[0-9A-F]{2}){5}$`)

func LoadIdentity(dir string) (Identity, error) {
	var id Identity
	if err := os.MkdirAll(dir, 0700); err != nil {
		return id, err
	}
	path := filepath.Join(dir, "identity.json")
	b, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(b, &id); err != nil {
			return id, fmt.Errorf("invalid persisted identity: %w", err)
		}
		if _, err = uuid.Parse(id.UUID); err != nil || !macPattern.MatchString(id.DeviceID) {
			return id, fmt.Errorf("invalid persisted identity values")
		}
		return id, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return id, err
	}
	var mac [6]byte
	if _, err = rand.Read(mac[:]); err != nil {
		return id, err
	}
	mac[0] = (mac[0] | 2) & 0xfe
	id = Identity{UUID: uuid.NewString(), DeviceID: fmt.Sprintf("%02X:%02X:%02X:%02X:%02X:%02X", mac[0], mac[1], mac[2], mac[3], mac[4], mac[5])}
	b, err = json.MarshalIndent(id, "", "  ")
	if err != nil {
		return id, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return LoadIdentity(dir)
	}
	if err != nil {
		return id, err
	}
	_, writeErr := f.Write(append(b, '\n'))
	syncErr := f.Sync()
	closeErr := f.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		return id, err
	}
	return id, nil
}
func (i Identity) ClockID() uint64 {
	b, _ := hex.DecodeString(strings.ReplaceAll(i.DeviceID, ":", ""))
	if len(b) != 6 {
		return 0
	}
	return uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | 0xfffe<<24 | uint64(b[3])<<16 | uint64(b[4])<<8 | uint64(b[5])
}
