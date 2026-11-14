// Adapted from ibiscum/goplay2; modifications Copyright 2026 Station AirPlay contributors.
package homekit

type Configuration struct {
	DeviceName           string `plist:"Device_Name"`
	AccessControlEnabled bool   `plist:"Enable_HK_Access_Control"`
	AccessControlLevel   uint64 `plist:"Access_Control_Level"`
	Identifier           string `plist:"Identifier"`
	PublicKey            []byte `plist:"PublicKey"`
}
