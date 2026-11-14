// Adapted from goplay2; typed control bodies avoid unchecked map assertions.
package handlers

import (
	"github.com/skrashevich/station-airplay/audio"
	"github.com/skrashevich/station-airplay/rtsp"
	"howett.net/plist"
	"math/bits"
	"time"
)

type setRateAnchorTime struct {
	Rate          uint32 `plist:"rate"`
	RtpTime       uint32 `plist:"rtpTime"`
	Fraction      uint64 `plist:"networkTimeFrac"`
	Seconds       uint64 `plist:"networkTimeSecs"`
	ClockIdentity uint64 `plist:"networkTimeTimelineID"`
}

func (s *setRateAnchorTime) StartTime() time.Time {
	ns, _ := bits.Mul64(s.Fraction, 1_000_000_000)
	return time.Unix(int64(s.Seconds), int64(ns))
}
func (r *Rstp) OnRecordWeb(req *rtsp.Request) (*rtsp.Response, error) {
	return &rtsp.Response{StatusCode: rtsp.StatusOK}, nil
}
func (r *Rstp) OnAudioMode(req *rtsp.Request) (*rtsp.Response, error) {
	return &rtsp.Response{StatusCode: rtsp.StatusOK}, nil
}
func (r *Rstp) OnCommand(req *rtsp.Request) (*rtsp.Response, error) {
	return &rtsp.Response{StatusCode: rtsp.StatusOK}, nil
}
func (r *Rstp) OnSetRateAnchorTime(req *rtsp.Request) (*rtsp.Response, error) {
	var v setRateAnchorTime
	if _, err := plist.Unmarshal(req.Body, &v); err != nil || v.Rate > 1 || v.Seconds > 1<<63-1 {
		return &rtsp.Response{StatusCode: rtsp.StatusBadRequest}, nil
	}
	s := r.streams[req.Path]
	if s == nil {
		return &rtsp.Response{StatusCode: rtsp.StatusSessionNotFound}, nil
	}
	if v.Rate == 0 {
		s.SetRate0()
	} else {
		r.logger.Printf("audio anchor rate=%d rtp=%d clock=%016x seconds=%d", v.Rate, v.RtpTime, v.ClockIdentity, v.Seconds)
		r.clockID = v.ClockIdentity
		s.SetTimingPeers(r.peers, r.clockID)
		s.SetRateAnchorTime(v.RtpTime, v.StartTime())
	}
	return &rtsp.Response{StatusCode: rtsp.StatusOK}, nil
}
func (r *Rstp) OnTeardownWeb(req *rtsp.Request) (*rtsp.Response, error) {
	var body map[string]any
	if len(req.Body) > 0 {
		if _, err := plist.Unmarshal(req.Body, &body); err != nil {
			return &rtsp.Response{StatusCode: rtsp.StatusBadRequest}, nil
		}
	}
	if s := r.streams[req.Path]; s != nil {
		r.volume = r.currentVolume()
		s.Close()
		s.Wait()
		if _, partial := body["streams"]; partial {
			replacement := audio.NewServer(r.player)
			r.attachRemote(replacement)
			replacement.PeerIP = r.peerIP
			r.streams[req.Path] = replacement
		} else {
			delete(r.streams, req.Path)
		}
	}
	return &rtsp.Response{StatusCode: rtsp.StatusOK}, nil
}
func (r *Rstp) OnFlushBuffered(req *rtsp.Request) (*rtsp.Response, error) {
	var v struct {
		From  uint64 `plist:"flushFromSeq"`
		Until uint64 `plist:"flushUntilSeq"`
		// Alice patch: logged only, to see what the sender asks for on pause.
		FromTS  *uint64 `plist:"flushFromTS"`
		UntilTS uint64  `plist:"flushUntilTS"`
	}
	if _, err := plist.Unmarshal(req.Body, &v); err != nil || v.From > 0xffffff || v.Until > 0xffffff {
		return &rtsp.Response{StatusCode: rtsp.StatusBadRequest}, nil
	}
	fromTS := int64(-1)
	if v.FromTS != nil {
		fromTS = int64(*v.FromTS)
	}
	r.logger.Printf("audio flush from_seq=%d until_seq=%d from_ts=%d until_ts=%d", v.From, v.Until, fromTS, v.UntilTS)
	if s := r.streams[req.Path]; s != nil {
		s.Flush(v.From, v.Until)
	}
	return &rtsp.Response{StatusCode: rtsp.StatusOK}, nil
}
