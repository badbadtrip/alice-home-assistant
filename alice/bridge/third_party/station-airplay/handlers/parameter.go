// Adapted from goplay2; volume applies only to this receiver's PCM.
package handlers

import (
	"bufio"
	"bytes"
	"fmt"
	"github.com/skrashevich/station-airplay/rtsp"
	"math"
	"strconv"
	"strings"
)

func (r *Rstp) OnGetParameterWeb(req *rtsp.Request) (*rtsp.Response, error) {
	volume := r.currentVolume()
	// Alice patch: with the speaker's volume in charge, the sender starts from it.
	if r.player.SpeakerVolume != nil {
		if db, ok := r.player.SpeakerVolume(); ok {
			volume = db
		}
	}
	return &rtsp.Response{StatusCode: rtsp.StatusOK, Header: rtsp.Header{"Content-Type": {"text/parameters"}}, Body: []byte(fmt.Sprintf("volume: %.6f\r\n", volume))}, nil
}
func (r *Rstp) OnSetParameterWeb(req *rtsp.Request) (*rtsp.Response, error) {
	// Alice patch: what the sender actually sets (type, size, first text line),
	// to find where the phone's volume slider goes. Binary bodies are not shown.
	first, ct := "", ""
	if v := req.Header["Content-Type"]; len(v) > 0 {
		ct = v[0]
	}
	if strings.HasPrefix(ct, "text/") {
		first, _, _ = strings.Cut(string(req.Body), "\n")
		first = strings.TrimSpace(first)
		if len(first) > 60 {
			first = first[:60]
		}
	}
	r.logger.Printf("audio set_parameter type=%q bytes=%d first=%q hooked=%v", ct, len(req.Body), first, r.player.SenderVolume != nil)
	scanner := bufio.NewScanner(bytes.NewReader(req.Body))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "volume:") {
			v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, "volume:")), 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v > 0 || v < -144 {
				return &rtsp.Response{StatusCode: rtsp.StatusBadRequest}, nil
			}
			if len(r.streams) == 0 {
				return &rtsp.Response{StatusCode: rtsp.StatusMethodNotValidInThisState}, nil
			}
			r.volume = v
			gain := v
			// Alice patch: the speaker takes the slider; locally only silence.
			if r.player.SenderVolume != nil && r.player.SenderVolume(v) {
				gain = 0
				if v <= -144 {
					gain = -144
				}
			}
			for _, s := range r.streams {
				r.player.SetVolumeFor(s, gain)
			}
		}
	}
	return &rtsp.Response{StatusCode: rtsp.StatusOK}, nil
}

func (r *Rstp) currentVolume() float64 {
	owner := r.player.Current()
	for _, s := range r.streams {
		if s == owner {
			return r.player.Volume()
		}
	}
	return r.volume
}
