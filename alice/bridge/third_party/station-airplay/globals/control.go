// Adapted from ibiscum/goplay2; modifications Copyright 2026 Station AirPlay contributors.
package globals

type ControlMessageType uint8

type ControlMessage struct {
	MType  ControlMessageType
	Param1 int64
	Param2 int64
	Paramf float64
}

const (
	PAUSE ControlMessageType = iota
	START
	SKIP
	VOLUME
)
