package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BertoldVdb/go-ais"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Fintraffic Digitraffic marine AIS over MQTT (CC BY 4.0). JSON per vessel, not NMEA: mapped to go-ais
// structs and re-encoded, so events from here are `synthesized`. Originals are archived as received.

type dtLocation struct {
	Time    int64   `json:"time"` // epoch seconds
	Sog     float64 `json:"sog"`
	Cog     float64 `json:"cog"`
	NavStat uint8   `json:"navStat"`
	Rot     int16   `json:"rot"`
	PosAcc  bool    `json:"posAcc"`
	Raim    bool    `json:"raim"`
	Heading uint16  `json:"heading"`
	Lon     float64 `json:"lon"`
	Lat     float64 `json:"lat"`
}

type dtMetadata struct {
	Timestamp   int64  `json:"timestamp"` // epoch milliseconds
	Destination string `json:"destination"`
	Name        string `json:"name"`
	Draught     uint16 `json:"draught"` // 0.1 m
	Eta         uint32 `json:"eta"`     // packed AIS ETA: month 4 bits, day 5, hour 5, minute 6
	PosType     uint8  `json:"posType"`
	RefA        uint16 `json:"refA"`
	RefB        uint16 `json:"refB"`
	RefC        uint8  `json:"refC"`
	RefD        uint8  `json:"refD"`
	CallSign    string `json:"callSign"`
	Imo         uint32 `json:"imo"`
	Type        uint8  `json:"type"`
}

func (l dtLocation) packet(mmsi uint32) ais.Packet {
	// Digitraffic doesn't say class A or B; everything becomes a type 1 report.
	return ais.PositionReport{
		Header: ais.Header{MessageID: 1, UserID: mmsi}, Valid: true,
		NavigationalStatus: l.NavStat, RateOfTurn: l.Rot, Sog: ais.Field10(l.Sog), PositionAccuracy: l.PosAcc,
		Longitude: ais.FieldLatLonFine(l.Lon), Latitude: ais.FieldLatLonFine(l.Lat), Cog: ais.Field10(l.Cog),
		TrueHeading: l.Heading, Timestamp: uint8(l.Time % 60), Raim: l.Raim,
	}
}

func (m dtMetadata) packet(mmsi uint32) ais.Packet {
	return ais.ShipStaticData{
		Header: ais.Header{MessageID: 5, UserID: mmsi}, Valid: true,
		ImoNumber: m.Imo, CallSign: m.CallSign, Name: m.Name, Type: m.Type,
		Dimension: ais.FieldDimension{A: m.RefA, B: m.RefB, C: m.RefC, D: m.RefD}, FixType: m.PosType,
		Eta:                  ais.FieldETA{Month: uint8(m.Eta >> 16 & 0xF), Day: uint8(m.Eta >> 11 & 0x1F), Hour: uint8(m.Eta >> 6 & 0x1F), Minute: uint8(m.Eta & 0x3F)},
		MaximumStaticDraught: ais.Field10(float64(m.Draught) / 10),
		Destination:          m.Destination,
	}
}

func (p *Pipeline) digitrafficMessage(topic string, body []byte) {
	now := time.Now()
	p.arch.write(Reception{Source: "digitraffic", Station: "digitraffic", RecvTime: now, Body: topic + " " + string(body)})
	parts := strings.Split(topic, "/") // vessels-v2/<mmsi>/location|metadata
	if len(parts) != 3 {
		return
	}
	mmsi, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return
	}
	var pkt ais.Packet
	t := now
	switch parts[2] {
	case "location":
		var l dtLocation
		if json.Unmarshal(body, &l) != nil {
			p.stats.parseErr.Add(1)
			return
		}
		pkt = l.packet(uint32(mmsi))
		if st := time.Unix(l.Time, 0); absDur(st.Sub(now)) <= maxSkew {
			t = st
		}
	case "metadata":
		var m dtMetadata
		if json.Unmarshal(body, &m) != nil {
			p.stats.parseErr.Add(1)
			return
		}
		pkt = m.packet(uint32(mmsi))
		if st := time.UnixMilli(m.Timestamp); absDur(st.Sub(now)) <= maxSkew {
			t = st
		}
	default:
		return
	}
	p.ingestPacket("digitraffic", "digitraffic", t, pkt)
}

func runDigitraffic(p *Pipeline, url string) {
	host, _ := os.Hostname()
	opts := mqtt.NewClientOptions().AddBroker(url).
		SetClientID(fmt.Sprintf("aiscast-%s-%d", host, os.Getpid())).
		SetHTTPHeaders(http.Header{"Digitraffic-User": {"aiscast"}}). // asked for by the ToS; lifts the per-IP limits
		SetAutoReconnect(true).SetConnectRetry(true).SetConnectRetryInterval(10 * time.Second).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) { log.Printf("digitraffic: connection lost: %v", err) }).
		SetOnConnectHandler(func(c mqtt.Client) {
			log.Printf("digitraffic: connected to %s", url)
			c.Subscribe("vessels-v2/+/location", 0, func(_ mqtt.Client, m mqtt.Message) { p.digitrafficMessage(m.Topic(), m.Payload()) })
			c.Subscribe("vessels-v2/+/metadata", 0, func(_ mqtt.Client, m mqtt.Message) { p.digitrafficMessage(m.Topic(), m.Payload()) })
		})
	if tok := mqtt.NewClient(opts).Connect(); tok.Wait() && tok.Error() != nil {
		log.Printf("digitraffic: %v (auto-retrying)", tok.Error())
	}
}
