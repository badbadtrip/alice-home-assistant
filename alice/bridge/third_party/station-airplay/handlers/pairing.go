// Adapted from ibiscum/goplay2; modifications Copyright 2026 Station AirPlay contributors.
package handlers

import (
	"bytes"

	"github.com/brutella/hc/hap"
	"github.com/brutella/hc/hap/pair"
	"github.com/brutella/hc/util"
	"github.com/skrashevich/station-airplay/homekit"
	"github.com/skrashevich/station-airplay/rtsp"
	"howett.net/plist"
	"strings"
)

func (r *Rstp) OnPairSetup(conn *rtsp.Conn, req *rtsp.Request) (*rtsp.Response, error) {
	in, err := util.NewTLV8ContainerFromReader(bytes.NewReader(req.Body))
	if err != nil {
		return &rtsp.Response{StatusCode: rtsp.StatusBadRequest}, nil
	}
	session := r.server.Context.GetSessionForConnection(conn.NetConn())
	sequence := in.GetByte(pair.TagSequence)
	flags := in.GetBytes(19)
	if sequence == 1 && len(flags) > 0 && flags[0]&16 != 0 {
		if in.GetByte(pair.TagPairingMethod) != 0 {
			return &rtsp.Response{StatusCode: rtsp.StatusBadRequest}, nil
		}
		transient, err := pair.NewSetupServerSession("Pair-Setup", "3939")
		if err != nil {
			return nil, err
		}
		r.transient = transient
		out := util.NewTLV8Container()
		out.SetByte(pair.TagSequence, 2)
		out.SetBytes(pair.TagSalt, transient.Salt)
		out.SetBytes(pair.TagPublicKey, transient.PublicKey)
		return &rtsp.Response{StatusCode: rtsp.StatusOK, Body: out.BytesBuffer().Bytes()}, nil
	}
	if r.transient != nil {
		transient := r.transient
		r.transient = nil
		if sequence != 3 || len(in.GetBytes(pair.TagPublicKey)) != 384 || len(in.GetBytes(pair.TagProof)) != 64 {
			return &rtsp.Response{StatusCode: rtsp.StatusBadRequest}, nil
		}
		if err := transient.SetupPrivateKeyFromClientPublicKey(in.GetBytes(pair.TagPublicKey)); err != nil {
			return &rtsp.Response{StatusCode: rtsp.StatusBadRequest}, nil
		}
		proof, err := transient.ProofFromClientProof(in.GetBytes(pair.TagProof))
		if err != nil {
			return &rtsp.Response{StatusCode: rtsp.StatusUnauthorized}, nil
		}
		cryptographer, err := homekit.NewControlSession(transient.PrivateKey, false)
		if err != nil {
			return nil, err
		}
		out := util.NewTLV8Container()
		out.SetByte(pair.TagSequence, 4)
		out.SetBytes(pair.TagProof, proof)
		conn.AfterResponse = func() {
			session.SetCryptographer(cryptographer)
			clear(r.eventKey)
			r.eventKey = append([]byte(nil), transient.PrivateKey...)
		}
		return &rtsp.Response{StatusCode: rtsp.StatusOK, Body: out.BytesBuffer().Bytes()}, nil
	}
	ctrl := session.PairSetupHandler()
	if ctrl == nil || sequence == 1 {
		controller, err := pair.NewSetupServerController(r.server.Device, r.server.Database)
		if err != nil {
			return nil, err
		}
		ctrl = controller
		session.SetPairSetupHandler(ctrl)
	}
	if sequence == 5 && len(in.GetBytes(pair.TagEncryptedData)) < 16 {
		return &rtsp.Response{StatusCode: rtsp.StatusBadRequest}, nil
	}
	out, err := ctrl.Handle(in)
	if err != nil {
		return &rtsp.Response{StatusCode: rtsp.StatusBadRequest}, nil
	}
	return &rtsp.Response{StatusCode: rtsp.StatusOK, Body: out.BytesBuffer().Bytes()}, nil
}

func (r *Rstp) OnPairVerify(conn *rtsp.Conn, req *rtsp.Request) (*rtsp.Response, error) {

	key := conn.NetConn().RemoteAddr().String()
	session := r.server.Context.Get(key).(hap.Session)

	ctlr := session.PairVerifyHandler()
	if ctlr == nil {
		ctlr = pair.NewVerifyServerController(r.server.Database, r.server.Context)
		session.SetPairVerifyHandler(ctlr)
	}

	var err error
	var in util.Container
	var out util.Container
	var secSession *homekit.ControlSession

	if in, err = util.NewTLV8ContainerFromReader(bytes.NewReader(req.Body)); err == nil {
		out, err = ctlr.Handle(in)
	}

	if err != nil {
		return &rtsp.Response{StatusCode: rtsp.StatusInternalServerError}, err
	}
	body := out.BytesBuffer().Bytes()

	// When key verification is done, switch to a secure session
	// based on the negotiated shared session key
	b := out.GetByte(pair.TagSequence)
	switch pair.VerifyStepType(b) {
	case pair.VerifyStepFinishResponse:
		key := ctlr.SharedKey()
		if secSession, err = homekit.NewControlSession(key[:], false); err == nil {
			conn.AfterResponse = func() {
				session.SetCryptographer(secSession)
				clear(r.eventKey)
				r.eventKey = append([]byte(nil), key[:]...)
			}
		} else {
			return &rtsp.Response{StatusCode: rtsp.StatusInternalServerError}, err
		}
	}

	return &rtsp.Response{StatusCode: rtsp.StatusOK, Body: body}, nil
}

func (r *Rstp) OnPairAdd(conn *rtsp.Conn, req *rtsp.Request) (*rtsp.Response, error) {

	if contentType, found := req.Header["Content-Type"]; found && strings.EqualFold(contentType[0], "application/x-apple-binary-plist") {
		if container, err := util.NewTLV8ContainerFromReader(bytes.NewReader(req.Body)); err == nil {
			outputContainer, err := r.pairing.Handle(container)
			if err != nil {
				return &rtsp.Response{StatusCode: rtsp.StatusInternalServerError}, nil
			}
			return &rtsp.Response{StatusCode: rtsp.StatusOK, Body: outputContainer.BytesBuffer().Bytes()}, nil
		}

	}
	return &rtsp.Response{StatusCode: rtsp.StatusOK}, nil
}

func (r *Rstp) OnPairRemove(conn *rtsp.Conn, req *rtsp.Request) (*rtsp.Response, error) {

	if contentType, found := req.Header["Content-Type"]; found && strings.EqualFold(contentType[0], "application/x-apple-binary-plist") {
		if container, err := util.NewTLV8ContainerFromReader(bytes.NewReader(req.Body)); err == nil {
			outputContainer, err := r.pairing.Handle(container)
			if err != nil {
				return &rtsp.Response{StatusCode: rtsp.StatusInternalServerError}, nil
			}
			return &rtsp.Response{StatusCode: rtsp.StatusOK, Body: outputContainer.BytesBuffer().Bytes()}, nil
		}

	}
	return &rtsp.Response{StatusCode: rtsp.StatusOK}, nil
}

func (r *Rstp) OnPairList(conn *rtsp.Conn, req *rtsp.Request) (*rtsp.Response, error) {

	if contentType, found := req.Header["Content-Type"]; found && strings.EqualFold(contentType[0], "application/x-apple-binary-plist") {
		if container, err := util.NewTLV8ContainerFromReader(bytes.NewReader(req.Body)); err == nil {
			outputContainer, err := r.pairing.Handle(container)
			if err != nil {
				return &rtsp.Response{StatusCode: rtsp.StatusInternalServerError}, nil
			}
			return &rtsp.Response{StatusCode: rtsp.StatusOK, Body: outputContainer.BytesBuffer().Bytes()}, nil
		}
	}
	return &rtsp.Response{StatusCode: rtsp.StatusOK}, nil
}

func (r *Rstp) OnPairConfigure(req *rtsp.Request) (*rtsp.Response, error) {

	if contentType, found := req.Header["Content-Type"]; found && strings.EqualFold(contentType[0], "application/x-apple-binary-plist") {
		var content map[string]interface{}
		if _, err := plist.Unmarshal(req.Body, &content); err != nil {
			return &rtsp.Response{StatusCode: rtsp.StatusInternalServerError}, nil
		}
	}

	config := homekit.Configuration{
		DeviceName:           r.server.Name,
		AccessControlEnabled: true,
		AccessControlLevel:   0,
		Identifier:           r.accessory.Pi.String(),
		PublicKey:            r.server.Device.PublicKey(),
	}

	if body, err := plist.Marshal(config, plist.AutomaticFormat); err == nil {
		return &rtsp.Response{StatusCode: rtsp.StatusOK, Header: rtsp.Header{
			"Content-Type": rtsp.HeaderValue{"application/x-apple-binary-plist"},
		}, Body: body}, nil
	}
	return &rtsp.Response{StatusCode: rtsp.StatusInternalServerError}, nil
}
