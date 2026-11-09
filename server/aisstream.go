package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"slices"
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

func (p *Pipeline) aisstreamMessage(body []byte, now time.Time) {
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
}

func runAisstream(p *Pipeline, url, apiKey, bboxJSON string) {
	backoff := 5 * time.Second
	for {
		err := aisstreamSession(p, url, apiKey, bboxJSON)
		log.Printf("aisstream: %v (retry in %s)", err, backoff)
		time.Sleep(backoff)
		backoff = min(backoff*2, 5*time.Minute)
	}
}

func aisstreamSession(p *Pipeline, url, apiKey, bboxJSON string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := aisstreamDial(ctx, url)
	if err != nil {
		return err
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)
	sub, _ := json.Marshal(map[string]any{"APIKey": apiKey, "BoundingBoxes": json.RawMessage(bboxJSON)})
	if err := c.Write(ctx, websocket.MessageText, sub); err != nil {
		return err
	}
	log.Printf("aisstream: connected to %s", url)
	for {
		rctx, rc := context.WithTimeout(ctx, upstreamSilence)
		_, body, err := c.Read(rctx)
		rc()
		if err != nil {
			return err
		}
		if len(body) > 0 && body[0] == '{' && len(body) < 200 {
			var e struct{ Error string }
			if json.Unmarshal(body, &e) == nil && e.Error != "" {
				return &aisstreamError{e.Error}
			}
		}
		p.aisstreamMessage(body, time.Now())
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
