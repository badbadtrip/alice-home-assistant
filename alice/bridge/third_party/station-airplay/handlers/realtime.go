package handlers

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/skrashevich/station-airplay/rtsp"
)

func realtimeFlushTimestamp(value string) (uint32, bool, error) {
	var rtp uint32
	seenRTP, seenSequence := false, false
	for field := range strings.SplitSeq(value, ";") {
		key, raw, ok := strings.Cut(strings.TrimSpace(field), "=")
		if !ok {
			return 0, false, fmt.Errorf("invalid RTP-Info field")
		}
		key, raw = strings.TrimSpace(key), strings.TrimSpace(raw)
		switch key {
		case "rtptime":
			v, err := strconv.ParseUint(raw, 10, 32)
			if err != nil || seenRTP {
				return 0, false, fmt.Errorf("invalid RTP timestamp")
			}
			rtp, seenRTP = uint32(v), true
		case "seq":
			if _, err := strconv.ParseUint(raw, 10, 16); err != nil || seenSequence {
				return 0, false, fmt.Errorf("invalid RTP sequence")
			}
			seenSequence = true
		}
	}
	return rtp, seenRTP, nil
}

func (r *Rstp) OnFlushWeb(req *rtsp.Request) (*rtsp.Response, error) {
	s := r.streams[req.Path]
	if s == nil {
		return &rtsp.Response{StatusCode: rtsp.StatusSessionNotFound}, nil
	}
	if !s.IsRealtime() {
		return &rtsp.Response{StatusCode: rtsp.StatusMethodNotValidInThisState}, nil
	}
	values := req.Header["RTP-Info"]
	if len(values) == 0 {
		s.FlushRealtimeAll()
		return &rtsp.Response{StatusCode: rtsp.StatusOK}, nil
	}
	if len(values) > 1 {
		return &rtsp.Response{StatusCode: rtsp.StatusBadRequest}, nil
	}
	rtp, haveTimestamp, err := realtimeFlushTimestamp(values[0])
	if err != nil {
		return &rtsp.Response{StatusCode: rtsp.StatusBadRequest}, nil
	}
	if haveTimestamp {
		s.FlushRealtime(rtp)
	} else {
		s.FlushRealtimeAll()
	}
	return &rtsp.Response{StatusCode: rtsp.StatusOK}, nil
}
