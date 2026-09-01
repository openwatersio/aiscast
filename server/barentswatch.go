package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BertoldVdb/go-ais"
)

// BarentsWatch Live AIS (NLOD, credit "Data delivered by BarentsWatch"): the same AIS Norge network as the
// kystverket TCP feed plus satellite and offshore receivers covering the full EEZ and Svalbard, as
// line-delimited JSON over a chunked HTTPS stream. Runs continuously; the pipeline's advance rule for
// rebuilt events (vessels.go) withholds its copies of transmissions kystverket already delivered raw, so
// this source contributes the vessels only it can hear and takes over per vessel from kystverket's next
// missed transmission onward.

const bwTokenURL = "https://id.barentswatch.no/connect/token"

// bwMessage is one stream line; type Position (1/2/3/9/18/19/27), Staticdata (5/24), Aton (21). JSON null
// leaves a field untouched, so sentinel defaults set before unmarshal survive as AIS "not available".
type bwMessage struct {
	Type        string    `json:"type"`
	MessageType int       `json:"messageType"`
	MMSI        uint32    `json:"mmsi"`
	Msgtime     time.Time `json:"msgtime"`
	Stream      string    `json:"stream"` // terra | satellite | offshore
	AisClass    string    `json:"aisClass"`
	Latitude    float64   `json:"latitude"`
	Longitude   float64   `json:"longitude"`
	Cog         float64   `json:"courseOverGround"`
	Sog         float64   `json:"speedOverGround"`
	Heading     uint16    `json:"trueHeading"`
	NavStat     uint8     `json:"navigationalStatus"`
	Rot         float64   `json:"rateOfTurn"` // degrees per minute
	IMO         uint32    `json:"imoNumber"`
	CallSign    string    `json:"callSign"`
	Name        string    `json:"name"`
	Dest        string    `json:"destination"`
	Eta         string    `json:"eta"`     // "MMDDHHmm" digits; "00002460" = not available
	Draught     uint16    `json:"draught"` // 0.1 m
	ShipType    uint8     `json:"shipType"`
	DimA        uint16    `json:"dimensionA"`
	DimB        uint16    `json:"dimensionB"`
	DimC        uint8     `json:"dimensionC"`
	DimD        uint8     `json:"dimensionD"`
	FixType     uint8     `json:"positionFixingDeviceType"`
	AtonType    uint8     `json:"typeOfAidsToNavigation"`
	AtonFix     uint8     `json:"typeOfElectronicFixingDevice"`
}

func newBwMessage() bwMessage {
	return bwMessage{Latitude: 91, Longitude: 181, Cog: 360, Sog: 102.3, Heading: 511, NavStat: 15, Rot: math.NaN()}
}

func (m bwMessage) position() ais.Packet {
	lat, lon := ais.FieldLatLonFine(m.Latitude), ais.FieldLatLonFine(m.Longitude)
	if m.MessageType == 9 { // SAR aircraft: type 9 carries whole-knot SOG (n/a 1023) and no nav status
		sog := uint16(1023)
		if m.Sog != 102.3 && m.Sog < 1023 {
			sog = uint16(math.Round(m.Sog))
		}
		return ais.StandardSearchAndRescueAircraftReport{
			Header: ais.Header{MessageID: 9, UserID: m.MMSI}, Valid: true,
			Altitude: 4095, Sog: sog, Longitude: lon, Latitude: lat, Cog: ais.Field10(m.Cog),
			Timestamp: uint8(m.Msgtime.Second()),
		}
	}
	sog := math.Min(m.Sog, 102.3) // types 1/18 carry a 10-bit SOG; the encoder rejects rather than clamps
	if m.AisClass == "B" {
		return ais.StandardClassBPositionReport{
			Header: ais.Header{MessageID: 18, UserID: m.MMSI}, Valid: true,
			Sog: ais.Field10(sog), Longitude: lon, Latitude: lat, Cog: ais.Field10(m.Cog),
			TrueHeading: m.Heading, Timestamp: uint8(m.Msgtime.Second()),
		}
	}
	rot := int16(-128) // n/a
	if !math.IsNaN(m.Rot) {
		raw := 4.733 * math.Sqrt(math.Abs(m.Rot)) // AIS ROT field is 4.733*sqrt(deg/min), signed
		rot = int16(math.Copysign(math.Min(math.Round(raw), 126), m.Rot))
	}
	return ais.PositionReport{
		Header: ais.Header{MessageID: 1, UserID: m.MMSI}, Valid: true,
		NavigationalStatus: m.NavStat, RateOfTurn: rot, Sog: ais.Field10(sog),
		Longitude: lon, Latitude: lat, Cog: ais.Field10(m.Cog),
		TrueHeading: m.Heading, Timestamp: uint8(m.Msgtime.Second()),
	}
}

// bw6bit fits free text into an AIS 6-bit string field: uppercased, anything outside the charset becomes
// '?', truncated to the field's character width. The encoder rejects the whole packet otherwise.
func bw6bit(s string, width int) string {
	rs := []rune(strings.ToUpper(s))
	if len(rs) > width {
		rs = rs[:width]
	}
	b := make([]byte, len(rs))
	for i, r := range rs {
		if r < 32 || r > 95 {
			r = '?'
		}
		b[i] = byte(r)
	}
	return string(b)
}

func (m bwMessage) static() ais.Packet {
	eta := ais.FieldETA{Hour: 24, Minute: 60}
	if len(m.Eta) == 8 { // "MMDDHHmm"
		var mo, d, h, mi uint8
		if _, err := fmt.Sscanf(m.Eta, "%02d%02d%02d%02d", &mo, &d, &h, &mi); err == nil &&
			mo <= 12 && d <= 31 && h <= 24 && mi <= 60 {
			eta = ais.FieldETA{Month: mo, Day: d, Hour: h, Minute: mi}
		}
	}
	return ais.ShipStaticData{
		Header: ais.Header{MessageID: 5, UserID: m.MMSI}, Valid: true,
		ImoNumber: m.IMO, CallSign: bw6bit(m.CallSign, 7), Name: bw6bit(m.Name, 20), Type: m.ShipType,
		Dimension: ais.FieldDimension{A: m.DimA, B: m.DimB, C: m.DimC, D: m.DimD},
		FixType:   m.FixType, Eta: eta,
		MaximumStaticDraught: ais.Field10(math.Min(float64(m.Draught), 255) / 10), Destination: bw6bit(m.Dest, 20),
	}
}

func (m bwMessage) aton() ais.Packet {
	return ais.AidsToNavigationReport{
		Header: ais.Header{MessageID: 21, UserID: m.MMSI}, Valid: true,
		Type: m.AtonType, Name: bw6bit(m.Name, 34),
		Longitude: ais.FieldLatLonFine(m.Longitude), Latitude: ais.FieldLatLonFine(m.Latitude),
		Dimension: ais.FieldDimension{A: m.DimA, B: m.DimB, C: m.DimC, D: m.DimD},
		Fixtype:   m.AtonFix, Timestamp: 60,
	}
}

func (p *Pipeline) barentswatchLine(line []byte, now time.Time) {
	p.arch.write(Reception{Source: "barentswatch", Station: "barentswatch", RecvTime: now, Body: string(line)})
	m := newBwMessage()
	err := json.Unmarshal(line, &m)
	if _, mismatch := err.(*json.UnmarshalTypeError); err != nil && !mismatch { // one odd field doesn't void the rest
		p.stats.parseErr.Add(1)
		return
	}
	if m.MMSI == 0 {
		p.stats.parseErr.Add(1)
		return
	}
	var pkt ais.Packet
	switch m.Type {
	case "Position":
		if m.Latitude == 91 { // null position: nothing to synthesize
			return
		}
		pkt = m.position()
	case "Staticdata":
		pkt = m.static()
	case "Aton":
		pkt = m.aton()
	default: // MetHyd weather broadcasts and the like
		return
	}
	station := "barentswatch"
	if m.Stream != "" {
		station += "/" + m.Stream
	}
	// msgtime goes through as-is: ingestPacket caps zero or future stamps at receive time
	p.ingestPacketAt("barentswatch", station, m.Msgtime, pkt)
}

func barentswatchToken(clientID, clientSecret string) (string, time.Duration, error) {
	client := &http.Client{Timeout: 30 * time.Second} // bounds the whole request; a stalled IdP must not park the source goroutine
	res, err := client.PostForm(bwTokenURL, url.Values{
		"client_id": {clientID}, "client_secret": {clientSecret},
		"scope": {"ais"}, "grant_type": {"client_credentials"},
	})
	if err != nil {
		return "", 0, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return "", 0, err
	}
	if res.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(body)))
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &t); err != nil || t.AccessToken == "" {
		return "", 0, fmt.Errorf("bad token response: %v", err)
	}
	return t.AccessToken, time.Duration(t.ExpiresIn) * time.Second, nil
}

func barentswatchStream(p *Pipeline, client *http.Client, streamURL, token string) error {
	req, err := http.NewRequest("GET", streamURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		err := fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(body)))
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			err = fmt.Errorf("%w: %w", errBwAuth, err) // the cached token is no good; mint a new one
		}
		return err
	}
	log.Printf("barentswatch: connected to %s", streamURL)
	// net/http has no per-read deadline; a stalled but open stream is killed like source.go's 2 min one
	watchdog := time.AfterFunc(2*time.Minute, func() { res.Body.Close() })
	defer watchdog.Stop()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		watchdog.Reset(2 * time.Minute)
		if line := strings.TrimSpace(sc.Text()); line != "" {
			p.barentswatchLine([]byte(line), time.Now())
		}
	}
	return sc.Err()
}

var errBwAuth = errors.New("token rejected")

func runBarentswatch(p *Pipeline, streamURL, clientID, clientSecret string) {
	backoff := time.Second
	// no overall timeout (the stream is long-lived), but headers must arrive before the read watchdog can
	client := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 30 * time.Second}}
	var token string
	var expiry time.Time
	for {
		if time.Until(expiry) < 5*time.Minute {
			t, ttl, err := barentswatchToken(clientID, clientSecret)
			if err != nil {
				log.Printf("barentswatch: token: %v (retry in %s)", err, backoff)
				time.Sleep(backoff)
				backoff = min(backoff*2, time.Minute)
				continue
			}
			token, expiry = t, time.Now().Add(ttl)
		}
		start := time.Now()
		err := barentswatchStream(p, client, streamURL, token)
		log.Printf("barentswatch: disconnected: %v", err)
		if errors.Is(err, errBwAuth) {
			expiry = time.Time{}
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		time.Sleep(backoff)
		backoff = min(backoff*2, time.Minute)
	}
}
