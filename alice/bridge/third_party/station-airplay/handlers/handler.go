// Adapted from goplay2; every control connection owns its handler and streams.
package handlers

import (
	"fmt"
	"github.com/brutella/hc/hap/pair"
	"github.com/skrashevich/station-airplay/audio"
	"github.com/skrashevich/station-airplay/event"
	"github.com/skrashevich/station-airplay/homekit"
	"github.com/skrashevich/station-airplay/pairing"
	"github.com/skrashevich/station-airplay/rtsp"
	"log"
	"net"
	"time"
)

type Rstp struct {
	eventKey     []byte
	eventSession *event.Session
	server       *homekit.Server
	accessory    *homekit.Accessory
	logger       *log.Logger
	streams      map[string]*audio.Server
	pairing      *pairing.Controller
	player       *audio.Player
	peerIP       net.IP
	peers        []net.IP
	clockID      uint64
	volume       float64
	transient    *pair.SetupServerSession
}

func NewRstpHandler(server *homekit.Server, accessory *homekit.Accessory, p *audio.Player) (*Rstp, error) {
	if server == nil || accessory == nil || p == nil {
		return nil, fmt.Errorf("receiver handler dependencies are required")
	}
	logger := p.Logger
	if logger == nil {
		logger = log.Default()
	}
	return &Rstp{server: server, accessory: accessory, logger: logger, streams: make(map[string]*audio.Server), pairing: pairing.NewController(server.Database), player: p, volume: p.Volume()}, nil
}
func (r *Rstp) OnConnOpen(c *rtsp.Conn) {
	r.peerIP = c.NetConn().RemoteAddr().(*net.TCPAddr).IP
	c.SetNetConn(homekit.NewConnection(c.NetConn(), r.server.Context))
}
func (r *Rstp) OnConnClose(c *rtsp.Conn) {
	if r.eventSession != nil {
		r.eventSession.Close()
	}
	clear(r.eventKey)
	r.eventKey = nil
	for _, s := range r.streams {
		s.Close()
		s.Wait()
	}
	r.server.Context.DeleteSessionForConnection(c.NetConn())
}
func (r *Rstp) OnRequest(_ *rtsp.Conn, req *rtsp.Request) {
	r.logger.Printf("RTSP %s /%s", req.Method, req.Path)
}
func (r *Rstp) OnResponse(_ *rtsp.Conn, _ *rtsp.Response) {}
func (r *Rstp) Handle(c *rtsp.Conn, req *rtsp.Request) (response *rtsp.Response, failure error) {
	defer func() {
		if recover() != nil {
			response = &rtsp.Response{StatusCode: rtsp.StatusBadRequest}
			failure = nil
		}
		if r.logger != nil && response != nil {
			cseq := ""
			if values := req.Header["CSeq"]; len(values) > 0 {
				cseq = values[0]
			}
			// Never log request/response bodies or arbitrary headers: pairing and
			// stream setup carry private key material even over encrypted RTSP.
			r.logger.Printf("RTSP response peer=%q method=%s path=%.256q cseq=%.32q status=%d", r.peerIP, req.Method, req.Path, cseq, response.StatusCode)
		}
	}()
	if req.Method == "OPTIONS" {
		return &rtsp.Response{StatusCode: rtsp.StatusOK, Header: rtsp.Header{"Public": {"GET, POST, SETUP, GET_PARAMETER, SET_PARAMETER, RECORD, SETPEERS, SETRATEANCHORTIME, FLUSH, FLUSHBUFFERED, TEARDOWN"}}}, nil
	}
	if req.Method == "GET" {
		return r.OnGetWeb(req)
	}
	if req.Method == "POST" && (req.Path == "pair-setup" || req.Path == "pair-verify") {
		return r.OnPostWeb(c, req)
	}
	session := r.server.Context.GetSessionForConnection(c.NetConn())
	if session == nil || session.Encrypter() == nil {
		return &rtsp.Response{StatusCode: rtsp.StatusConnectionAuthorizationRequired}, nil
	}
	switch req.Method {
	case "POST":
		return r.OnPostWeb(c, req)
	case "SETUP":
		return r.OnSetupWeb(req)
	case "GET_PARAMETER":
		return r.OnGetParameterWeb(req)
	case "SET_PARAMETER":
		return r.OnSetParameterWeb(req)
	case "RECORD":
		return r.OnRecordWeb(req)
	case "SETPEERS", "SETPEERSX":
		return r.OnSetPeerWeb(req)
	case "SETRATEANCHORTIME":
		return r.OnSetRateAnchorTime(req)
	case "FLUSHBUFFERED":
		return r.OnFlushBuffered(req)
	case "FLUSH":
		return r.OnFlushWeb(req)
	case "TEARDOWN":
		return r.OnTeardownWeb(req)
	}
	return &rtsp.Response{StatusCode: rtsp.StatusNotImplemented}, nil
}

// A claimed sender may remain paused indefinitely; control EOF or application
// cancellation still releases it. Other connections retain the bounded timeout.
func (r *Rstp) ReadTimeout() time.Duration {
	owner := r.player.Current()
	for _, s := range r.streams {
		if s == owner {
			return 0
		}
	}
	return 5 * time.Minute
}
