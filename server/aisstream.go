package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BertoldVdb/go-ais"
	"github.com/coder/websocket"
)

// aisstream.io as an upstream, for coverage while it lasts. Its /v0 envelopes are decoded go-ais structs,
// so they map straight back: unmarshal `Message[MessageType]` into the struct, re-encode, emit as synthesized.
// Anything Kystverket/Digitraffic already delivered dedupes on the re-encoded payload.

var v0Types = func() map[string]reflect.Type {
	m := map[string]reflect.Type{}
	for _, p := range []ais.Packet{
		ais.PositionReport{}, ais.BaseStationReport{}, ais.ShipStaticData{}, ais.AddressedBinaryMessage{}, ais.BinaryAcknowledge{},
		ais.BinaryBroadcastMessage{}, ais.StandardSearchAndRescueAircraftReport{}, ais.CoordinatedUTCInquiry{}, ais.AddessedSafetyMessage{},
		ais.SafetyBroadcastMessage{}, ais.Interrogation{}, ais.AssignedModeCommand{}, ais.GnssBroadcastBinaryMessage{},
		ais.StandardClassBPositionReport{}, ais.ExtendedClassBPositionReport{}, ais.DataLinkManagementMessage{}, ais.AidsToNavigationReport{},
		ais.ChannelManagement{}, ais.GroupAssignmentCommand{}, ais.StaticDataReport{}, ais.SingleSlotBinaryMessage{},
		ais.MultiSlotBinaryMessage{}, ais.LongRangeAisBroadcastMessage{},
	} {
		m[typeName(p)] = reflect.TypeOf(p)
	}
	return m
}()

type v0Envelope struct {
	MessageType string
	MetaData    struct {
		TimeUTC string `json:"time_utc"`
	}
	Message map[string]json.RawMessage
}

// Go's time.Time.String() layout, which is what aisstream emits.
const goTimeLayout = "2006-01-02 15:04:05.999999999 -0700 MST"

// aisstreamMessage reports whether body carried an AIS message, as opposed to a control frame or one it couldn't read.
func (p *Pipeline) aisstreamMessage(body []byte, now time.Time) (data bool) {
	now, ok := p.admit(now)
	if !ok {
		return
	}
	defer p.release()
	p.arch.write(Reception{Source: "aisstream", Station: "aisstream", RecvTime: now, Body: string(body)})
	var env v0Envelope
	if json.Unmarshal(body, &env) != nil {
		p.stats.parseErr.Add(1)
		return
	}
	rt, ok := v0Types[env.MessageType]
	raw, ok2 := env.Message[env.MessageType]
	if !ok {
		if !controlTypes["aisstream/"+env.MessageType] {
			countUnmappedType("aisstream", env.MessageType)
		}
		return
	}
	if !ok2 {
		return
	}
	if shadowSample("aisstream") {
		shadowCheck("aisstream", body, v0EnvKnown)
		var md struct{ MetaData json.RawMessage }
		if json.Unmarshal(body, &md) == nil && len(md.MetaData) > 0 {
			shadowCheck("aisstream/meta", md.MetaData, v0MetaKnown)
		}
		shadowCheck("aisstream/"+env.MessageType, raw, aisstreamKnown(env.MessageType))
	}
	pv := reflect.New(rt)
	if json.Unmarshal(raw, pv.Interface()) != nil {
		p.stats.parseErr.Add(1)
		return
	}
	t := now
	if st, err := time.Parse(goTimeLayout, env.MetaData.TimeUTC); err == nil && absDur(st.Sub(now)) <= maxSkew {
		t = st
	}
	p.ingestPacket("aisstream", "aisstream", t, now, pv.Elem().Interface().(ais.Packet))
	return true
}

// aisstream caps each connection's throughput. A whole-world subscription keeps up for a minute or two, then
// falls minutes behind until aisstream drops it, so the default splits the world at 4.5°E, where the volume
// divides about evenly, and runs a connection for each half. aisstream closes a third connection on a key a
// second after it subscribes, without an error.
const aisstreamDefaultBoxes = "[[[-90,-180],[90,4.5]],[[-90,4.5],[90,180]]]"

// aisstreamSubscriptions turns AISSTREAM_BBOX into one subscription per connection: the first box gets the
// first connection, and every other box shares the second, since aisstream would close a third.
func aisstreamSubscriptions(bboxJSON string) ([]string, error) {
	var boxes [][][]float64
	if err := json.Unmarshal([]byte(bboxJSON), &boxes); err != nil {
		return nil, err
	}
	if len(boxes) == 0 {
		return nil, errors.New("no bounding boxes")
	}
	for i, b := range boxes {
		if len(b) != 2 || len(b[0]) != 2 || len(b[1]) != 2 || !validLatLon(b[0]) || !validLatLon(b[1]) {
			return nil, fmt.Errorf("box %d is not two [lat, lon] corners: %v", i+1, b)
		}
	}
	groups := [][][][]float64{boxes[:1]}
	if len(boxes) > 1 {
		groups = append(groups, boxes[1:])
	}
	subs := make([]string, len(groups))
	for i, g := range groups {
		sub, _ := json.Marshal(g)
		subs[i] = string(sub)
	}
	return subs, nil
}

func validLatLon(c []float64) bool { return math.Abs(c[0]) <= 90 && math.Abs(c[1]) <= 180 }

// aisstream answers dials made at the same moment with HTTP 429, so connections take turns: each dial
// waits until aisstreamDialGap after the previous one, at startup and after a drop that ended both.
var aisstreamDialGap = 5 * time.Second

var aisstreamDialTurn struct {
	sync.Mutex
	last time.Time
}

func waitAisstreamDialTurn(gap time.Duration) {
	aisstreamDialTurn.Lock()
	defer aisstreamDialTurn.Unlock()
	time.Sleep(time.Until(aisstreamDialTurn.last.Add(gap)))
	aisstreamDialTurn.last = time.Now()
}

// aisstreamConn is one connection's health for /metrics. Both connections ingest as the one aisstream source,
// so the source's age stays fresh while either half delivers; these show each half on its own.
type aisstreamConn struct {
	drops atomic.Int64 // sessions that ended after subscribing; failed dials don't count, so a full outage doesn't
	last  atomic.Int64 // unix nanoseconds of the last AIS message, or of the start before the first one
}

var aisstreamConns sync.Map // subscription -> *aisstreamConn

var aisstreamSilence = upstreamSilence // how long a session waits for a frame before giving up

// newAisstreamConn returns the subscription's entry; two connections with the same boxes share one.
func newAisstreamConn(bboxJSON string) *aisstreamConn {
	c := new(aisstreamConn)
	c.last.Store(time.Now().UnixNano())
	got, _ := aisstreamConns.LoadOrStore(bboxJSON, c)
	return got.(*aisstreamConn)
}

func writeAisstreamMetrics(w io.Writer) {
	var subs []string
	aisstreamConns.Range(func(k, _ any) bool { subs = append(subs, k.(string)); return true })
	if len(subs) == 0 {
		return
	}
	sort.Strings(subs)
	metricHead(w, "aiscast_aisstream_drops_total", "counter", "aisstream sessions that ended after subscribing, per connection")
	for _, s := range subs {
		c, _ := aisstreamConns.Load(s)
		fmt.Fprintf(w, "aiscast_aisstream_drops_total{bbox=%q} %d\n", s, c.(*aisstreamConn).drops.Load())
	}
	metricHead(w, "aiscast_aisstream_last_age_seconds", "gauge", "seconds since each aisstream connection's last message")
	for _, s := range subs {
		c, _ := aisstreamConns.Load(s)
		fmt.Fprintf(w, "aiscast_aisstream_last_age_seconds{bbox=%q} %.0f\n", s, time.Since(time.Unix(0, c.(*aisstreamConn).last.Load())).Seconds())
	}
}

func runAisstream(p *Pipeline, url, apiKey, bboxJSON string) {
	conn := newAisstreamConn(bboxJSON)
	backoff := 5 * time.Second
	for {
		connected, err := aisstreamSession(p, url, apiKey, bboxJSON, conn)
		backoff = reconnectDelay(backoff, sessionLength(connected))
		log.Printf("aisstream %s: %v (retry in %s)", bboxJSON, err, backoff)
		time.Sleep(backoff)
		backoff = min(backoff*2, 5*time.Minute)
	}
}

// sessionLength is how long the session ran once it was established; zero when it never was,
// so a dial or subscribe that blocked a long time and then failed still counts as a failed attempt.
func sessionLength(connected time.Time) time.Duration {
	if connected.IsZero() {
		return 0
	}
	return time.Since(connected)
}

// reconnectDelay is the wait before the next attempt. A session that ran for more than a minute was
// working, so its drop is a blip: start again from the shortest wait instead of keeping the backoff
// earned by earlier failures, which otherwise stays at five minutes for the rest of the process.
func reconnectDelay(backoff, ran time.Duration) time.Duration {
	if ran > time.Minute {
		return 5 * time.Second
	}
	return backoff
}

// aisstreamSession runs one connection and returns when it ends. connected is the time the
// subscription was sent, zero if the session was never established.
func aisstreamSession(p *Pipeline, url, apiKey, bboxJSON string, conn *aisstreamConn) (connected time.Time, err error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waitAisstreamDialTurn(aisstreamDialGap)
	c, err := aisstreamDial(ctx, url)
	if err != nil {
		return connected, err
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)
	sub, _ := json.Marshal(map[string]any{"APIKey": apiKey, "BoundingBoxes": json.RawMessage(bboxJSON)})
	if err := c.Write(ctx, websocket.MessageText, sub); err != nil {
		return connected, err
	}
	connected = time.Now()
	silent := false
	defer func() {
		// A session we ended on silence is aisstream sending nothing: an outage, left to the source's own signals.
		if !silent {
			conn.drops.Add(1)
		}
	}()
	log.Printf("aisstream %s: connected to %s", bboxJSON, url)
	for {
		rctx, rc := context.WithTimeout(ctx, aisstreamSilence)
		_, body, err := c.Read(rctx)
		silent = rctx.Err() == context.DeadlineExceeded
		rc()
		if err != nil {
			return connected, err
		}
		if len(body) > 0 && body[0] == '{' && len(body) < 200 {
			var e struct{ Error string }
			if json.Unmarshal(body, &e) == nil && e.Error != "" {
				return connected, &aisstreamError{e.Error}
			}
		}
		if now := time.Now(); p.aisstreamMessage(body, now) {
			conn.last.Store(now.UnixNano())
		}
	}
}

// aisstream's certificate has lapsed before (aisstream/issues#341). Keep ingesting through a short lapse:
// the chain and hostname are still verified, and only a leaf that expired within certGrace is let through.
// Every other certificate in the chain must be valid now. Go never checks revocation, so for the grace period a
// leaked key from the lapsed certificate could intercept the connection and the API key it carries.
// ponytail: delete once aisstream renews reliably.
const certGrace = 30 * 24 * time.Hour

var aisstreamHTTP = func() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone() // keeps the default proxy and timeouts
	t.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true, // verifyAllowExpired does the verification
		VerifyConnection: func(cs tls.ConnectionState) error {
			if err := verifyAllowExpired(cs, nil, time.Now()); err != nil {
				return fmt.Errorf("aisstream certificate: %w", err)
			}
			return nil
		},
	}
	return &http.Client{Transport: t}
}()

func aisstreamDial(ctx context.Context, url string) (*websocket.Conn, error) {
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: aisstreamHTTP})
	return c, err
}

// roots nil means the system pool.
func verifyAllowExpired(cs tls.ConnectionState, roots *x509.CertPool, now time.Time) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("no peer certificate")
	}
	if cs.ServerName == "" { // an IP-literal URL sends no SNI, and an empty DNSName would skip the hostname check
		return errors.New("aisstream URL must use a hostname")
	}
	leaf := cs.PeerCertificates[0]
	opts := x509.VerifyOptions{DNSName: cs.ServerName, CurrentTime: now, Roots: roots, Intermediates: x509.NewCertPool()}
	for _, c := range cs.PeerCertificates[1:] {
		opts.Intermediates.AddCert(c)
	}
	_, err := leaf.Verify(opts)
	var ce x509.CertificateInvalidError
	if !errors.As(err, &ce) || ce.Reason != x509.Expired || !now.After(leaf.NotAfter) || now.After(leaf.NotAfter.Add(certGrace)) {
		return err
	}
	opts.CurrentTime = leaf.NotAfter
	chains, err := leaf.Verify(opts)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(chains, func(chain []*x509.Certificate) bool {
		return !slices.ContainsFunc(chain[1:], func(c *x509.Certificate) bool { return now.Before(c.NotBefore) || now.After(c.NotAfter) })
	}) {
		return errors.New("certificate chain is not valid now")
	}
	log.Printf("aisstream: accepting certificate for %s that expired %s", cs.ServerName, leaf.NotAfter.UTC().Format(time.RFC3339))
	return nil
}

type aisstreamError struct{ msg string }

func (e *aisstreamError) Error() string { return "server error: " + e.msg }
