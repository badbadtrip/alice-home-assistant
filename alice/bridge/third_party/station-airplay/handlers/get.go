// Adapted from ibiscum/goplay2; modifications Copyright 2026 Station AirPlay contributors.
package handlers

import (
	"github.com/skrashevich/station-airplay/rtsp"
	"howett.net/plist"
	"strings"
	"time"
)

type getInfoContent struct {
	Qualifier []string `plist:"qualifier"`
}

type audioLatenciesResponse struct {
	AudioType           string `plist:"audioType"`
	InputLatencyMicros  uint64 `plist:"inputLatencyMicros"`
	OutputLatencyMicros uint64 `plist:"outputLatencyMicros"`
	Type                uint64 `plist:"type"`
}

type getInfoResponse struct {
	AudioLatencies  []audioLatenciesResponse `plist:"audioLatencies"`
	DeviceId        string                   `plist:"deviceID"`
	Features        uint64                   `plist:"features"`
	Pi              string                   `plist:"pi"`
	Psi             string                   `plist:"psi"`
	ProtocolVersion string                   `plist:"protocolVersion"`
	Sdk             string                   `plist:"sdk"`
	SourceVersion   string                   `plist:"sourceVersion"`
	StatusFlags     uint64                   `plist:"statusFlags"`
}

func NewGetInfoResponse(deviceId string, features uint64, pi string,
	psi string, sourceVersion string) *getInfoResponse {

	latencies := [1]audioLatenciesResponse{{
		InputLatencyMicros:  0,
		OutputLatencyMicros: 400000,
		Type:                103,
	}}

	return &getInfoResponse{
		AudioLatencies:  latencies[:],
		DeviceId:        deviceId,
		Features:        features,
		Pi:              pi,
		Psi:             psi,
		ProtocolVersion: "1.1",
		Sdk:             "AirPlay;2.0.4",
		SourceVersion:   sourceVersion,
		StatusFlags:     0x4,
	}
}

func (r *Rstp) OnGetInfo(req *rtsp.Request) (*rtsp.Response, error) {

	if contentType, found := req.Header["Content-Type"]; found && strings.EqualFold(contentType[0], "application/x-apple-binary-plist") {
		var content getInfoContent
		if _, err := plist.Unmarshal(req.Body, &content); err != nil {
			return &rtsp.Response{StatusCode: rtsp.StatusBadRequest}, err
		}
	}

	responseBody := NewGetInfoResponse(r.accessory.Deviceid, r.accessory.Features.ToUint64(),
		r.accessory.Pi.String(), r.accessory.Psi.String(), r.accessory.Srcvers)

	responseBody.AudioLatencies[0].OutputLatencyMicros = uint64(r.player.OutputDelay / time.Microsecond)
	if body, err := plist.Marshal(*responseBody, plist.AutomaticFormat); err == nil {
		return &rtsp.Response{StatusCode: rtsp.StatusOK, Header: rtsp.Header{
			"Content-Type": rtsp.HeaderValue{"application/x-apple-binary-plist"},
		}, Body: body}, nil
	}

	return &rtsp.Response{StatusCode: rtsp.StatusNotImplemented}, nil
}

func (r *Rstp) OnGetWeb(req *rtsp.Request) (*rtsp.Response, error) {

	switch req.Path {
	case "info":
		return r.OnGetInfo(req)
	}
	return &rtsp.Response{StatusCode: rtsp.StatusNotImplemented}, nil
}
