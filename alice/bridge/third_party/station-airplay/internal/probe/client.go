// Package probe is a synthetic development sender, not an iPhone interoperability claim.
package probe

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"github.com/brutella/hc/hap"
	"github.com/brutella/hc/hap/pair"
	"github.com/brutella/hc/util"
	"github.com/skrashevich/go-aac/pkg/adts"
	"github.com/skrashevich/station-airplay/homekit"
	"github.com/skrashevich/station-airplay/rtsp"
	"golang.org/x/crypto/chacha20poly1305"
	"howett.net/plist"
	"net"
	"os"
	"strconv"
	"time"
)

type client struct {
	conn   *homekit.Connection
	reader *bufio.Reader
	cseq   int
}

func (c *client) request(method, path, contentType string, body []byte) (*rtsp.Response, error) {
	c.cseq++
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s %s RTSP/1.0\r\nCSeq: %d\r\nContent-Type: %s\r\nContent-Length: %d\r\n\r\n", method, path, c.cseq, contentType, len(body))
	b.Write(body)
	if _, err := c.conn.Write(b.Bytes()); err != nil {
		return nil, err
	}
	res := new(rtsp.Response)
	if err := res.Read(c.reader); err != nil {
		return nil, err
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("%s %s: status %d", method, path, res.StatusCode)
	}
	return res, nil
}
func (c *client) property(method, path string, value any) (*rtsp.Response, error) {
	b, err := plist.Marshal(value, plist.BinaryFormat)
	if err != nil {
		return nil, err
	}
	return c.request(method, path, "application/x-apple-binary-plist", b)
}
func Run(ctx context.Context, address, fixture string, volume float64) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := new(net.Dialer).DialContext(ctx, "tcp4", address)
	if err != nil {
		return err
	}
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(30 * time.Second))
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	hapContext := hap.NewContextForSecuredDevice(nil)
	conn := homekit.NewConnection(raw, hapContext)
	defer conn.Close()
	c := &client{conn: conn, reader: bufio.NewReader(conn)}
	m1 := util.NewTLV8Container()
	m1.SetByte(pair.TagSequence, 1)
	m1.SetByte(pair.TagPairingMethod, 0)
	m1.SetByte(19, 16)
	res, err := c.request("POST", "/pair-setup", "application/octet-stream", m1.BytesBuffer().Bytes())
	if err != nil {
		return err
	}
	m2, err := util.NewTLV8ContainerFromReader(bytes.NewReader(res.Body))
	if err != nil {
		return err
	}
	srp := pair.NewSetupClientSession("Pair-Setup", "3939")
	if err := srp.GenerateKeys(m2.GetBytes(pair.TagSalt), m2.GetBytes(pair.TagPublicKey)); err != nil {
		return err
	}
	m3 := util.NewTLV8Container()
	m3.SetByte(pair.TagSequence, 3)
	m3.SetBytes(pair.TagPublicKey, srp.PublicKey)
	m3.SetBytes(pair.TagProof, srp.Proof)
	res, err = c.request("POST", "/pair-setup", "application/octet-stream", m3.BytesBuffer().Bytes())
	if err != nil {
		return err
	}
	m4, err := util.NewTLV8ContainerFromReader(bytes.NewReader(res.Body))
	if err != nil {
		return err
	}
	if !srp.IsServerProofValid(m4.GetBytes(pair.TagProof)) {
		return fmt.Errorf("invalid SRP server proof")
	}
	secure, err := homekit.NewControlSession(srp.PrivateKey, true)
	if err != nil {
		return err
	}
	session := hapContext.GetSessionForConnection(conn)
	session.SetCryptographer(secure)
	session.Decrypter() // hc activates a pending cryptographer on the next read; the client writes first.
	if _, err = c.request("GET", "/info", "application/x-apple-binary-plist", nil); err != nil {
		return err
	}
	if _, err = c.property("SETUP", "/probe", map[string]any{"timingProtocol": "PTP"}); err != nil {
		return err
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		return err
	}
	header, err := adts.ReadHeaderFromBytes(data)
	if err != nil {
		return err
	}
	asc, err := adts.AudioSpecificConfig(header)
	if err != nil {
		return err
	}
	rate := 44100
	format := uint64(1 << 22)
	if asc == [2]byte{0x11, 0x90} {
		rate = 48000
		format = 1 << 24
	} else if asc != [2]byte{0x12, 0x10} {
		return fmt.Errorf("probe requires AAC-LC stereo 44100/48000")
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return err
	}
	res, err = c.property("SETUP", "/probe", map[string]any{"streams": []any{map[string]any{"type": uint64(103), "shk": key, "ct": uint64(4), "audioFormat": format, "spf": uint64(1024)}}})
	if err != nil {
		return err
	}
	var response struct {
		Streams []struct {
			Port uint16 `plist:"dataPort"`
		} `plist:"streams"`
	}
	if _, err := plist.Unmarshal(res.Body, &response); err != nil || len(response.Streams) != 1 {
		return fmt.Errorf("invalid stream setup: %v", err)
	}
	if _, err = c.request("SET_PARAMETER", "/probe", "text/parameters", []byte(fmt.Sprintf("volume: %f\r\n", volume))); err != nil {
		return err
	}
	host, _, _ := net.SplitHostPort(address)
	media, err := new(net.Dialer).DialContext(ctx, "tcp4", net.JoinHostPort(host, strconv.Itoa(int(response.Streams[0].Port))))
	if err != nil {
		return err
	}
	defer media.Close()
	media.SetDeadline(time.Now().Add(20 * time.Second))
	// A zero network anchor deliberately starts in the past. This probe validates
	// protocol/codec/output, not clock synchronization with a real Apple sender.
	if _, err = c.property("SETRATEANCHORTIME", "/probe", map[string]any{"rate": uint64(1), "rtpTime": uint64(0), "networkTimeSecs": uint64(0), "networkTimeFrac": uint64(0)}); err != nil {
		return err
	}
	aead, _ := chacha20poly1305.New(key)
	seq := uint32(0)
	for len(data) > 0 {
		h, err := adts.ReadHeaderFromBytes(data)
		if err != nil {
			return err
		}
		skip := 7
		if !h.ProtectionAbsent {
			skip = 9
		}
		if h.FrameLength < skip || h.FrameLength > len(data) {
			return fmt.Errorf("truncated AAC fixture")
		}
		packet := make([]byte, 12)
		packet[0] = 0x80
		packet[1] = byte(seq >> 16)
		packet[2] = byte(seq >> 8)
		packet[3] = byte(seq)
		binary.BigEndian.PutUint32(packet[4:8], seq*1024)
		binary.BigEndian.PutUint32(packet[8:12], 0x5100)
		var nonce [12]byte
		binary.LittleEndian.PutUint64(nonce[4:], uint64(seq))
		packet = aead.Seal(packet, nonce[:], data[skip:h.FrameLength], packet[4:12])
		packet = append(packet, nonce[4:]...)
		if err := binary.Write(media, binary.BigEndian, uint16(len(packet)+2)); err != nil {
			return err
		}
		if _, err := media.Write(packet); err != nil {
			return err
		}
		seq++
		data = data[h.FrameLength:]
	}
	if tcp, ok := media.(*net.TCPConn); ok {
		tcp.CloseWrite()
	}
	wait := time.Duration(seq)*1024*time.Second/time.Duration(rate) + 600*time.Millisecond
	select {
	case <-time.After(wait):
	case <-ctx.Done():
		return ctx.Err()
	}
	_, err = c.request("TEARDOWN", "/probe", "application/x-apple-binary-plist", nil)
	return err
}
