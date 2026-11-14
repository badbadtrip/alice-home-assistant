// Adapted from ibiscum/goplay2; modifications Copyright 2026 Station AirPlay contributors.
package homekit

import (
	"github.com/brutella/hc/db"
	"github.com/brutella/hc/hap"
	"github.com/skrashevich/station-airplay/event"
	"path/filepath"
)

// reference : https://github.com/ejurgensen/pair_ap
const airportExpressHardcodedPin = "3939"

type Server struct {
	Events    *event.Hub
	Context   hap.Context
	Database  db.Database
	Device    hap.SecuredDevice
	Name      string
	Ips       []string
	EventPort uint16
}

func NewServer(macAddress string, deviceName string, ips []string) (*Server, error) {

	storage, err := NewPrivateStorage(filepath.Join(deviceName, "pairing"))
	if err != nil {
		return nil, err
	}
	database := db.NewDatabaseWithStorage(storage)

	device, err := hap.NewSecuredDevice(macAddress, airportExpressHardcodedPin, database)
	if err != nil {
		return nil, err
	}

	return &Server{Events: event.NewHub(), Context: hap.NewContextForSecuredDevice(device),
		Database: database,
		Device:   device,
		Name:     deviceName,
		Ips:      ips,
	}, nil
}
