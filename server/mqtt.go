package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// MQTT on /v1/stream: a client that negotiates the `mqtt` WebSocket subprotocol gets a receive-only MQTT
// 3.1.1 session on the same URL the JSON frames use, so one address streams in either direction whatever
// the client speaks. It exists for feeders that publish each message as it is decoded (AIS-catcher
// `-Q wssmqtt://x:<token>@ais.openwaters.io:443/v1/stream MSGFORMAT NMEA`): AIS-catcher's HTTP output
// batches on an interval with a one-second floor, and UDP is unauthenticated, so this is the realtime path
// that carries station credit. The token travels as the CONNECT password (username ignored), or in the
// request as on any /v1/stream socket; the token's `sub` is the station. Each PUBLISH payload is
// newline-separated NMEA; topics are ignored. SUBSCRIBE is refused: a subscription would be a data-out path
// around every tier limit, and the JSON frames on this URL exist for reading. Hand-rolled rather than an
// embedded broker, because the receive-only subset is a fixed header, a varint length, and five packet types.

// MQTT 3.1.1 control packet types (high nibble of the first byte).
const (
	mqttConnect     = 1
	mqttConnack     = 2
	mqttPublish     = 3
	mqttPuback      = 4
	mqttPubrec      = 5
	mqttPubrel      = 6
	mqttPubcomp     = 7
	mqttSubscribe   = 8
	mqttSuback      = 9
	mqttUnsubscribe = 10
	mqttUnsuback    = 11
	mqttPingreq     = 12
	mqttPingresp    = 13
	mqttDisconnect  = 14
)

// CONNACK return codes.
const (
	mqttAccepted       = 0x00
	mqttBadProtocol    = 0x01
	mqttUnavailable    = 0x03
	mqttBadCredentials = 0x04
	mqttNotAuthorized  = 0x05
)

const maxMQTTPacket = maxBody // one PUBLISH is one AIS-catcher batch at most; same bound as a /v1/receive post

// offersMQTT reports whether the upgrade request lists mqtt among its subprotocols.
func offersMQTT(r *http.Request) bool {
	for _, v := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "mqtt") {
				return true
			}
		}
	}
	return false
}

type mqttPacket struct {
	typ   byte
	flags byte
	body  []byte
}

// readMQTTPacket reads one control packet from a byte stream. Packets may span or share WebSocket frames,
// so the socket is read as a stream rather than frame by frame.
func readMQTTPacket(r *bufio.Reader) (mqttPacket, error) {
	h, err := r.ReadByte()
	if err != nil {
		return mqttPacket{}, err
	}
	n, mult := 0, 1
	for i := 0; ; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return mqttPacket{}, err
		}
		n += int(b&0x7f) * mult
		if b&0x80 == 0 {
			break
		}
		if i == 3 { // the remaining length is at most four bytes
			return mqttPacket{}, errors.New("mqtt: malformed remaining length")
		}
		mult *= 128
	}
	if n > maxMQTTPacket {
		return mqttPacket{}, errors.New("mqtt: packet too large")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return mqttPacket{}, err
	}
	return mqttPacket{typ: h >> 4, flags: h & 0x0f, body: body}, nil
}

// mqttEncode frames a control packet: fixed header, varint remaining length, body.
func mqttEncode(typ, flags byte, body []byte) []byte {
	out := []byte{typ<<4 | flags}
	n := len(body)
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			break
		}
	}
	return append(out, body...)
}

func mqttID(id uint16) []byte { return []byte{byte(id >> 8), byte(id)} }

// mqttFields decodes the length-prefixed fields of a packet body; the first malformed field sets err and
// every later read returns zero values, so a caller checks err once at the end.
type mqttFields struct {
	b   []byte
	err error
}

func (f *mqttFields) byte() byte {
	if f.err != nil || len(f.b) < 1 {
		f.err = errors.New("mqtt: truncated packet")
		return 0
	}
	v := f.b[0]
	f.b = f.b[1:]
	return v
}

func (f *mqttFields) uint16() uint16 {
	hi, lo := f.byte(), f.byte()
	return uint16(hi)<<8 | uint16(lo)
}

// bytes reads a two-byte-length-prefixed field: a UTF-8 string or the binary password.
func (f *mqttFields) bytes() []byte {
	n := int(f.uint16())
	if f.err != nil || len(f.b) < n {
		f.err = errors.New("mqtt: truncated packet")
		return nil
	}
	v := f.b[:n]
	f.b = f.b[n:]
	return v
}

func (f *mqttFields) string() string { return string(f.bytes()) }

// mqttConnect validates a CONNECT body: protocol, then the token in the password (or the username, for a
// client that puts it there as AIS-catcher's USERPWD may). A CONNECT without credentials falls back to
// the request's own token (`?key=` or a header), already verified as reqClaims or refused as reqErr. The
// client id is ignored: the token names the station. A malformed packet is an error, answered by closing
// without a CONNACK as the spec requires.
func (p *Pipeline) mqttConnect(body []byte, ip string, reqClaims *Claims, reqErr error) (cl *Claims, code byte, keepAlive time.Duration, err error) {
	f := mqttFields{b: body}
	name, level, flags := f.string(), f.byte(), f.byte()
	keepAlive = time.Duration(f.uint16()) * time.Second
	if f.err != nil {
		return nil, 0, 0, f.err
	}
	if !(name == "MQTT" && level == 4) && !(name == "MQIsdp" && level == 3) {
		return nil, mqttBadProtocol, 0, nil
	}
	if flags&0x01 != 0 {
		return nil, 0, 0, errors.New("mqtt: reserved connect flag set")
	}
	f.string()           // client id
	if flags&0x04 != 0 { // will topic and message: accepted and ignored, there is nothing to deliver them to
		f.string()
		f.bytes()
	}
	var user, pass string
	if flags&0x80 != 0 {
		user = f.string()
	}
	if flags&0x40 != 0 {
		pass = string(f.bytes())
	}
	if f.err != nil {
		return nil, 0, 0, f.err
	}
	tok := pass
	if !strings.HasPrefix(tok, tokenPrefix) && strings.HasPrefix(user, tokenPrefix) {
		tok = user
	}
	switch {
	case tok != "" || (reqClaims == nil && reqErr == nil):
		cl, err = p.authorizeToken(tok, ip, "publish")
	case reqErr != nil:
		err = reqErr
	case !reqClaims.may("publish"):
		err = forbiddenError{"role " + reqClaims.Role + " may not publish"}
	default:
		cl = reqClaims
	}
	if errors.As(err, new(forbiddenError)) {
		return nil, mqttNotAuthorized, 0, nil
	}
	if err != nil {
		return nil, mqttBadCredentials, 0, nil
	}
	return cl, mqttAccepted, keepAlive, nil
}

// serveMQTT runs the MQTT session on an accepted /v1/stream socket whose subprotocol is mqtt. The caller has
// started the ping loop; the connect limit and the stream slots are taken here, once CONNECT says whose
// they are. A request token was charged before the upgrade, so it is charged again only if CONNECT names
// a different one.
func (p *Pipeline) serveMQTT(ctx context.Context, c *websocket.Conn, r *http.Request, reqClaims *Claims, reqErr error) {
	ip := clientIP(r)
	nc := websocket.NetConn(ctx, c, websocket.MessageBinary)
	c.SetReadLimit(maxMQTTPacket + 16) // after NetConn, which lifts the limit; a frame is at most one packet's worth
	br := bufio.NewReader(nc)
	write := func(b []byte) error {
		nc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_, err := nc.Write(b)
		return err
	}

	nc.SetReadDeadline(time.Now().Add(10 * time.Second)) // CONNECT must be the first packet, and promptly
	pk, err := readMQTTPacket(br)
	if err != nil || pk.typ != mqttConnect {
		return
	}
	cl, code, keepAlive, err := p.mqttConnect(pk.body, ip, reqClaims, reqErr)
	if err != nil {
		return
	}
	if code == mqttAccepted && cl != reqClaims && !wsConnectLimit.allow(cl.Sub) {
		p.stats.rateLimited.Add(1)
		code = mqttUnavailable
	}
	if code == mqttAccepted {
		release, err := acquireStream(cl, ip)
		if err != nil {
			code = mqttUnavailable
		} else {
			defer release()
		}
	}
	if err := write(mqttEncode(mqttConnack, 0, []byte{0, code})); err != nil || code != mqttAccepted {
		c.Close(websocket.StatusPolicyViolation, "not authorized")
		return
	}
	// A client that asked for a keep-alive must send something within one and a half times it.
	readDeadline := func() {
		if keepAlive > 0 {
			nc.SetReadDeadline(time.Now().Add(keepAlive * 3 / 2))
		} else {
			nc.SetReadDeadline(time.Time{})
		}
	}
	src := stationSource(cl.Sub)
	inflight := map[uint16]struct{}{} // QoS 2 packet ids delivered but not yet released: a retransmit is acknowledged, not ingested again
	for {
		readDeadline()
		pk, err := readMQTTPacket(br)
		if err != nil {
			return
		}
		switch pk.typ {
		case mqttPublish:
			qos := pk.flags >> 1 & 0x03
			f := mqttFields{b: pk.body}
			f.string() // topic: every publish is this station's NMEA, whatever it is filed under
			var id uint16
			if qos > 0 {
				id = f.uint16()
			}
			if f.err != nil || qos == 3 {
				return
			}
			_, redelivered := inflight[id]
			if qos == 2 && !redelivered {
				inflight[id] = struct{}{}
			}
			now := time.Now()
			for _, line := range bytes.Split(f.b, []byte{'\n'}) {
				if qos == 2 && redelivered {
					break
				}
				if len(bytes.TrimSpace(line)) == 0 {
					continue
				}
				if !publishLimit.allow(cl.Sub) {
					p.stats.rateLimited.Add(1)
					break
				}
				p.Ingest(Reception{Source: src, Station: src, RecvTime: now, Body: string(line)})
			}
			var ack []byte
			switch qos {
			case 1:
				ack = mqttEncode(mqttPuback, 0, mqttID(id))
			case 2:
				ack = mqttEncode(mqttPubrec, 0, mqttID(id))
			}
			if ack != nil && write(ack) != nil {
				return
			}
		case mqttPubrel:
			f := mqttFields{b: pk.body}
			id := f.uint16()
			delete(inflight, id)
			if f.err != nil || write(mqttEncode(mqttPubcomp, 0, mqttID(id))) != nil {
				return
			}
		case mqttSubscribe: // refused per filter (0x80) rather than by disconnecting, so the client sees why
			f := mqttFields{b: pk.body}
			id := f.uint16()
			var codes []byte
			for f.err == nil && len(f.b) > 0 {
				f.string()
				f.byte()
				codes = append(codes, 0x80)
			}
			if f.err != nil || len(codes) == 0 || write(mqttEncode(mqttSuback, 0, append(mqttID(id), codes...))) != nil {
				return
			}
		case mqttUnsubscribe:
			f := mqttFields{b: pk.body}
			id := f.uint16()
			if f.err != nil || write(mqttEncode(mqttUnsuback, 0, mqttID(id))) != nil {
				return
			}
		case mqttPingreq:
			if write(mqttEncode(mqttPingresp, 0, nil)) != nil {
				return
			}
		case mqttDisconnect:
			c.Close(websocket.StatusNormalClosure, "")
			return
		case mqttPuback, mqttPubrec, mqttPubcomp: // acknowledgements for messages this end never sends
		default: // a second CONNECT, or a reserved type: protocol violation
			return
		}
	}
}
