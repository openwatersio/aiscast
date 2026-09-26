package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// mqttClient is the subset of an MQTT client the tests need: raw packets over a binary WebSocket.
type mqttClient struct {
	t    *testing.T
	nc   net.Conn
	br   *bufio.Reader
	ws   *websocket.Conn
	resp *http.Response
}

func dialMQTT(t *testing.T, srv *httptest.Server, query string) *mqttClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/stream"+query, &websocket.DialOptions{Subprotocols: []string{"mqtt"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	nc := websocket.NetConn(ctx, c, websocket.MessageBinary)
	return &mqttClient{t: t, nc: nc, br: bufio.NewReader(nc), ws: c, resp: resp}
}

func (m *mqttClient) send(typ, flags byte, body []byte) {
	m.t.Helper()
	if _, err := m.nc.Write(mqttEncode(typ, flags, body)); err != nil {
		m.t.Fatal(err)
	}
}

func (m *mqttClient) read() (mqttPacket, error) {
	m.nc.SetReadDeadline(time.Now().Add(2 * time.Second))
	return readMQTTPacket(m.br)
}

func (m *mqttClient) expect(typ byte) mqttPacket {
	m.t.Helper()
	pk, err := m.read()
	if err != nil || pk.typ != typ {
		m.t.Fatalf("want packet type %d, got %+v %v", typ, pk, err)
	}
	return pk
}

func mqttStr(s string) []byte { return append(mqttID(uint16(len(s))), s...) }

// connectPacket builds a CONNECT body the way AIS-catcher does: MQTT 3.1.1, clean session, keep-alive off,
// empty client id, credentials as given (empty = flag off).
func connectPacket(user, pass string) []byte {
	flags := byte(0x02)
	if user != "" {
		flags |= 0x80
	}
	if pass != "" {
		flags |= 0x40
	}
	b := append(mqttStr("MQTT"), 4, flags, 0, 0)
	b = append(b, mqttStr("")...)
	if user != "" {
		b = append(b, mqttStr(user)...)
	}
	if pass != "" {
		b = append(b, mqttStr(pass)...)
	}
	return b
}

func publishPacket(topic string, id uint16, payload string) []byte {
	b := mqttStr(topic)
	if id != 0 {
		b = append(b, mqttID(id)...)
	}
	return append(b, payload...)
}

const sentence = "!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23"

// The receive-only subset end to end: CONNECT is answered, each PUBLISH line is ingested under the token's
// station, QoS 1 is acknowledged, pings are answered, subscriptions are refused, DISCONNECT closes.
func TestMQTTPublish(t *testing.T) {
	p := testPipeline(t)
	sub := p.subscribe()
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()
	m := dialMQTT(t, srv, "")
	if m.ws.Subprotocol() != "mqtt" {
		t.Errorf("subprotocol %q, want mqtt", m.ws.Subprotocol())
	}
	if l := m.resp.Header.Get("Link"); !strings.Contains(l, termsURL) {
		t.Errorf("terms link missing from the upgrade response: %q", l)
	}
	m.send(mqttConnect, 0, connectPacket("x", "anything")) // ALLOW_ANON: any password is the anonymous admin
	if pk := m.expect(mqttConnack); pk.body[1] != mqttAccepted {
		t.Fatalf("connack %v", pk.body)
	}

	// QoS 0: nothing back, the event arrives
	m.send(mqttPublish, 0, publishPacket("ais/data", 0, sentence+"\r\n"))
	select {
	case ev := <-sub.ch:
		if ev.Source != "station:anon" || ev.Station != "station:anon" || ev.MMSI != 227006760 {
			t.Errorf("unexpected event %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatalf("no event from QoS 0 publish (parse_err=%d)", p.stats.parseErr.Load())
	}

	// QoS 1: PUBACK with the packet id; two sentences in one payload are two receptions
	second := "!BSVDM,1,1,,B,13noH:00000H@P@RSPEakGK@0D33,0*43"
	m.send(mqttPublish, 0x02, publishPacket("ais/data", 0x1234, sentence+"\n"+second+"\n"))
	if pk := m.expect(mqttPuback); string(pk.body) != string(mqttID(0x1234)) {
		t.Errorf("puback id %v", pk.body)
	}
	select {
	case ev := <-sub.ch:
		if ev.MMSI != 258857000 { // the first sentence is a duplicate of the QoS 0 one; only the second is new
			t.Errorf("unexpected event %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no event from QoS 1 publish")
	}
	if d := p.stats.dup.Load(); d != 1 {
		t.Errorf("dup=%d want 1", d)
	}

	// QoS 2: PUBREC, then PUBCOMP for the PUBREL. A retransmit before the release is acknowledged again but
	// not ingested again, whatever the pipeline's own dedupe window would say.
	forget := func() { // drop the pipeline's dedupe memory, so only the packet id can stop a repeat
		p.mu.Lock()
		p.seen = map[string]time.Time{}
		p.mu.Unlock()
	}
	forget()
	m.send(mqttPublish, 0x04, publishPacket("ais/data", 7, sentence))
	if pk := m.expect(mqttPubrec); string(pk.body) != string(mqttID(7)) {
		t.Errorf("pubrec id %v", pk.body)
	}
	select {
	case <-sub.ch:
	case <-time.After(time.Second):
		t.Fatal("no event from QoS 2 publish")
	}
	forget()
	m.send(mqttPublish, 0x0c, publishPacket("ais/data", 7, sentence)) // DUP + QoS 2
	if pk := m.expect(mqttPubrec); string(pk.body) != string(mqttID(7)) {
		t.Errorf("pubrec on retransmit %v", pk.body)
	}
	select {
	case ev := <-sub.ch:
		t.Errorf("retransmit ingested again: %+v", ev.MMSI)
	case <-time.After(200 * time.Millisecond):
	}
	m.send(mqttPubrel, 0x02, mqttID(7))
	if pk := m.expect(mqttPubcomp); string(pk.body) != string(mqttID(7)) {
		t.Errorf("pubcomp id %v", pk.body)
	}
	forget()
	m.send(mqttPublish, 0x04, publishPacket("ais/data", 7, sentence)) // id reused after release: a new message
	m.expect(mqttPubrec)
	select {
	case <-sub.ch:
	case <-time.After(time.Second):
		t.Error("reused packet id after release not ingested")
	}
	m.send(mqttPubrel, 0x02, mqttID(7))
	m.expect(mqttPubcomp)

	m.send(mqttPingreq, 0, nil)
	m.expect(mqttPingresp)

	// SUBSCRIBE is refused per filter, not by disconnecting
	m.send(mqttSubscribe, 0x02, append(append(mqttID(9), mqttStr("ais/#")...), 0))
	if pk := m.expect(mqttSuback); string(pk.body) != string(append(mqttID(9), 0x80)) {
		t.Errorf("suback %v", pk.body)
	}
	m.send(mqttUnsubscribe, 0x02, append(mqttID(10), mqttStr("ais/#")...))
	if pk := m.expect(mqttUnsuback); string(pk.body) != string(mqttID(10)) {
		t.Errorf("unsuback %v", pk.body)
	}

	m.send(mqttDisconnect, 0, nil)
	if _, err := m.read(); err != io.EOF {
		t.Errorf("after DISCONNECT: %v, want EOF", err)
	}
}

// CONNECT carries the token as the password and is answered with the MQTT return code that fits: accepted
// for a publishing role, not authorized for a role that may not publish, bad credentials for no token at all.
func TestMQTTConnectAuth(t *testing.T) {
	p := testPipeline(t)
	allowAnon = false
	defer func() { allowAnon = true }()
	kid, priv := testIssuer(t, p)
	exp := time.Now().Add(time.Hour).Unix()
	feeder, _ := signToken(priv, Claims{Kid: kid, Sub: "st-9", Role: "feeder", Exp: exp})
	partner, _ := signToken(priv, Claims{Kid: kid, Sub: "acme", Role: "partner", Exp: exp})
	elsewhere, _ := signToken(priv, Claims{Kid: kid, Sub: "far", Role: "feeder", Exp: exp, CIDR: []string{"203.0.113.0/24"}})
	sub := p.subscribe()
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()

	cases := []struct {
		name, query, user, pass string
		code                    byte
	}{
		{"feeder token as password", "", "x", feeder, mqttAccepted},
		{"feeder token as username", "", feeder, "", mqttAccepted},
		{"feeder token on the URL, no CONNECT credentials", "?key=" + feeder, "", "", mqttAccepted},
		{"partner may not publish", "", "x", partner, mqttNotAuthorized},
		{"partner token on the URL", "?key=" + partner, "", "", mqttNotAuthorized},
		{"token bound elsewhere", "", "x", elsewhere, mqttNotAuthorized},
		{"garbage", "", "x", "ak1.nope", mqttBadCredentials},
		{"garbage on the URL", "?key=ak1.nope", "", "", mqttBadCredentials},
		{"no credentials", "", "", "", mqttBadCredentials},
	}
	for _, c := range cases {
		m := dialMQTT(t, srv, c.query)
		m.send(mqttConnect, 0, connectPacket(c.user, c.pass))
		pk := m.expect(mqttConnack)
		if pk.body[1] != c.code {
			t.Errorf("%s: connack %d, want %d", c.name, pk.body[1], c.code)
		}
		if c.code != mqttAccepted {
			if _, err := m.read(); err == nil {
				t.Errorf("%s: connection stayed open after refusal", c.name)
			}
			continue
		}
		m.send(mqttPublish, 0, publishPacket("t", 0, sentence))
		select {
		case ev := <-sub.ch:
			if ev.Source != "station:st-9" {
				t.Errorf("%s: source %q", c.name, ev.Source)
			}
		case <-time.After(time.Second):
			t.Errorf("%s: no event", c.name)
		}
		m.send(mqttDisconnect, 0, nil)
		m.read()
		p.mu.Lock()
		p.seen = map[string]time.Time{} // so the next accepted case's sentence is not a duplicate
		p.mu.Unlock()
	}
	// MQTT stations count toward the earned feeder tier like /v1 and HTTP ones
	personal := personalClaims(kid, "ed25519:mq", time.Now())
	for i := 0; i < feederMinEvents24h; i++ {
		p.stations.event(&Event{Station: "station:ed25519:mq", Source: "station:ed25519:mq", Time: time.Now(), MMSI: 1})
	}
	if e := p.effective(&personal); !e.Feeder {
		t.Error("MQTT contribution did not earn the feeder tier")
	}
}

// The connect limit for an MQTT socket is keyed by the token in CONNECT, not by address: a second connect
// on the same token is refused as server unavailable, another token from the same address is not.
func TestMQTTConnectLimitPerToken(t *testing.T) {
	p := testPipeline(t)
	allowAnon = false
	defer func() { allowAnon = true }()
	wsConnectLimit = newLimiter(1)
	defer func() { wsConnectLimit = newLimiter(20) }()
	kid, priv := testIssuer(t, p)
	exp := time.Now().Add(time.Hour).Unix()
	a, _ := signToken(priv, Claims{Kid: kid, Sub: "a", Role: "feeder", Exp: exp})
	b, _ := signToken(priv, Claims{Kid: kid, Sub: "b", Role: "feeder", Exp: exp})
	c, _ := signToken(priv, Claims{Kid: kid, Sub: "c", Role: "feeder", Exp: exp})
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()
	// a is charged for its first connect; b is charged when CONNECT names it even though the request
	// carried c and was charged for that, so b's next connect is over the limit.
	for i, want := range []struct {
		query, pass string
		code        byte
	}{{"", a, mqttAccepted}, {"", a, mqttUnavailable}, {"?key=" + c, b, mqttAccepted}, {"", b, mqttUnavailable}} {
		m := dialMQTT(t, srv, want.query)
		m.send(mqttConnect, 0, connectPacket("x", want.pass))
		if pk := m.expect(mqttConnack); pk.body[1] != want.code {
			t.Errorf("connect %d: connack %d, want %d", i, pk.body[1], want.code)
		}
	}
}

// Before CONNECT names a token, MQTT upgrades are bounded per address, so an unidentified flood cannot
// hold sockets open until their CONNECT deadline.
func TestMQTTAdmitLimitPerAddress(t *testing.T) {
	p := testPipeline(t)
	mqttAdmitLimit = newLimiter(1)
	defer func() { mqttAdmitLimit = newLimiter(200) }()
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()
	dialMQTT(t, srv, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/stream", &websocket.DialOptions{Subprotocols: []string{"mqtt"}}); err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("second unidentified MQTT upgrade: %v %v", err, resp)
	}
}

// Anything but CONNECT first is refused without leaking a CONNACK, and a JSON client on the same URL that
// does not ask for mqtt still gets its welcome frame.
func TestMQTTRefusals(t *testing.T) {
	p := testPipeline(t)
	srv := httptest.NewServer(httpHandler(p))
	defer srv.Close()
	m := dialMQTT(t, srv, "")
	m.send(mqttPublish, 0, publishPacket("t", 0, sentence))
	if pk, err := m.read(); err == nil {
		t.Errorf("publish before connect answered with %+v", pk)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if c.Subprotocol() != "" {
		t.Errorf("JSON socket negotiated %q", c.Subprotocol())
	}
	if _, msg, err := c.Read(ctx); err != nil || !strings.Contains(string(msg), `"welcome"`) {
		t.Errorf("JSON socket: %s %v", msg, err)
	}
}

func TestMQTTFraming(t *testing.T) {
	for _, n := range []int{0, 1, 127, 128, 16383, 16384, maxMQTTPacket} {
		body := make([]byte, n)
		pk, err := readMQTTPacket(bufio.NewReader(strings.NewReader(string(mqttEncode(mqttPublish, 0x02, body)))))
		if err != nil || pk.typ != mqttPublish || pk.flags != 0x02 || len(pk.body) != n {
			t.Errorf("n=%d: %+v %v", n, pk.typ, err)
		}
	}
	// five continuation bytes is not a length, and a packet over the bound is refused before it is read
	for _, bad := range []string{"\x30\x80\x80\x80\x80\x01", string(mqttEncode(mqttPublish, 0, make([]byte, maxMQTTPacket+1)))} {
		if _, err := readMQTTPacket(bufio.NewReader(strings.NewReader(bad))); err == nil {
			t.Error("malformed or oversized packet accepted")
		}
	}
}
